package harness

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// This file turns Argo's generic per-node "main: Error (exit code 1)" into the
// actual proximate error (a tofu/terragrunt error, a helm timeout, ...) for the
// harness failure Slack card. The source is already on the child Workflow CR: every
// failed Pod node carries the exact S3 key of its own archived container log
// (outputs.artifacts["main-logs"].s3.key), and the CR carries the bucket
// (status.artifactRepositoryRef.artifactRepository.s3.bucket). No kubectl logs, no
// new RBAC: post-status already runs as a ServiceAccount whose Pod Identity role
// holds s3:GetObject on that bucket.

// ---- minimal decode shapes for `kubectl get workflows -o json` ----

// WorkflowList is the shape of `kubectl -n argo get workflows -l ... -o json`.
type WorkflowList struct {
	Items []Workflow `json:"items"`
}

type Workflow struct {
	Metadata WorkflowMetadata `json:"metadata"`
	Status   WorkflowStatus   `json:"status"`
}

type WorkflowMetadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type WorkflowStatus struct {
	Phase                 string                `json:"phase"`
	Message               string                `json:"message"`
	ArtifactRepositoryRef ArtifactRepositoryRef `json:"artifactRepositoryRef"`
	Nodes                 map[string]Node       `json:"nodes"`
}

type ArtifactRepositoryRef struct {
	ArtifactRepository ArtifactRepository `json:"artifactRepository"`
}

type ArtifactRepository struct {
	S3 S3RepoRef `json:"s3"`
}

type S3RepoRef struct {
	Bucket string `json:"bucket"`
}

type Node struct {
	ID          string      `json:"id"`
	Type        string      `json:"type"`
	Phase       string      `json:"phase"`
	DisplayName string      `json:"displayName"`
	Message     string      `json:"message"`
	FinishedAt  string      `json:"finishedAt"`
	Outputs     NodeOutputs `json:"outputs"`
}

type NodeOutputs struct {
	Artifacts []NodeArtifact `json:"artifacts"`
}

type NodeArtifact struct {
	Name string          `json:"name"`
	S3   *NodeArtifactS3 `json:"s3,omitempty"`
}

type NodeArtifactS3 struct {
	Key string `json:"key"`
}

// artifactKey returns the S3 key of a node's archived container log, or "" if the
// node has none (e.g. it never started, or the archive step never ran).
func artifactKey(n Node) string {
	for _, a := range n.Outputs.Artifacts {
		if a.Name == "main-logs" && a.S3 != nil {
			return a.S3.Key
		}
	}
	return ""
}

// failedPodNodes returns a child's failed Pod nodes, sorted by FinishedAt then
// DisplayName for determinism. When capped at maxNodes (0 = unlimited), it keeps the
// EARLIEST maxNodes by FinishedAt, not the latest - a retried step lands one failed
// Pod node per attempt, all carrying the same underlying error, and keeping the
// earliest one means the originating failure (and its failed_phase/log_uri) survives
// the retries instead of being pushed out by them.
//
// Filtering on Type == "Pod" is what excludes the duplicate Retry node Argo also
// marks Failed for the same phase - Retry nodes carry no artifact of their own, and
// including them would double every bullet. Steps/StepGroup nodes are excluded the
// same way.
func failedPodNodes(wf Workflow, maxNodes int) []Node {
	var nodes []Node
	for _, n := range wf.Status.Nodes {
		if n.Type != "Pod" {
			continue
		}
		if n.Phase != "Failed" && n.Phase != "Error" {
			continue
		}
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].FinishedAt != nodes[j].FinishedAt {
			return nodes[i].FinishedAt < nodes[j].FinishedAt
		}
		return nodes[i].DisplayName < nodes[j].DisplayName
	})
	if maxNodes > 0 && len(nodes) > maxNodes {
		nodes = nodes[:maxNodes]
	}
	return nodes
}

// ---- fetching the log tail ----

// LogTail fetches the tail of an archived container log. An interface so Build is
// testable without S3.
type LogTail interface {
	Tail(ctx context.Context, bucket, key string) (string, error)
}

// S3GetObjectAPI is the minimal S3 surface the tail fetch needs.
type S3GetObjectAPI interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// tailRangeBytes bounds the fetch cost regardless of log size: the pr440 teardown
// log is 2.2MB / 14,822 lines, the two upgrade logs are under 1.5KB. Either way we
// only ever pull the last 256KiB.
const tailRangeBytes = 256 * 1024

// S3LogTail fetches log tails from the Argo artifact bucket via a suffix byte range.
type S3LogTail struct {
	Client S3GetObjectAPI
}

func NewS3LogTail(client S3GetObjectAPI) *S3LogTail { return &S3LogTail{Client: client} }

func (t *S3LogTail) Tail(ctx context.Context, bucket, key string) (string, error) {
	out, err := t.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=-%d", tailRangeBytes)),
	})
	if err != nil {
		return "", err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return "", err
	}
	s := string(b)
	// A suffix range that lands mid-file starts partway through a line, so line 1 is
	// possibly cut in half - drop it rather than risk it reading like a truncated
	// error. But when the object is smaller than tailRangeBytes, S3 answers the
	// suffix request with the WHOLE object (still 206 Partial Content, but
	// Content-Range's start offset is 0): line 1 is then intact, and dropping it
	// would wrongly discard a short log's only content - including turning a
	// single-line log with no trailing newline into "" instead of its real text.
	if s3PartialStart(out.ContentRange) {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			return s[idx+1:], nil
		}
		return "", nil
	}
	return s, nil
}

// s3PartialStart reports whether an S3 Content-Range response header ("bytes
// start-end/total") indicates the returned bytes begin after offset 0 - i.e. this is
// a genuine partial read and its first line may be a fragment. A missing or
// unparsable header is treated as a whole-object read (the safer default: it keeps
// content rather than risking silently dropping line 1 of a short log).
func s3PartialStart(contentRange *string) bool {
	if contentRange == nil {
		return false
	}
	var start int64
	if _, err := fmt.Sscanf(*contentRange, "bytes %d-", &start); err != nil {
		return false
	}
	return start > 0
}

// ---- error extraction ----

const errWindowLines = 400

var (
	ansiRE     = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	tsPrefixRE = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3}\s+(STDOUT|STDERR|ERROR|WARN|INFO)\s*`)

	verdictRE       = regexp.MustCompile(`^(provision|upgrade|validate|teardown) failed: `)
	verdictPrefixRE = regexp.MustCompile(`^(?:provision|upgrade|validate|teardown) failed: `)
	thinVerdictRE   = regexp.MustCompile(`^([\w./-]+: )?exit (status|code) \d+$`)

	// awsSDKBoilerplateRE matches the aws-sdk-go-v2 error wrapper that every service
	// error carries ("operation error <Service>: <Operation>, https response error
	// StatusCode: <NNN>, RequestID: <uuid>, ") ahead of the actual diagnostic (e.g.
	// "InvalidResourceStateFault: ... cannot be stopped."). It is pure noise - the
	// service/operation/status code/request id never explain the failure - and at
	// ~150 runes it is often the difference between the real diagnostic surviving
	// the per-line excerpt cap or being truncated away before it appears.
	awsSDKBoilerplateRE = regexp.MustCompile(`operation error [^,]+, https response error StatusCode: \d+, RequestID: [0-9a-fA-F-]+, `)

	// errLineRE deliberately does NOT get its leading "* " stripped by
	// normalizeLine (see there) - stripping it would make this pattern
	// unmatchable against terragrunt's own "* Failed to execute ..." banner.
	errLineRE = regexp.MustCompile(`^(Error:|ERROR\b|fatal:|panic:|\* Failed to execute\b)`)

	noiseRE = regexp.MustCompile(`Still (destroying|creating|modifying)|Destruction complete|Creation complete|^error occurred:$|^exit status \d+$`)
)

// normalizeLine strips ANSI color codes, terragrunt's "HH:MM:SS.mmm LEVEL"
// timestamp prefix, a repeated leading "tofu: " marker, the aws-sdk-go-v2 error
// wrapper boilerplate, and leading box-drawing decoration, then trims whitespace.
//
// The leading-decoration strip intentionally leaves a bare "*" alone: that is the
// bullet on terragrunt's own "* Failed to execute ..." error banner, which errLineRE
// matches on. Only the box-drawing run/error markers (│ ╷ ╵) are decoration here.
func normalizeLine(line string) string {
	line = strings.TrimRight(line, "\r")
	line = ansiRE.ReplaceAllString(line, "")
	line = tsPrefixRE.ReplaceAllString(line, "")
	// Prefix-anchored only: "tofu: " can legitimately appear mid-line (e.g. an error
	// message that itself mentions running tofu), and stripping it wherever it
	// occurs would silently mangle that text. terragrunt can repeat the marker
	// ("tofu: tofu: ..."), so trim it off the front in a loop.
	for strings.HasPrefix(line, "tofu: ") {
		line = strings.TrimPrefix(line, "tofu: ")
	}
	line = awsSDKBoilerplateRE.ReplaceAllString(line, "")
	line = strings.TrimLeft(line, "│╷╵ \t")
	return strings.TrimSpace(line)
}

// normalizeAndWindow normalizes every line of log, then keeps only the last
// errWindowLines of them - the heuristic's contract is that anything further back
// is not inspected. (This is also why terragrunt.go wraps its own error at the
// source: a tofu failure that scrolls past this window would otherwise degrade to
// a bare exit code.)
func normalizeAndWindow(log string) []string {
	raw := strings.Split(log, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		lines[i] = normalizeLine(l)
	}
	if len(lines) > errWindowLines {
		lines = lines[len(lines)-errWindowLines:]
	}
	return lines
}

// findErrLine scans lines bottom-up for the last line that looks like a real error,
// skipping blanks, polling noise, and the exclude line (the verdict, so it is never
// picked as its own explanation).
//
// Bottom-up on purpose: a superseded, non-fatal error the harness deliberately
// continued past (e.g. "logical destroy failed (continuing to physical so infra
// isn't stranded)") can sit thousands of lines above the real failure. First-match
// would report the wrong error.
func findErrLine(lines []string, exclude string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l == "" || l == exclude {
			continue
		}
		if noiseRE.MatchString(l) {
			continue
		}
		if errLineRE.MatchString(l) {
			return l
		}
	}
	return ""
}

// ExtractError picks the single most useful line out of a (possibly truncated) log
// tail, or "" if nothing in the window looks like a real error - callers fall back
// to the Argo node message in that case.
//
// verdict is the last line matching "<phase> failed: ..." - the one choke point
// every phase failure in cmd/harness/main.go funnels through. It is usually the most
// specific thing available (e.g. "upgrade failed: worktree add <sha>: exit status
// 128"). But when the wrapped error is itself just a bare exit code ("teardown
// failed: destroy: exit status 1" - a "thin" verdict), the real explanation is the
// tofu/terragrunt error underneath, so errLine wins instead.
func ExtractError(log string) string {
	return extractErrorFromLines(normalizeAndWindow(log))
}

// extractErrorFromLines is ExtractError's body, split out so callers that already
// have a normalized/windowed line slice (Build's phase extraction, below) can reuse
// it without re-normalizing the same log twice.
func extractErrorFromLines(lines []string) string {
	verdict := ""
	for _, l := range lines {
		if verdictRE.MatchString(l) {
			verdict = l
		}
	}

	errLine := findErrLine(lines, verdict)

	if verdict != "" {
		rest := verdictPrefixRE.ReplaceAllString(verdict, "")
		if thinVerdictRE.MatchString(rest) && errLine != "" {
			return errLine
		}
		return verdict
	}
	return errLine
}

// verdictPhase scans normalized/windowed log lines for the phase word off the last
// "<phase> failed: ..." verdict line - the same line extractErrorFromLines' own
// verdict scan finds - and returns "" if the window carries none.
func verdictPhase(lines []string) string {
	phase := ""
	for _, l := range lines {
		if m := verdictRE.FindStringSubmatch(l); m != nil {
			phase = m[1]
		}
	}
	return phase
}

// displayPhaseRE captures the leading alphabetic phase word off a Pod node's
// DisplayName ("upgrade(0)" -> "upgrade", "destroy(0)" -> "destroy").
var displayPhaseRE = regexp.MustCompile(`^[a-zA-Z]+`)

// phaseFromDisplayName is the failed_phase fallback for when a node's log carries no
// "<phase> failed: ..." verdict line to source the phase word from instead (the
// fetch itself failed, the log has no verdict line, or the window scrolled past it).
func phaseFromDisplayName(displayName string) string {
	return displayPhaseRE.FindString(displayName)
}

// lastErrorLine is ExtractError's errLine step alone, capped to 400 runes. It exists
// for terragrunt.go, which wraps Apply/destroyModule's captured output into the
// returned error before cmd/harness/main.go ever writes a "<phase> failed: ..."
// verdict line - so there is no verdict to prefer yet, just the tofu/terragrunt
// output itself.
func lastErrorLine(out string) string {
	lines := normalizeAndWindow(out)
	return truncateRunes(findErrLine(lines, ""), 400)
}

// truncateRunes cuts s to at most n runes on a rune boundary, appending "…" (which
// counts toward n) when it truncates.
func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ---- secret scrubbing ----

// privateKeyLineRE matches both the classic PKCS#1-style headers ("-----BEGIN RSA
// PRIVATE KEY-----") and the algorithm-less PKCS#8 header ("-----BEGIN PRIVATE
// KEY-----").
var privateKeyLineRE = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)

// scrubRule applies re, replacing matches with repl (which may reference capture
// groups, e.g. "$1$2[redacted]").
type scrubRule struct {
	re   *regexp.Regexp
	repl string
}

// scrubRules runs in order: specific token shapes first (so a Vault/AWS/Slack/
// GitHub/JWT token is labeled precisely), then the generic password/token keyword
// rule, then the generic base64-blob catch-all last (so it never eats a shorter,
// already-labeled replacement).
//
// The blob threshold is 60, not 40: a 40-hex git SHA must survive scrubbing (in the
// bad-ref specimen, the SHA IS the diagnostic). AWS account ids and ARNs are not
// scrubbed at all - they are already all over the existing cards and PR comments.
var scrubRules = []scrubRule{
	{regexp.MustCompile(`hvs\.[A-Za-z0-9_-]{20,}`), "[redacted-vault-token]"},
	{regexp.MustCompile(`hvb\.[A-Za-z0-9_-]{20,}`), "[redacted-vault-token]"},
	{regexp.MustCompile(`\bs\.[A-Za-z0-9]{24,}\b`), "[redacted-vault-token]"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "[redacted-akid]"},
	{regexp.MustCompile(`xox[abprs]-[A-Za-z0-9-]{10,}`), "[redacted-slack-token]"},
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), "[redacted-github-token]"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`), "[redacted-github-token]"},
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[redacted-jwt]"},
	// Embedded keyword rule: the keyword can sit anywhere inside a longer
	// identifier (AWS_SECRET_ACCESS_KEY, DB_PASSWORD, ...), not just stand alone -
	// the old alternation (password|passwd|secret|token|api[_-]?key|session[_-]?key)
	// required the keyword to butt directly against the separator, so
	// "AWS_SECRET_ACCESS_KEY=..." never matched (the "_ACCESS_KEY" in between broke
	// it). Only the value is replaced, so the variable name stays for context.
	//
	// The value must not start with "[" - several labels above ("[redacted-vault-
	// token]", "[redacted-slack-token]", "[redacted-github-token]") contain the word
	// "token", and without this guard this rule would run over its own output and
	// flatten those specific labels down to a bare "[redacted]".
	// Bare "key" is deliberately absent from the alternation: it would redact
	// kms_key_id ARNs and "creating KMS Key:" lines out of exactly the tofu
	// errors this excerpt exists to show. Only qualified key forms are
	// credential-shaped.
	{regexp.MustCompile(`(?i)([A-Za-z_]*(?:secret|password|passwd|token|(?:api|access|private|signing|secret)[_-]?key)[A-Za-z_]*\s*[:=]\s*)[^\s\[]\S*`), "$1[redacted]"},
	// URL-embedded credentials: scheme://user:password@host - the password never
	// matches the keyword rule above (nothing there looks like "secret"/"token"/
	// etc), so it needs its own rule. The username is left visible for context.
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@]+:)[^\s@]+(@)`), "$1[redacted]$2"},
	{regexp.MustCompile(`[A-Za-z0-9+/]{60,}={0,2}`), "[redacted-blob]"},
}

// Scrub redacts secret-shaped substrings from s and drops any line that carries a
// PEM private key header entirely. Runs both in CPI (before the text leaves the
// cluster) and again in the Slack relay module (defence in depth).
func Scrub(s string) string {
	if strings.Contains(s, "PRIVATE KEY-----") {
		var kept []string
		for _, l := range strings.Split(s, "\n") {
			if privateKeyLineRE.MatchString(l) {
				continue
			}
			kept = append(kept, l)
		}
		s = strings.Join(kept, "\n")
	}
	for _, r := range scrubRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// ---- assembling the per-child evidence payload ----

const (
	// maxLineExcerptRunes and maxChildExcerptRunes both got bumped (220->300,
	// 400->460) alongside the aws-sdk-go-v2 boilerplate strip above: a real DMS
	// specimen's diagnostic ("Error: stopping DMS Serverless Replication (arn:...):
	// InvalidResourceStateFault: ... cannot be stopped.") is still ~270 runes once
	// the boilerplate is gone, and a two-node child joining an upgrade + a destroy
	// failure runs ~425 runes total - both would still get truncated away under the
	// old caps. 20-matrix.yaml's jq slice on log_excerpt must stay in sync with
	// maxChildExcerptRunes (the "relay's existing per-child slice" this comment
	// refers to).
	maxLineExcerptRunes     = 300 // per extracted line, before the "; " join
	maxChildExcerptRunes    = 460 // per child, after the join - matches the relay's existing per-child slice
	defaultMaxNodesPerChild = 3
	defaultWorkers          = 4
	defaultPerObjectTimeout = 5 * time.Second
	defaultOverallBudget    = 45 * time.Second
)

// ChildEvidence is one child workflow's row in the CHILD_JSON array post-status
// builds. Field order/tags match the existing jq output (name, config, phase, msg,
// detail, log_excerpt) plus three new keys carrying the failing phase and links to
// where the failure lives:
//
//   - FailedPhase: the harness phase word (provision/upgrade/validate/teardown, or a
//     DisplayName-derived fallback) of the EARLIEST failed Pod node - the node
//     failedPodNodes now keeps first under retries.
//   - ArgoURL: the Argo UI deep link for the workflow, or "" when ARGO_UI_BASE is
//     unset - callers omit the link in that case rather than showing a broken one.
//   - LogURI: the s3://bucket/key of that same earliest node's archived log, or ""
//     when the node has no artifact.
type ChildEvidence struct {
	Name        string `json:"name"`
	Config      string `json:"config"`
	Phase       string `json:"phase"`
	Msg         string `json:"msg"`
	Detail      string `json:"detail"`
	LogExcerpt  string `json:"log_excerpt"`
	FailedPhase string `json:"failed_phase"`
	ArgoURL     string `json:"argo_url"`
	LogURI      string `json:"log_uri"`
}

// BuildOptions bounds Build's S3 fan-out. Zero values take the defaults below.
type BuildOptions struct {
	MaxNodesPerChild int
	Workers          int
	PerObjectTimeout time.Duration
	OverallBudget    time.Duration
}

func (o BuildOptions) withDefaults() BuildOptions {
	if o.MaxNodesPerChild <= 0 {
		o.MaxNodesPerChild = defaultMaxNodesPerChild
	}
	if o.Workers <= 0 {
		o.Workers = defaultWorkers
	}
	if o.PerObjectTimeout <= 0 {
		o.PerObjectTimeout = defaultPerObjectTimeout
	}
	if o.OverallBudget <= 0 {
		o.OverallBudget = defaultOverallBudget
	}
	return o
}

// argoWorkflowURL builds the Argo UI deep link for a workflow name from the
// ARGO_UI_BASE env var, or "" when that var is unset/empty - the orchestrator wires
// up the real hostname later; until then (and in any environment that never sets it)
// this deliberately omits the link rather than hardcoding a guess.
func argoWorkflowURL(name string) string {
	base := strings.TrimRight(os.Getenv("ARGO_UI_BASE"), "/")
	if base == "" {
		return ""
	}
	return base + "/workflows/argo/" + name
}

// s3URI formats a bucket/key pair as an s3:// URI, or "" if either is empty.
func s3URI(bucket, key string) string {
	if bucket == "" || key == "" {
		return ""
	}
	return "s3://" + bucket + "/" + key
}

// childBase builds a child's {name, config, phase, msg, detail} exactly as the jq
// expression it replaces did - detail is unchanged in meaning and in how it is
// computed, uncapped, so an un-bumped renderer sees identical text.
func childBase(wf Workflow) ChildEvidence {
	config := wf.Metadata.Labels["harness/config"]
	if config == "" {
		config = "-"
	}
	phase := wf.Status.Phase
	if phase == "" {
		phase = "-"
	}
	var parts []string
	for _, n := range failedPodNodes(wf, 0) {
		parts = append(parts, fmt.Sprintf("%s: %s", n.DisplayName, n.Message))
	}
	return ChildEvidence{
		Name:   wf.Metadata.Name,
		Config: config,
		Phase:  phase,
		Msg:    wf.Status.Message,
		Detail: strings.Join(parts, "; "),
	}
}

// Build assembles CHILD_JSON for every child in list, fetching each failed Pod
// node's archived log tail through fetcher and extracting the proximate error.
// Any fetch or extraction miss (timeout, missing artifact, empty log, no
// recognizable error line) leaves that child's LogExcerpt at "" - callers then fall
// back to Detail, which is exactly today's card text. This function can never make
// a child's evidence worse, only better.
func Build(ctx context.Context, list WorkflowList, fetcher LogTail, opts BuildOptions) []ChildEvidence {
	opts = opts.withDefaults()

	ctx, cancel := context.WithTimeout(ctx, opts.OverallBudget)
	defer cancel()

	children := make([]ChildEvidence, len(list.Items))
	perChildNodes := make([][]Node, len(list.Items))
	// phases and logURIs seed the failed_phase/log_uri fallback from data that
	// needs no S3 fetch (the DisplayName, and the bucket/key already on the
	// Workflow), so both fields stand even when the earliest node's log never
	// comes back. A worker overwrites phases[i] below if the fetched log carries
	// an actual verdict line.
	phases := make([]string, len(list.Items))
	logURIs := make([]string, len(list.Items))
	for i, wf := range list.Items {
		children[i] = childBase(wf)
		children[i].ArgoURL = argoWorkflowURL(wf.Metadata.Name)
		perChildNodes[i] = failedPodNodes(wf, opts.MaxNodesPerChild)
		if len(perChildNodes[i]) > 0 {
			earliest := perChildNodes[i][0]
			phases[i] = phaseFromDisplayName(earliest.DisplayName)
			bucket := wf.Status.ArtifactRepositoryRef.ArtifactRepository.S3.Bucket
			logURIs[i] = s3URI(bucket, artifactKey(earliest))
		}
	}

	type job struct {
		childIdx, nodeIdx    int
		bucket, key, display string
	}
	var jobs []job
	for i, wf := range list.Items {
		bucket := wf.Status.ArtifactRepositoryRef.ArtifactRepository.S3.Bucket
		for ni, n := range perChildNodes[i] {
			key := artifactKey(n)
			if bucket == "" || key == "" {
				continue
			}
			jobs = append(jobs, job{childIdx: i, nodeIdx: ni, bucket: bucket, key: key, display: n.DisplayName})
		}
	}

	// nodeResult keeps a fetched node's excerpt separate from its display name so
	// the final join can dedup on excerpt text alone - a retried step's nodes
	// carry different DisplayNames ("upgrade(0)" vs "upgrade(1)") for the
	// identical underlying error.
	type nodeResult struct {
		display, excerpt string
	}

	// results[childIdx][nodeIdx] holds a node's result, zero-value on a miss.
	// Each job owns a disjoint slot, so workers write without a lock, and the
	// final join walks nodeIdx order - deterministic regardless of which worker
	// finishes first. Node index 0 is always the earliest failed node
	// (failedPodNodes keeps the earliest under a cap), which is also the only
	// index any worker writes phases[childIdx] from.
	results := make([][]nodeResult, len(list.Items))
	for i := range results {
		results[i] = make([]nodeResult, len(perChildNodes[i]))
	}

	workers := opts.Workers
	if len(jobs) > 0 && workers > len(jobs) {
		workers = len(jobs)
	}
	if workers <= 0 {
		workers = 1
	}

	jobCh := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				octx, cancel := context.WithTimeout(ctx, opts.PerObjectTimeout)
				tail, err := fetcher.Tail(octx, j.bucket, j.key)
				cancel()
				if err != nil {
					continue
				}
				lines := normalizeAndWindow(tail)
				if j.nodeIdx == 0 {
					if phase := verdictPhase(lines); phase != "" {
						phases[j.childIdx] = phase
					}
				}
				errText := extractErrorFromLines(lines)
				if errText == "" {
					continue
				}
				excerpt := truncateRunes(Scrub(errText), maxLineExcerptRunes)
				results[j.childIdx][j.nodeIdx] = nodeResult{display: j.display, excerpt: excerpt}
			}
		}()
	}
	go func() {
		defer close(jobCh)
		for _, j := range jobs {
			select {
			case jobCh <- j:
			case <-ctx.Done():
				return
			}
		}
	}()
	wg.Wait()

	for i, parts := range results {
		var nonEmpty []string
		seen := make(map[string]bool, len(parts))
		for _, p := range parts {
			if p.excerpt == "" || seen[p.excerpt] {
				continue
			}
			seen[p.excerpt] = true
			nonEmpty = append(nonEmpty, p.display+": "+p.excerpt)
		}
		if len(nonEmpty) == 0 {
			continue
		}
		children[i].LogExcerpt = truncateRunes(strings.Join(nonEmpty, "; "), maxChildExcerptRunes)
	}

	for i := range children {
		children[i].FailedPhase = phases[i]
		children[i].LogURI = logURIs[i]
	}
	return children
}
