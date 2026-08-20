package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "evidence", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(b)
}

func TestExtractErrorPrefersTofuErrorOverThinVerdict(t *testing.T) {
	got := ExtractError(readTestdata(t, "bi-ha-destroy-tail.log"))
	for _, want := range []string{"InvalidResourceStateFault", "cannot be stopped"} {
		if !strings.Contains(got, want) {
			t.Errorf("ExtractError() = %q, want it to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"Still destroying", "context deadline exceeded"} {
		if strings.Contains(got, notWant) {
			t.Errorf("ExtractError() = %q, must not contain %q (superseded/noise)", got, notWant)
		}
	}
	if got == "teardown failed: destroy: exit status 1" {
		t.Errorf("ExtractError() returned the thin verdict verbatim; the tofu error should have won")
	}
}

func TestExtractErrorKeepsInformativeVerdict(t *testing.T) {
	got := ExtractError(readTestdata(t, "min-default-upgrade.log"))
	want := "upgrade failed: worktree add 8f347c78fb11039a9737c30e956fa16e5f57cd78 (8f347c78fb11039a9737c30e956fa16e5f57cd78): exit status 128"
	if got != want {
		t.Errorf("ExtractError() = %q, want %q", got, want)
	}
}

func TestExtractErrorReturnsEmptyOnNoise(t *testing.T) {
	got := ExtractError(readTestdata(t, "noise-only.log"))
	if got != "" {
		t.Errorf("ExtractError() = %q, want \"\" (falls back to node message)", got)
	}
}

func TestExtractErrorStripsAnsiAndTerragruntPrefix(t *testing.T) {
	got := ExtractError(readTestdata(t, "bi-ha-destroy-tail.log"))
	for _, bad := range []string{"\x1b[", "STDERR", "│"} {
		if strings.Contains(got, bad) {
			t.Errorf("ExtractError() = %q, must not contain raw decoration %q", got, bad)
		}
	}
	if strings.HasPrefix(got, "01:") {
		t.Errorf("ExtractError() = %q, still carries a timestamp prefix", got)
	}
}

func TestExtractErrorWindowIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("Error: the real error, but it is 50000 lines from the tail\n")
	for i := 0; i < 50000; i++ {
		b.WriteString("module.thing: Still destroying... [id=x, 1s elapsed]\n")
	}
	got := ExtractError(b.String())
	if got != "" {
		t.Errorf("ExtractError() = %q, want \"\" - the error is outside the 400-line window", got)
	}
}

func TestScrubRedactsSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"vault token", "VAULT_TOKEN=hvs.CAESIJf8vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3"},
		{"akid", "aws_access_key_id = AKIAIOSFODNN7EXAMPLE"},
		{"asid", "aws_access_key_id = ASIAIOSFODNN7EXAMPLE"},
		{"slack token", "SLACK_TOKEN=xoxb-1234567890-abcdefghij"},
		{"github token", "GH_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"},
		{"jwt", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQ_dQw4w9WgXcQdQw4w9WgXcQ"},
		{"password kv", `password="hunter2hunter"`},
		{"base64 blob", strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 2)},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIBogIBAAJ...\n-----END RSA PRIVATE KEY-----"},
		{"private key pkcs8", "-----BEGIN PRIVATE KEY-----\nMIIBogIBAAJ...\n-----END PRIVATE KEY-----"},
		{"embedded keyword env var", "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"url credentials", "mysql://root:SuperSecretPw1@host/db"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scrub(c.input)
			if got == c.input {
				t.Errorf("Scrub(%q) left input unchanged", c.input)
			}
		})
	}
}

func TestScrubRedactsEmbeddedKeywordAndURLCredentials(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		mustNotHave string
		mustHave    string
	}{
		{
			name:        "aws secret access key",
			input:       "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			mustNotHave: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			mustHave:    "AWS_SECRET_ACCESS_KEY=[redacted]",
		},
		{
			name:        "mysql url password",
			input:       "mysql://root:SuperSecretPw1@host/db",
			mustNotHave: "SuperSecretPw1",
			mustHave:    "mysql://root:[redacted]@host/db",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scrub(c.input)
			if strings.Contains(got, c.mustNotHave) {
				t.Errorf("Scrub(%q) = %q, must not contain the raw secret %q", c.input, got, c.mustNotHave)
			}
			if !strings.Contains(got, c.mustHave) {
				t.Errorf("Scrub(%q) = %q, want it to contain %q", c.input, got, c.mustHave)
			}
		})
	}
}

// TestScrubKeywordRuleDoesNotFlattenSpecificLabels guards against the widened
// keyword rule running over its own output: several specific labels above
// ("[redacted-vault-token]", "[redacted-slack-token]", "[redacted-github-token]")
// contain the word "token", which the generic keyword rule also matches on.
func TestScrubKeywordRuleDoesNotFlattenSpecificLabels(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"vault token", "VAULT_TOKEN=hvs.CAESIJf8vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3", "[redacted-vault-token]"},
		{"slack token", "SLACK_TOKEN=xoxb-1234567890-abcdefghij", "[redacted-slack-token]"},
		{"github token", "GH_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "[redacted-github-token]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scrub(c.input)
			if !strings.Contains(got, c.want) {
				t.Errorf("Scrub(%q) = %q, want the specific label %q preserved (not flattened to a bare [redacted])", c.input, got, c.want)
			}
		})
	}
}

func TestScrubKeepsDiagnosticSurvivors(t *testing.T) {
	survivors := []string{
		"8f347c78fb11039a9737c30e956fa16e5f57cd78", // 40-hex git SHA
		"arn:aws:dms:us-east-1:000000000000:replication-config:REPLICATIONCONFIGID",
		"dozuki-argo-artifacts-000000000000",
		"smokef9b3-bi-reader",
		// KMS diagnostics are the most common real failure shape; a bare "key"
		// alternation redacted all three of these.
		"kms_key_id = arn:aws:kms:us-east-1:000000000000:key/abc-def-1234",
		"Error: creating KMS Key: AccessDeniedException: User is not authorized",
		"keyspace: production",
	}
	for _, s := range survivors {
		t.Run(s, func(t *testing.T) {
			if got := Scrub(s); got != s {
				t.Errorf("Scrub(%q) = %q, want it unchanged", s, got)
			}
		})
	}
}

func loadWorkflowList(t *testing.T, name string) WorkflowList {
	t.Helper()
	var list WorkflowList
	if err := json.Unmarshal([]byte(readTestdata(t, name)), &list); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return list
}

// stubFetcher answers Tail from an in-memory map keyed by "bucket/key", or via a
// custom fn when set - so tests can simulate misses/timeouts without real S3.
type stubFetcher struct {
	logs      map[string]string
	fn        func(ctx context.Context, bucket, key string) (string, error)
	mu        sync.Mutex
	requested []string
}

func (s *stubFetcher) Tail(ctx context.Context, bucket, key string) (string, error) {
	s.mu.Lock()
	s.requested = append(s.requested, bucket+"/"+key)
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, bucket, key)
	}
	if s.logs != nil {
		if v, ok := s.logs[bucket+"/"+key]; ok {
			return v, nil
		}
	}
	return "", errors.New("stub: no log for " + bucket + "/" + key)
}

func TestBuildSelectsArtifactKeyPerFailedPodNode(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{logs: map[string]string{}}
	_ = Build(context.Background(), list, stub, BuildOptions{})

	stub.mu.Lock()
	got := append([]string(nil), stub.requested...)
	stub.mu.Unlock()
	sort.Strings(got)

	want := []string{
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log",
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log",
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("requested %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("requested[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestBuildCapsAtThreeNodesPerChild covers the DISPLAY cap: with 5 distinct failed
// nodes (different DisplayNames, so none dedup away), Build must fetch every
// surviving candidate - the cap no longer bounds the S3 fan-out, only the number of
// distinct bullets that make it into LogExcerpt - and keep the EARLIEST 3 by
// FinishedAt (a, b, c) in the shown output, not the latest (c, d, e). Capping the
// fetch itself first was the earlier bug: it let a distinct later diagnostic get
// dropped before it was ever read (see TestBuildDedupSurvivesRetriesAndReclaimsSlot).
func TestBuildCapsAtThreeNodesPerChild(t *testing.T) {
	mkNode := func(id, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: id, FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-many-abcde", Labels: map[string]string{"harness/config": "many"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"a": mkNode("a", "2026-08-04T00:00:01Z"),
				"b": mkNode("b", "2026-08-04T00:00:02Z"),
				"c": mkNode("c", "2026-08-04T00:00:03Z"),
				"d": mkNode("d", "2026-08-04T00:00:04Z"),
				"e": mkNode("e", "2026-08-04T00:00:05Z"),
				// A Retry node with the same displayName/phase must not be fetched.
				"retry-a": {ID: "retry-a", Type: "Retry", Phase: "Failed", DisplayName: "a"},
			},
		},
	}
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return "Error: boom (" + key + ")\n", nil
	}}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}

	stub.mu.Lock()
	got := append([]string(nil), stub.requested...)
	stub.mu.Unlock()
	if len(got) != 5 {
		t.Errorf("requested %d objects, want 5 (every surviving candidate is fetched, the cap only bounds what is displayed)", len(got))
	}
	for _, r := range got {
		if strings.Contains(r, "retry-a") {
			t.Errorf("Retry node was fetched: %v", got)
		}
	}
	excerpt := children[0].LogExcerpt
	for _, want := range []string{"a/main.log", "b/main.log", "c/main.log"} {
		if !strings.Contains(excerpt, want) {
			t.Errorf("LogExcerpt = %q, want it to include the earliest node %q", excerpt, want)
		}
	}
	for _, notWant := range []string{"d/main.log", "e/main.log"} {
		if strings.Contains(excerpt, notWant) {
			t.Errorf("LogExcerpt = %q, want the later nodes dropped in favor of the earliest", excerpt)
		}
	}
}

// TestBuildKeepsFirstFailureAcrossRetries covers a run where the last node to fail
// is not the one that explains the run: three attempts of the same retried step, the
// first genuinely different from the later two (which look identical to each
// other - the retry re-hit the same error). The earliest attempt's error must be the
// one that survives into LogExcerpt, and the identical text on attempts 2 and 3 must
// collapse into a single bullet rather than repeating.
func TestBuildKeepsFirstFailureAcrossRetries(t *testing.T) {
	mkNode := func(id, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: "upgrade(" + id + ")", FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-retry-abcde", Labels: map[string]string{"harness/config": "retry"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"0": mkNode("0", "2026-08-04T00:00:01Z"),
				"1": mkNode("1", "2026-08-04T00:00:02Z"),
				"2": mkNode("2", "2026-08-04T00:00:03Z"),
			},
		},
	}
	logs := map[string]string{
		"b/0/main.log": "Error: worktree add 8f347c78: exit status 128\n",
		"b/1/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/2/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	got := children[0].LogExcerpt
	if !strings.Contains(got, "worktree add 8f347c78") {
		t.Errorf("LogExcerpt = %q, want the first (earliest) attempt's error to survive", got)
	}
	if n := strings.Count(got, "context deadline exceeded"); n != 1 {
		t.Errorf("LogExcerpt = %q, want the identical retry excerpt deduped to 1 occurrence, got %d", got, n)
	}
}

func TestBuildFallsBackToNodeMessage(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return "", errors.New("s3: access denied")
	}}
	children := Build(context.Background(), list, stub, BuildOptions{})

	for _, c := range children {
		if c.LogExcerpt != "" {
			t.Errorf("child %q: LogExcerpt = %q, want \"\" (fetch failed)", c.Name, c.LogExcerpt)
		}
		if c.Config == "bi_ha" || c.Config == "min_default" {
			if !strings.Contains(c.Detail, "main: Error (exit code 1)") {
				t.Errorf("child %q: Detail = %q, want today's node-summary text", c.Name, c.Detail)
			}
		}
	}
}

func TestBuildRespectsWireBudget(t *testing.T) {
	bigErr := "Error: " + strings.Repeat("something failed badly here ", 300)
	var items []Workflow
	for i := 0; i < 20; i++ {
		nodes := map[string]Node{}
		for j := 0; j < 3; j++ {
			id := "n" + string(rune('a'+i)) + string(rune('0'+j))
			nodes[id] = Node{
				ID: id, Type: "Pod", Phase: "Failed", DisplayName: id,
				FinishedAt: "2026-08-04T00:00:00Z",
				Outputs:    NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id}}}},
			}
		}
		items = append(items, Workflow{
			Metadata: WorkflowMetadata{Name: "wf" + string(rune('a'+i)), Labels: map[string]string{"harness/config": "c"}},
			Status: WorkflowStatus{
				Phase:                 "Failed",
				ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
				Nodes:                 nodes,
			},
		})
	}
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return bigErr + "\n", nil
	}}
	children := Build(context.Background(), WorkflowList{Items: items}, stub, BuildOptions{})
	for _, c := range children {
		if n := utf8.RuneCountInString(c.LogExcerpt); n > maxChildExcerptRunes {
			t.Errorf("child %q: log_excerpt is %d runes, want <= %d", c.Name, n, maxChildExcerptRunes)
		}
		if !utf8.ValidString(c.LogExcerpt) {
			t.Errorf("child %q: log_excerpt is not valid UTF-8", c.Name)
		}
		if c.LogExcerpt != "" && !strings.HasSuffix(c.LogExcerpt, "…") {
			t.Errorf("child %q: log_excerpt = %q, want it truncated with an ellipsis", c.Name, c.LogExcerpt)
		}
	}
}

// TestBuildDMSDiagnosticSurvivesTruncation is the CRITICAL regression for the
// aws-sdk-go-v2 boilerplate strip: it asserts on Build's actual card-visible output
// (ChildEvidence.LogExcerpt), not just ExtractError's return value, since Build
// applies its own truncation on top of ExtractError and that is where the
// diagnostic was getting cut off before the fix.
func TestBuildDMSDiagnosticSurvivesTruncation(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), list, stub, BuildOptions{})

	var biHa *ChildEvidence
	for i := range children {
		if children[i].Config == "bi_ha" {
			biHa = &children[i]
		}
	}
	if biHa == nil {
		t.Fatal("no bi_ha child in Build() output")
	}
	for _, want := range []string{"InvalidResourceStateFault", "cannot be stopped"} {
		if !strings.Contains(biHa.LogExcerpt, want) {
			t.Errorf("bi_ha LogExcerpt = %q, want it to contain %q (the DMS diagnostic must survive both the per-line and per-child truncation)", biHa.LogExcerpt, want)
		}
	}
}

// TestBuildExtractsFailedPhaseFromVerdictLine covers the primary source for
// failed_phase: a "<phase> failed: ..." verdict line in the earliest failed node's
// log. workflows-list.json's bi_ha child has upgrade(0) (2026-08-04T01:06:53Z) as
// its earliest failed node, ahead of destroy(0) (01:30:24Z) - failed_phase must come
// from upgrade's own log, not destroy's, and log_uri must point at upgrade's key.
func TestBuildExtractsFailedPhaseFromVerdictLine(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), list, stub, BuildOptions{})

	var biHa *ChildEvidence
	for i := range children {
		if children[i].Config == "bi_ha" {
			biHa = &children[i]
		}
	}
	if biHa == nil {
		t.Fatal("no bi_ha child in Build() output")
	}
	if biHa.FailedPhase != "upgrade" {
		t.Errorf("bi_ha FailedPhase = %q, want %q (from upgrade(0)'s own verdict line, not destroy(0)'s)", biHa.FailedPhase, "upgrade")
	}
	wantURI := "s3://dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log"
	if biHa.LogURI != wantURI {
		t.Errorf("bi_ha LogURI = %q, want %q", biHa.LogURI, wantURI)
	}
}

// TestBuildFailedPhaseFallsBackToDisplayName covers the fallback: when the earliest
// node's log carries no "<phase> failed: ..." verdict line (fetch fails here, but a
// verdict-free log hits the same branch), failed_phase comes from the node's
// DisplayName instead. log_uri must still be populated from the known bucket/key
// even though the fetch itself failed.
func TestBuildFailedPhaseFallsBackToDisplayName(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return "", errors.New("s3: access denied")
	}}
	children := Build(context.Background(), list, stub, BuildOptions{})

	for _, c := range children {
		if c.Config != "bi_ha" && c.Config != "min_default" {
			continue
		}
		if c.FailedPhase != "upgrade" {
			t.Errorf("child %q: FailedPhase = %q, want %q (DisplayName fallback off %q)", c.Name, c.FailedPhase, "upgrade", "upgrade(0)")
		}
		if c.LogURI == "" {
			t.Errorf("child %q: LogURI is empty, want it populated from the workflow's known bucket/key even though the fetch failed", c.Name)
		}
	}
}

// TestBuildFallbackAppliesRetryMaskWhenAllCandidatesFiltered covers FIX 1: the
// all-candidates-filtered fallback in Build used to call the RAW failedPodNodes(wf)
// and take index 0 unconditionally, so neither the retry mask nor a workflow-phase
// guard applied. provision(0) fails first and has no artifact (filtered from
// evidence candidates either way), but its Retry node ultimately Succeeded - masked,
// and must not drive failed_phase even though it is earliest. upgrade(0)/upgrade(1)
// also have no artifact, under a Retry that itself Failed - unmasked, and the real
// failure the fallback must report.
func TestBuildFallbackAppliesRetryMaskWhenAllCandidatesFiltered(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-fallbackmask-abcde", Labels: map[string]string{"harness/config": "fallbackmask"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"retry-provision": {ID: "retry-provision", Type: "Retry", Phase: "Succeeded", DisplayName: "provision", Children: []string{"provision0"}},
				"provision0": {
					ID: "provision0", Type: "Pod", Phase: "Failed", DisplayName: "provision(0)", FinishedAt: "2026-08-04T00:00:01Z",
				},
				"retry-upgrade": {ID: "retry-upgrade", Type: "Retry", Phase: "Failed", DisplayName: "upgrade", Children: []string{"upgrade0", "upgrade1"}},
				"upgrade0": {
					ID: "upgrade0", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:02Z",
				},
				"upgrade1": {
					ID: "upgrade1", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(1)", FinishedAt: "2026-08-04T00:00:03Z",
				},
			},
		},
	}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, &stubFetcher{logs: map[string]string{}}, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]
	if c.FailedPhase != "upgrade" {
		t.Errorf("FailedPhase = %q, want %q (masked provision(0) must not drive the fallback even though it is earliest)", c.FailedPhase, "upgrade")
	}
}

// TestBuildFallbackLeavesPhaseEmptyWhenChildSucceeded covers the other half of FIX 1:
// the fallback must run only when the workflow's own phase is Failed/Error. The
// isolation here is deliberately on the phase guard alone - provision(0) carries NO
// Succeeded Retry ancestor (so the retry mask does not touch it) and NO main-logs
// artifact (so it still reaches the fallback), leaving the phase guard as the ONLY
// thing that can stop it from being selected. A retried-away node would also pass
// this test with the guard deleted, which is exactly what made the previous version
// of this test not isolate the regression.
func TestBuildFallbackLeavesPhaseEmptyWhenChildSucceeded(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-fallbacksucceeded-abcde", Labels: map[string]string{"harness/config": "fallbacksucceeded"}},
		Status: WorkflowStatus{
			Phase:                 "Succeeded",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				// No Retry ancestor at all, so retriedAwayPodIDs never masks this
				// node - unmasked. No main-logs artifact, so it is filtered out of
				// evidenceCandidateNodes and the fallback is reached. The child's
				// own phase is Succeeded, so only the phase guard can prevent
				// selection.
				"provision0": {
					ID: "provision0", Type: "Pod", Phase: "Failed", DisplayName: "provision(0)", FinishedAt: "2026-08-04T00:00:01Z",
				},
			},
		},
	}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, &stubFetcher{logs: map[string]string{}}, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]
	if c.FailedPhase != "" {
		t.Errorf("FailedPhase = %q, want empty (child ultimately Succeeded, an unmasked no-artifact failure must not name a phase)", c.FailedPhase)
	}
}

// TestPhaseFromDisplayNameMapsDestroyToTeardown covers the Argo-step-name-to-
// harness-phase-word mapping: the Argo step is "destroy" but the harness verdict
// phase for that step is "teardown", and an unrecognized step must map to "" rather
// than rendering an arbitrary word the card never promises.
func TestPhaseFromDisplayNameMapsDestroyToTeardown(t *testing.T) {
	cases := []struct {
		displayName, want string
	}{
		{"destroy(0)", "teardown"},
		{"teardown(0)", "teardown"},
		{"upgrade(0)", "upgrade"},
		{"provision(1)", "provision"},
		{"validate(0)", "validate"},
		{"gate", "gate"},
		{"somethingUnexpected(0)", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := phaseFromDisplayName(c.displayName); got != c.want {
			t.Errorf("phaseFromDisplayName(%q) = %q, want %q", c.displayName, got, c.want)
		}
	}
}

// TestBuildDedupKeysOnStepAndExcerpt covers the two halves of the dedup key: two
// DIFFERENT steps whose excerpts happen to render identically (e.g. after
// truncation) must both survive as distinct bullets, while repeated attempts of the
// SAME step with an identical excerpt must still collapse to one.
func TestBuildDedupKeysOnStepAndExcerpt(t *testing.T) {
	mkNode := func(id, displayName, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: displayName, FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-dedup-abcde", Labels: map[string]string{"harness/config": "dedup"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				// Two attempts of the SAME step, identical excerpt: must collapse.
				"0": mkNode("0", "upgrade(0)", "2026-08-04T00:00:01Z"),
				"1": mkNode("1", "upgrade(1)", "2026-08-04T00:00:02Z"),
				// A DIFFERENT step with the identical excerpt text: must survive.
				"2": mkNode("2", "validate(0)", "2026-08-04T00:00:03Z"),
			},
		},
	}
	logs := map[string]string{
		"b/0/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/1/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/2/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{MaxNodesPerChild: 0})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	got := children[0].LogExcerpt
	if n := strings.Count(got, "context deadline exceeded"); n != 2 {
		t.Errorf("LogExcerpt = %q, want 2 occurrences (upgrade retries collapsed, validate distinct), got %d", got, n)
	}
	if !strings.Contains(got, "validate(0)") {
		t.Errorf("LogExcerpt = %q, want the distinct validate(0) step to survive dedup", got)
	}
}

// TestFailedPodNodesSkipsEmptyFinishedAtForEarliest covers the ordering fix: a
// failed node with an EMPTY FinishedAt must not be treated as the earliest just
// because an empty string sorts before any real timestamp. It has to sort AFTER
// every node with a real timestamp.
func TestFailedPodNodesSkipsEmptyFinishedAtForEarliest(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-noFinishedAt-abcde", Labels: map[string]string{"harness/config": "noFinishedAt"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"missing": {ID: "missing", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: ""},
				"real":    {ID: "real", Type: "Pod", Phase: "Failed", DisplayName: "validate(0)", FinishedAt: "2026-08-04T00:00:01Z"},
			},
		},
	}
	nodes := failedPodNodes(wf)
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	if nodes[0].ID != "real" {
		t.Errorf("nodes[0].ID = %q, want %q (the node with a real FinishedAt, not the one with an empty FinishedAt)", nodes[0].ID, "real")
	}
}

// TestBuildArgoURLRespectsARGOUIBASE covers both ends of the ARGO_UI_BASE contract:
// unset means omit the link entirely, set means build the deep link off it.
func TestBuildArgoURLRespectsARGOUIBASE(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{logs: map[string]string{}}

	t.Run("unset", func(t *testing.T) {
		// Force ARGO_UI_BASE into a known state rather than relying on its
		// ambient absence: t.Setenv registers a value (and its restore on
		// cleanup), then os.Unsetenv removes it for this subtest's duration so
		// the "unset" case holds even if ARGO_UI_BASE leaks into the test
		// process env.
		t.Setenv("ARGO_UI_BASE", "https://leaked.example")
		os.Unsetenv("ARGO_UI_BASE")
		children := Build(context.Background(), list, stub, BuildOptions{})
		for _, c := range children {
			if c.ArgoURL != "" {
				t.Errorf("child %q: ArgoURL = %q, want \"\" when ARGO_UI_BASE is unset", c.Name, c.ArgoURL)
			}
		}
	})

	t.Run("set", func(t *testing.T) {
		t.Setenv("ARGO_UI_BASE", "https://argo.example.internal/")
		children := Build(context.Background(), list, stub, BuildOptions{})
		for _, c := range children {
			want := "https://argo.example.internal/workflows/argo/" + c.Name
			if c.ArgoURL != want {
				t.Errorf("child %q: ArgoURL = %q, want %q", c.Name, c.ArgoURL, want)
			}
		}
	})
}

func TestBuildOutputIsDeterministic(t *testing.T) {
	// children.golden.json bakes in argo_url:"" for every child, which only
	// holds with ARGO_UI_BASE unset - force that explicitly (see the "unset"
	// case of TestBuildArgoURLRespectsARGOUIBASE) rather than relying on its
	// ambient absence.
	t.Setenv("ARGO_UI_BASE", "https://leaked.example")
	os.Unsetenv("ARGO_UI_BASE")

	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
	}

	run := func() []byte {
		stub := &stubFetcher{logs: logs}
		children := Build(context.Background(), list, stub, BuildOptions{})
		b, err := json.MarshalIndent(children, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}

	first := run()
	for i := 0; i < 5; i++ {
		if got := run(); string(got) != string(first) {
			t.Fatalf("Build() is non-deterministic across runs:\n--- run 0 ---\n%s\n--- run %d ---\n%s", first, i+1, got)
		}
	}

	golden := readTestdata(t, "children.golden.json")
	var gotChildren, wantChildren []ChildEvidence
	if err := json.Unmarshal(first, &gotChildren); err != nil {
		t.Fatalf("unmarshal actual: %v", err)
	}
	if err := json.Unmarshal([]byte(golden), &wantChildren); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	gotJSON, _ := json.Marshal(gotChildren)
	wantJSON, _ := json.Marshal(wantChildren)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("Build() output does not match children.golden.json\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

// TestBuildSkipsPodNodeWhoseParentRetrySucceeded covers the retry-masking regression:
// the harness retryStrategy is OnError/limit 2 (live/tests/argo/00-phase-templates.yaml
// ~:105), so a Pod node can be Failed while the Retry node grouping its attempts
// ultimately Succeeded. Such a Pod's failure was retried away and must not drive
// failed_phase/log_uri or be fetched at all, even though it is the earliest failed
// Pod node in the workflow.
func TestBuildSkipsPodNodeWhoseParentRetrySucceeded(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-retrymask-abcde", Labels: map[string]string{"harness/config": "retrymask"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				// upgrade(0) failed first, but its Retry node ultimately succeeded
				// (a later attempt, not modeled here, must have won) - this node's
				// failure is masked and must be skipped entirely.
				"retry-upgrade": {ID: "retry-upgrade", Type: "Retry", Phase: "Succeeded", DisplayName: "upgrade", Children: []string{"upgrade0"}},
				"upgrade0": {
					ID: "upgrade0", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:01Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "upgrade0/main.log"}}}},
				},
				// validate(0) is the real, unmasked failure - its Retry node also
				// ultimately failed.
				"retry-validate": {ID: "retry-validate", Type: "Retry", Phase: "Failed", DisplayName: "validate", Children: []string{"validate0"}},
				"validate0": {
					ID: "validate0", Type: "Pod", Phase: "Failed", DisplayName: "validate(0)", FinishedAt: "2026-08-04T00:00:02Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "validate0/main.log"}}}},
				},
			},
		},
	}
	logs := map[string]string{
		"b/validate0/main.log": "Error: assertion failed: thing not ready\n",
		"b/upgrade0/main.log":  "Error: this must never be read: the node was retried away\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]

	stub.mu.Lock()
	requested := append([]string(nil), stub.requested...)
	stub.mu.Unlock()
	for _, r := range requested {
		if strings.Contains(r, "upgrade0") {
			t.Errorf("requested %v, want the retried-away upgrade0 node never fetched", requested)
		}
	}
	if c.FailedPhase != "validate" {
		t.Errorf("FailedPhase = %q, want %q (the masked upgrade(0) node must not drive it)", c.FailedPhase, "validate")
	}
	wantURI := "s3://b/validate0/main.log"
	if c.LogURI != wantURI {
		t.Errorf("LogURI = %q, want %q", c.LogURI, wantURI)
	}
	if !strings.Contains(c.LogExcerpt, "assertion failed") {
		t.Errorf("LogExcerpt = %q, want it to contain the real validate(0) diagnostic", c.LogExcerpt)
	}
	if strings.Contains(c.LogExcerpt, "retried away") {
		t.Errorf("LogExcerpt = %q, want it to NEVER contain the masked upgrade0 text", c.LogExcerpt)
	}
}

// TestBuildSkipsNoArtifactNodeInsteadOfWastingBudget covers the evicted/OOM-killed
// regression: such an attempt has no main-logs artifact at all, so under the old
// earliest-first cap (applied before filtering) three no-artifact nodes could fill
// the entire 3-node budget and push the one real, useful failure out of the
// candidate set altogether - even though it is later and would otherwise easily fit.
func TestBuildSkipsNoArtifactNodeInsteadOfWastingBudget(t *testing.T) {
	mkEvicted := func(id, finishedAt string) Node {
		return Node{ID: id, Type: "Pod", Phase: "Error", DisplayName: "provision(" + id + ")", FinishedAt: finishedAt}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-evicted-abcde", Labels: map[string]string{"harness/config": "evicted"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"e0": mkEvicted("0", "2026-08-04T00:00:01Z"),
				"e1": mkEvicted("1", "2026-08-04T00:00:02Z"),
				"e2": mkEvicted("2", "2026-08-04T00:00:03Z"),
				"up0": {
					ID: "up0", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:04Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "up0/main.log"}}}},
				},
			},
		},
	}
	stub := &stubFetcher{logs: map[string]string{"b/up0/main.log": "upgrade failed: connection refused\n"}}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]
	if c.FailedPhase != "upgrade" {
		t.Errorf("FailedPhase = %q, want %q (the 3 no-artifact nodes must not spend the budget)", c.FailedPhase, "upgrade")
	}
	wantURI := "s3://b/up0/main.log"
	if c.LogURI != wantURI {
		t.Errorf("LogURI = %q, want %q", c.LogURI, wantURI)
	}
	if !strings.Contains(c.LogExcerpt, "connection refused") {
		t.Errorf("LogExcerpt = %q, want it to contain the real upgrade(0) diagnostic instead of being empty", c.LogExcerpt)
	}
}

// TestBuildDedupSurvivesRetriesAndReclaimsSlot covers the second MAJOR: dedup used to
// run AFTER the 3-node fetch cap, so three identical retries of one step could fill
// the entire budget and a genuinely distinct diagnostic on a later, different step
// was never even fetched - the fix fetches every surviving candidate and dedups
// while filling the display budget, so the distinct diagnostic reclaims the slot the
// duplicate retries collapsed away.
func TestBuildDedupSurvivesRetriesAndReclaimsSlot(t *testing.T) {
	mkNode := func(id, displayName, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: displayName, FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-reclaim-abcde", Labels: map[string]string{"harness/config": "reclaim"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				// Three retries of the same step, identical excerpt - must collapse
				// to a single bullet, not consume all 3 budget slots.
				"0": mkNode("0", "upgrade(0)", "2026-08-04T00:00:01Z"),
				"1": mkNode("1", "upgrade(1)", "2026-08-04T00:00:02Z"),
				"2": mkNode("2", "upgrade(2)", "2026-08-04T00:00:03Z"),
				// A later, genuinely distinct failure - must survive and take the
				// slot the retries' dedup freed up.
				"3": mkNode("3", "validate(0)", "2026-08-04T00:00:04Z"),
			},
		},
	}
	logs := map[string]string{
		"b/0/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/1/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/2/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/3/main.log": "Error: stopping DMS Serverless Replication (arn:aws:dms:us-east-1:000000000000:replication-config:REPLICATIONCONFIGID): InvalidResourceStateFault: replication cannot be stopped.\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	got := children[0].LogExcerpt
	if !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("LogExcerpt = %q, want the retried step's excerpt to survive", got)
	}
	if !strings.Contains(got, "InvalidResourceStateFault") {
		t.Errorf("LogExcerpt = %q, want the distinct validate(0) diagnostic to survive - it must not be dropped just because it wasn't in the earliest 3 nodes", got)
	}
	if n := len(strings.Split(got, "; ")); n != 2 {
		t.Errorf("LogExcerpt = %q, want exactly 2 distinct bullets (3 retries collapsed to 1, plus the distinct failure), got %d", got, n)
	}
}

// TestBuildLogURIMatchesReportedExcerptNode covers the log_uri/excerpt
// correspondence MINOR: log_uri used to be pinned to the earliest candidate node
// regardless of whether that node's own fetch produced any usable text, so a card
// could link a log that does not contain the quoted excerpt at all. log_uri must
// refer to the same node the reported excerpt actually came from.
func TestBuildLogURIMatchesReportedExcerptNode(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-uricorr-abcde", Labels: map[string]string{"harness/config": "uricorr"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				// Earliest node's log carries no recognizable error line, so it
				// contributes nothing to LogExcerpt.
				"a": {
					ID: "a", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:01Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "a/main.log"}}}},
				},
				// Later node's log carries the real diagnostic that ends up in
				// LogExcerpt.
				"b": {
					ID: "b", Type: "Pod", Phase: "Failed", DisplayName: "validate(0)", FinishedAt: "2026-08-04T00:00:02Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "b/main.log"}}}},
				},
			},
		},
	}
	logs := map[string]string{
		"b/a/main.log": "just some polling noise, nothing here looks like an error\n",
		"b/b/main.log": "Error: real diagnostic text\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]
	if !strings.Contains(c.LogExcerpt, "real diagnostic text") {
		t.Fatalf("LogExcerpt = %q, want it to contain the real diagnostic", c.LogExcerpt)
	}
	wantURI := "s3://b/b/main.log"
	if c.LogURI != wantURI {
		t.Errorf("LogURI = %q, want %q (the node the reported excerpt actually came from, not the earliest candidate's)", c.LogURI, wantURI)
	}
}

// TestBuildSkipsPodNodeBehindTwoLevelsOfRetry covers the finding-1 regression: the
// teardown path is retried at TWO levels (live/tests/argo/10-scenario.yaml wraps the
// Step in its own retryStrategy around harness-phase's own OnError retryStrategy from
// 00-phase-templates.yaml), so a Pod's DIRECT parent Retry node can be Failed (the
// inner attempt genuinely errored) while an OUTER Retry node further up ultimately
// Succeeded on a later attempt. The old one-hop retriedAwayPodIDs only checked a Pod's
// direct parent, so this shape survived unmasked - the exact regression class ce712b5
// fixed for the direct-parent shape, still open here. The outer Retry's own Children
// are a Steps node ID (not the Pod ID), which is exactly what the one-hop check could
// never reach.
func TestBuildSkipsPodNodeBehindTwoLevelsOfRetry(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-tworetry-abcde", Labels: map[string]string{"harness/config": "tworetry"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				// Outer Retry (the 10-scenario.yaml step-level retryStrategy)
				// ultimately Succeeded on a later attempt not modeled here. Its
				// Children are a STEPS node ID, not a Pod ID.
				"outer-retry": {ID: "outer-retry", Type: "Retry", Phase: "Succeeded", DisplayName: "teardown-step", Children: []string{"attempt-steps"}},
				"attempt-steps": {
					ID: "attempt-steps", Type: "Steps", Phase: "Failed", DisplayName: "teardown-step(0)",
					Children: []string{"inner-retry"},
				},
				// Inner Retry (harness-phase's own OnError retryStrategy) never
				// recovered on THIS outer attempt - it stays Failed even though
				// the outer attempt as a whole was superseded by a later,
				// successful outer attempt.
				"inner-retry": {ID: "inner-retry", Type: "Retry", Phase: "Failed", DisplayName: "teardown", Children: []string{"teardown0"}},
				"teardown0": {
					ID: "teardown0", Type: "Pod", Phase: "Failed", DisplayName: "teardown(0)", FinishedAt: "2026-08-04T00:00:01Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "teardown0/main.log"}}}},
				},
				// validate(0) is the real, unmasked failure.
				"retry-validate": {ID: "retry-validate", Type: "Retry", Phase: "Failed", DisplayName: "validate", Children: []string{"validate0"}},
				"validate0": {
					ID: "validate0", Type: "Pod", Phase: "Failed", DisplayName: "validate(0)", FinishedAt: "2026-08-04T00:00:02Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "validate0/main.log"}}}},
				},
			},
		},
	}
	logs := map[string]string{
		"b/teardown0/main.log": "Error: this must never be read: retried away two levels up\n",
		"b/validate0/main.log": "Error: assertion failed: thing not ready\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]

	stub.mu.Lock()
	requested := append([]string(nil), stub.requested...)
	stub.mu.Unlock()
	for _, r := range requested {
		if strings.Contains(r, "teardown0") {
			t.Errorf("requested %v, want the twice-retried-away teardown0 node never fetched", requested)
		}
	}
	if c.FailedPhase != "validate" {
		t.Errorf("FailedPhase = %q, want %q (the masked teardown(0) node must not drive it)", c.FailedPhase, "validate")
	}
	wantURI := "s3://b/validate0/main.log"
	if c.LogURI != wantURI {
		t.Errorf("LogURI = %q, want %q", c.LogURI, wantURI)
	}
	if strings.Contains(c.LogExcerpt, "retried away") || strings.Contains(c.Detail, "retried away") {
		t.Errorf("LogExcerpt/Detail must never contain the masked teardown0 text; LogExcerpt=%q Detail=%q", c.LogExcerpt, c.Detail)
	}
}

// TestRetriedAwayPodIDsDoesNotMaskUnresolvedRetryAncestors covers the dangerous
// over-masking direction: a Pod behind a Retry ancestor that is still Running, or
// that ultimately Failed/Errored (never recovered by any later attempt), must NOT be
// masked - only a Succeeded Retry ancestor mutes a Pod's failure.
func TestRetriedAwayPodIDsDoesNotMaskUnresolvedRetryAncestors(t *testing.T) {
	cases := []struct {
		name       string
		retryPhase string
	}{
		{"running ancestor", "Running"},
		{"failed ancestor", "Failed"},
		{"errored ancestor", "Error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nodes := map[string]Node{
				"retry": {ID: "retry", Type: "Retry", Phase: c.retryPhase, DisplayName: "upgrade", Children: []string{"pod"}},
				"pod":   {ID: "pod", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)"},
			}
			masked := retriedAwayPodIDs(nodes)
			if masked["pod"] {
				t.Errorf("retriedAwayPodIDs masked %q behind a %s Retry ancestor, want it unmasked", "pod", c.retryPhase)
			}
		})
	}
}

// TestBuildInterleavesFetchByNodeIndexAcrossChildren covers the finding-2 shared-
// budget-starvation regression: jobs used to dispatch child-major (every candidate of
// child 0, then every candidate of child 1, ...), so a child with many candidates
// could exhaust the single 45s OverallBudget before a LATER child's node 0 was ever
// dispatched, starving that child's evidence entirely. The fix interleaves dispatch by
// nodeIdx (every child's node 0 first, then node 1, ...) so budget exhaustion drops
// the LATEST candidates across all children, never a whole child.
//
// To make this deterministic under a real clock: a single worker processes jobs
// strictly in dispatch order, each fetch takes a fixed 20ms, and the overall budget is
// set to 45ms. The early child carries 10 candidates (so child-major dispatch would
// need >10 fetches, ~200ms, before ever reaching the late child's only node - long
// past the 45ms budget), while nodeIdx-major dispatch puts the late child's node 0
// second in line, well inside the budget.
func TestBuildInterleavesFetchByNodeIndexAcrossChildren(t *testing.T) {
	mkNode := func(id, displayName, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: displayName, FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	earlyNodes := map[string]Node{}
	for i := 0; i < 10; i++ {
		id := "early" + string(rune('a'+i))
		earlyNodes[id] = mkNode(id, "upgrade("+string(rune('a'+i))+")", "2026-08-04T00:00:0"+string(rune('1'+i%8))+"Z")
	}
	early := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-early-abcde", Labels: map[string]string{"harness/config": "early"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes:                 earlyNodes,
		},
	}
	late := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-late-abcde", Labels: map[string]string{"harness/config": "late"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"late0": mkNode("late0", "upgrade(0)", "2026-08-04T00:00:01Z"),
			},
		},
	}
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return "Error: boom (" + key + ")\n", nil
	}}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{early, late}}, stub, BuildOptions{
		Workers:       1,
		OverallBudget: 45 * time.Millisecond,
	})
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	lateChild := children[1]
	if lateChild.LogExcerpt == "" {
		t.Errorf("late child LogExcerpt is empty, want the late child's node 0 to have been fetched within the shared budget (dispatch must be nodeIdx-major, not child-major)")
	}
}

// TestBuildFailedPhaseMatchesLogURINodeWhenNodeZeroFetchFails covers the finding-3
// inconsistency: failed_phase used to stay pinned to node 0's DisplayName fallback
// even after log_uri moved on to a later node whose fetch actually produced the
// reported excerpt - so the card could name one phase while linking a different
// step's log. Node 0 here (upgrade(0)) always errors on fetch; node 1 (validate(0))
// succeeds and supplies the only bullet. Both failed_phase and log_uri must come from
// validate(0).
func TestBuildFailedPhaseMatchesLogURINodeWhenNodeZeroFetchFails(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-phasematch-abcde", Labels: map[string]string{"harness/config": "phasematch"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"a": {
					ID: "a", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:01Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "a/main.log"}}}},
				},
				"b": {
					ID: "b", Type: "Pod", Phase: "Failed", DisplayName: "validate(0)", FinishedAt: "2026-08-04T00:00:02Z",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "b/main.log"}}}},
				},
			},
		},
	}
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		if strings.Contains(key, "a/main.log") {
			return "", errors.New("s3: timeout")
		}
		return "Error: real diagnostic text\n", nil
	}}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	c := children[0]
	if !strings.Contains(c.LogExcerpt, "real diagnostic text") {
		t.Fatalf("LogExcerpt = %q, want it to contain the real diagnostic", c.LogExcerpt)
	}
	wantURI := "s3://b/b/main.log"
	if c.LogURI != wantURI {
		t.Errorf("LogURI = %q, want %q", c.LogURI, wantURI)
	}
	if c.FailedPhase != "validate" {
		t.Errorf("FailedPhase = %q, want %q (must come from the SAME node (validate(0)) log_uri points at, not node 0's fallback)", c.FailedPhase, "validate")
	}
}

// TestChildBaseDetailDoesNotLeadWithRetriedAwayNode covers the finding-4 asymmetry:
// Detail (the fallback text the renderer falls back to when log_excerpt is empty) used
// to build from the UNFILTERED failedPodNodes list, so it could headline a Pod node
// that evidenceCandidateNodes correctly excluded as retried-away, while failed_phase
// named the real failing step. Detail must apply the same retry mask.
func TestChildBaseDetailDoesNotLeadWithRetriedAwayNode(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-detailmask-abcde", Labels: map[string]string{"harness/config": "detailmask"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"retry-upgrade": {ID: "retry-upgrade", Type: "Retry", Phase: "Succeeded", DisplayName: "upgrade", Children: []string{"upgrade0"}},
				"upgrade0": {
					ID: "upgrade0", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:01Z",
					Message: "main: Error (exit code 1)",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "upgrade0/main.log"}}}},
				},
				"retry-validate": {ID: "retry-validate", Type: "Retry", Phase: "Failed", DisplayName: "validate", Children: []string{"validate0"}},
				"validate0": {
					ID: "validate0", Type: "Pod", Phase: "Failed", DisplayName: "validate(0)", FinishedAt: "2026-08-04T00:00:02Z",
					Message: "main: Error (exit code 1)",
					Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: "validate0/main.log"}}}},
				},
			},
		},
	}
	_, masked := evidenceCandidateNodes(wf)
	c := childBase(wf, masked)
	if strings.Contains(c.Detail, "upgrade(0)") {
		t.Errorf("Detail = %q, want it to NOT lead with the retried-away upgrade(0) node", c.Detail)
	}
	if !strings.Contains(c.Detail, "validate(0)") {
		t.Errorf("Detail = %q, want it to contain the real validate(0) failure", c.Detail)
	}
}

// TestChildBaseDetailKeepsNoArtifactNode covers FIX 2: childBase's Detail must apply
// ONLY the retry mask, not evidenceCandidateNodes' full filtered node list (which also
// drops nodes with no main-logs artifact to bound the S3 fetch budget - a cost Detail
// never spends). An evicted/OOM-killed Pod node has no artifact at all, so under the
// old (post-refactor) behavior Detail went empty for exactly this case, where master
// used to render e.g. "upgrade(0): Pod was evicted".
func TestChildBaseDetailKeepsNoArtifactNode(t *testing.T) {
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-evictdetail-abcde", Labels: map[string]string{"harness/config": "evictdetail"}},
		Status: WorkflowStatus{
			Phase: "Failed",
			Nodes: map[string]Node{
				// No main-logs artifact at all - an evicted/OOM-killed attempt.
				"upgrade0": {
					ID: "upgrade0", Type: "Pod", Phase: "Failed", DisplayName: "upgrade(0)", FinishedAt: "2026-08-04T00:00:01Z",
					Message: "Pod was evicted",
				},
			},
		},
	}
	_, masked := evidenceCandidateNodes(wf)
	c := childBase(wf, masked)
	if !strings.Contains(c.Detail, "Pod was evicted") {
		t.Errorf("Detail = %q, want it to carry the evicted node's Message even though it has no artifact", c.Detail)
	}
}
