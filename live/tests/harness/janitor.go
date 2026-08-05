package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// The Phase 4 janitor: the backstop for a teardown that failed for a reason no retry
// fixes (a lock held by a dead process, expired credentials, a half-provisioned upgrade).
// Over three days seven stacks leaked that way, the account hit its 10-VPC ceiling, and
// every run after that died at provision with VpcLimitExceeded, nightly included.
//
// Detection is here, in the harness package, rather than in a shell script, because the
// only safe way to identify a run's resources is to recompute the identity the apply used
// (Config.Salted, config.go) instead of matching a name prefix. A "smoke*" filter is how a
// sweeper eventually eats something it should not: the base customers are smoke/smokesrc/
// smokerec today and any new matrix config is free to pick another string, so a prefix
// filter silently stops covering a config the day someone adds one.
//
// The destroy action is PhaseParams.Teardown, unchanged. It is already idempotent and it
// already destroys against the manifest's AppliedRef, which matters for cross-architecture
// upgrades (target code cannot destroy baseline state). A second destroy implementation
// would be a second thing to keep correct.

// CandidateState is what the janitor concluded about one state prefix. Only Orphan is ever
// acted on; everything else exists so the report says WHY nothing happened, which is the
// difference between a sweeper you can trust and one you stop reading.
type CandidateState string

const (
	StateActive      CandidateState = "active"       // a live workflow or a fresh lock owns it
	StatePending     CandidateState = "pending"      // past delete_after, still inside the grace window
	StateClean       CandidateState = "clean"        // stale and unowned, but no tagged resources are left
	StateOrphan      CandidateState = "orphan"       // stale, unowned, tagged resources still live: a teardown FAILED
	StateBlocked     CandidateState = "blocked"      // orphan, but a stale lock is in the way; needs a human
	StateKept        CandidateState = "kept"         // orphan-shaped, but --keep-on-failure means a human left it up on purpose
	StateNeedsReview CandidateState = "needs-review" // we could not establish the facts; never acted on
	StateUnknown     CandidateState = "unknown"      // an AWS call was inconclusive; fail closed
)

// Candidate is one state prefix and everything the janitor learned about it.
type Candidate struct {
	Prefix        string         `json:"prefix"`
	Bucket        string         `json:"bucket"`
	RunID         string         `json:"run_id"`
	ConfigName    string         `json:"config_name"`
	Identifier    string         `json:"identifier"` // <salted customer>-<env>, the EKS cluster name
	DeleteAfter   string         `json:"delete_after"`
	State         CandidateState `json:"state"`
	Reason        string         `json:"reason"`
	Resources     int            `json:"resources"`      // tag-confirmed live resources
	WorkflowPhase string         `json:"workflow_phase"` // empty when no owning workflow object survives
	KeepOnFailure bool           `json:"keep_on_failure"`
	LockAge       string         `json:"lock_age,omitempty"`
	SweepResult   string         `json:"sweep_result,omitempty"`
}

// Report is the whole cycle, emitted as JSON for the Slack step and printed as a table.
type Report struct {
	Mode       string      `json:"mode"` // "report" | "sweep"
	At         string      `json:"at"`
	Account    string      `json:"account"`
	Candidates []Candidate `json:"candidates"`
	Orphans    int         `json:"orphans"`
	Swept      int         `json:"swept"`
	Failed     int         `json:"failed"`
}

// protectedStatePrefixes and protectedIdentifiers are the never-touch list. They are a
// GATE, not a filter: a hit aborts the entire cycle rather than skipping one candidate,
// because a protected identity reaching this point means the detection logic is wrong, and
// wrong logic gives no reason to trust the next candidate either.
//
// dev-min is already excluded structurally (its state is at the unprefixed top level, so
// it has no harness-manifest.json and never becomes a candidate). This list is the second
// wall behind that, for the day someone changes how prefixes are built.
var protectedStatePrefixes = []string{"standard/", "_templates/", "_global/"}
var protectedIdentifiers = []string{"dev-min", "dozuki-min", "min-min"}

// ErrProtected aborts the cycle. Callers must not continue past it.
var ErrProtected = fmt.Errorf("janitor: protected identity reached the candidate path")

func guardProtected(prefix, identifier string) error {
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("%w: refusing a prefix that is empty or not a directory: %q", ErrProtected, prefix)
	}
	for _, p := range protectedStatePrefixes {
		if strings.HasPrefix(prefix, p) {
			return fmt.Errorf("%w: prefix %q is protected", ErrProtected, prefix)
		}
	}
	for _, id := range protectedIdentifiers {
		if identifier == id {
			return fmt.Errorf("%w: identifier %q is protected (prefix %q)", ErrProtected, identifier, prefix)
		}
	}
	return nil
}

// JanitorOptions are the knobs. Every default here is the conservative end.
type JanitorOptions struct {
	AccountID    string
	Region       string
	DRRegion     string
	Profile      string
	RepoDir      string
	LockTable    string        // dozuki-terraform-lock (live/root.hcl)
	Grace        time.Duration // extra staleness on top of the manifest's delete_after
	LockFresh    time.Duration // a lock younger than this means something is applying NOW
	SelfWorkflow string        // this janitor workflow's own name; proves the stdin list is real
	Sweep        bool          // false = dry run. The default everywhere.
	MaxSweeps    int           // cap on SUCCESSFUL destroys in one cycle
	// MaxSweepFailures caps FAILED destroy attempts in one cycle, independent of
	// MaxSweeps (defect 3). <= 0 falls back to a default of 2 in Sweep, not to
	// "unbounded" - see the comment on that fallback for why zero is never treated as
	// "no cap".
	MaxSweepFailures int
	Now              func() time.Time // injected for tests; production is time.Now
}

func (o JanitorOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// JanitorDeps are the AWS/Argo surfaces, injected so the predicate is unit-testable
// without AWS or a cluster.
type JanitorDeps struct {
	S3     S3ListGetAPI
	Tags   map[string]TagAPI // keyed by region
	Locks  LockAPI
	Matrix *Matrix
	// Teardown performs the actual destroy. Production wires it to PhaseParams.Teardown;
	// tests substitute a fake so Sweep's SKIP/cap logic is exercised without a real repo
	// clone or terragrunt binary.
	Teardown func(ctx context.Context, p PhaseParams, failed bool) error
}

// S3ListGetAPI extends the manifest store's S3 surface with the listing call the janitor
// needs to enumerate run prefixes.
type S3ListGetAPI interface {
	S3API
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// TagAPI is the minimal Resource Groups Tagging API surface, one client per region.
type TagAPI interface {
	GetResources(context.Context, *resourcegroupstaggingapi.GetResourcesInput, ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

// LockAPI is the minimal DynamoDB surface the lock check needs. Read-only: the janitor
// never writes to the lock table.
type LockAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
}

// JanitorWorkflow decodes only what the ownership check needs. evidence.go's Workflow
// covers metadata+status but not spec, and run-id is a PARAMETER: a direct
// `argo submit --from workflowtemplate/harness-scenario -p run-id=...` can set it to
// something that is not the workflow name, so matching on the name alone would read that
// run as unowned while it is mid-apply.
type JanitorWorkflow struct {
	Metadata WorkflowMetadata `json:"metadata"`
	Spec     struct {
		Arguments struct {
			Parameters []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"parameters"`
		} `json:"arguments"`
	} `json:"spec"`
	Status WorkflowStatus `json:"status"`
}

type JanitorWorkflowList struct {
	Items []JanitorWorkflow `json:"items"`
}

func (w JanitorWorkflow) param(name string) string {
	for _, p := range w.Spec.Arguments.Parameters {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// runningOwners indexes the run ids that a live workflow claims. Both keys matter: the
// workflow name (the default run-id) and an explicitly passed run-id.
func runningOwners(list JanitorWorkflowList) map[string]string {
	owners := map[string]string{}
	for _, w := range list.Items {
		ph := w.Status.Phase
		if ph != "Running" && ph != "Pending" && ph != "" {
			continue
		}
		// An empty phase is a workflow the controller has not stamped yet. Treat it as
		// live: a workflow object that exists but has no verdict is the single most
		// dangerous thing to read as "finished".
		owners[w.Metadata.Name] = ph
		if rid := w.param("run-id"); rid != "" {
			owners[rid] = ph
		}
	}
	return owners
}

// byNameOrRunID indexes every workflow regardless of phase, keyed both by its own name
// and by its run-id parameter, so the report can annotate a candidate with the workflow's
// final phase and its keep-on-failure setting. Best-effort only: children TTL out after
// three days (10-scenario.yaml ttlStrategy), so an older orphan has no workflow object
// left - and once that happens keep-on-failure can no longer be recovered, so an old
// enough kept stack falls back to being judged purely on tags/lock like any other run.
// WorkflowPhase stays annotation only. KeepOnFailure is the one field off this index
// classify() actually acts on: see G8's StateKept branch below.
func byNameOrRunID(list JanitorWorkflowList) map[string]JanitorWorkflow {
	idx := map[string]JanitorWorkflow{}
	for _, w := range list.Items {
		idx[w.Metadata.Name] = w
		if rid := w.param("run-id"); rid != "" {
			idx[rid] = w
		}
	}
	return idx
}

// Scan is the whole read-only half: enumerate, classify, return. It performs NO mutation
// and is what runs in report mode. Sweep calls it first and then acts on StateOrphan only.
func Scan(ctx context.Context, d JanitorDeps, o JanitorOptions, wfList JanitorWorkflowList) (*Report, error) {
	now := o.now().UTC()
	owners := runningOwners(wfList)
	byWF := byNameOrRunID(wfList)

	rep := &Report{Mode: "report", At: now.Format(time.RFC3339), Account: o.AccountID}
	if o.Sweep {
		rep.Mode = "sweep"
	}

	// Both buckets. A run's manifest is not always in the bucket holding its state: the
	// store is built once from the matrix default region while each unit's state follows
	// the region it deploys to, so the recovery rebuild keeps its manifest in the primary
	// bucket and its state in the DR one (phases.go stateBucket).
	buckets := []string{stateBucket(o.AccountID, o.Region)}
	if o.DRRegion != "" && o.DRRegion != o.Region {
		buckets = append(buckets, stateBucket(o.AccountID, o.DRRegion))
	}

	seen := map[string]bool{}
	for _, bucket := range buckets {
		prefixes, err := listTopPrefixes(ctx, d.S3, bucket)
		if err != nil {
			// Fail the CYCLE, not the candidate. A partial listing under-reports, and an
			// under-report is invisible to a human reading a dry run: a missing line looks
			// exactly like a missing leak.
			return nil, fmt.Errorf("list prefixes in %s: %w", bucket, err)
		}
		for _, p := range prefixes {
			if seen[p] {
				continue
			}
			seen[p] = true
			c, err := classify(ctx, d, o, bucket, p, now, owners, byWF)
			if err != nil {
				return nil, err // only ErrProtected and cycle-fatal errors come back here
			}
			if c != nil {
				rep.Candidates = append(rep.Candidates, *c)
			}
		}
	}
	sort.Slice(rep.Candidates, func(i, j int) bool { return rep.Candidates[i].Prefix < rep.Candidates[j].Prefix })
	for _, c := range rep.Candidates {
		if c.State == StateOrphan {
			rep.Orphans++
		}
	}
	return rep, nil
}

// listTopPrefixes returns the top-level "directories" in bucket - the run prefixes plus
// whatever else lives at the top level (standard/, _templates/, ...). A delimited listing,
// not a recursive one: nothing below the first "/" is ever inspected here.
func listTopPrefixes(ctx context.Context, api S3ListGetAPI, bucket string) ([]string, error) {
	var prefixes []string
	var token *string
	for {
		out, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Delimiter:         aws.String("/"),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, cp := range out.CommonPrefixes {
			if cp.Prefix != nil {
				prefixes = append(prefixes, *cp.Prefix)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}
	return prefixes, nil
}

// classify applies the predicate to one prefix. Returns (nil, nil) for a prefix that is
// not a harness run at all. Returns a non-nil error ONLY for conditions that must stop the
// whole cycle (a protected identity, or a listing failure surfaced by the caller).
func classify(
	ctx context.Context, d JanitorDeps, o JanitorOptions,
	bucket, prefix string, now time.Time,
	owners map[string]string, byWF map[string]JanitorWorkflow,
) (*Candidate, error) {
	// G4 structural candidacy: only S3Store.Save writes this object name, and only ever
	// under a per-run prefix. Nothing else in the account produces one. dev-min's state
	// sits at the unprefixed top level (standard/us-east-1/min/...), so its common prefix
	// is "standard/", which has no manifest - it lands here as (nil, nil) and never goes
	// further.
	store := NewS3Store(d.S3, bucket)
	m, ok, err := store.Load(ctx, prefix)
	if err != nil {
		return &Candidate{Prefix: prefix, Bucket: bucket, State: StateNeedsReview,
			Reason: "manifest unreadable: " + err.Error()}, nil
	}
	if !ok {
		return nil, nil // not a harness run
	}

	cfg, cerr := d.Matrix.Config(m.ConfigName)
	if cerr != nil {
		return &Candidate{Prefix: prefix, Bucket: bucket, ConfigName: m.ConfigName, State: StateNeedsReview,
			Reason: "config " + m.ConfigName + " not found in matrix: " + cerr.Error()}, nil
	}

	// Reverse phases.go statePrefix: RunID + "-" + cfg.Name + "/". If the prefix does not
	// have that shape for THIS manifest's config, something wrote a manifest somewhere
	// unexpected and we stop reasoning about it rather than guess at the run id.
	suffix := "-" + m.ConfigName + "/"
	if !strings.HasSuffix(prefix, suffix) {
		return &Candidate{Prefix: prefix, Bucket: bucket, ConfigName: m.ConfigName, State: StateNeedsReview,
			Reason: "prefix does not match RunID-ConfigName/ for config " + m.ConfigName}, nil
	}
	runID := strings.TrimSuffix(prefix, suffix)

	salted := cfg.Salted(runID)
	customer, _ := salted.FeatureFlags["customer"].(string)
	identifier := ""
	if customer != "" {
		identifier = customer + "-" + cfg.Env
	}

	// G3, first call site. Abort the whole cycle, do not skip this one candidate.
	if perr := guardProtected(prefix, identifier); perr != nil {
		return nil, perr
	}

	base := &Candidate{
		Prefix: prefix, Bucket: bucket, RunID: runID, ConfigName: m.ConfigName,
		Identifier: identifier, DeleteAfter: m.DeleteAfter,
	}
	if wf, ok := byWF[runID]; ok {
		base.WorkflowPhase = wf.Status.Phase
		base.KeepOnFailure = wf.param("keep-on-failure") == "true"
	}

	// A salt that did not change anything (customer already at the 10-char terraform cap,
	// or the config carries no "customer" flag at all) means the identifier is not
	// run-unique and could collide with a long-lived stack. Refuse to reason about it
	// rather than work out whether this particular one is safe.
	unsaltedCustomer, _ := cfg.FeatureFlags["customer"].(string)
	if customer == "" || unsaltedCustomer == customer {
		base.State = StateNeedsReview
		base.Reason = "salted customer did not change from the base config value; identifier may not be run-unique"
		return base, nil
	}

	// G11: the recovery rebuild config stands its stack up in the DR region and is not
	// wired through the Argo phases at all yet (README "Not covered"). Getting the region
	// wiring wrong on a destroy is exactly the guess to avoid, so report and never sweep.
	if cfg.RegionOr(d.Matrix.Defaults.Region) != o.Region {
		base.State = StateNeedsReview
		base.Reason = fmt.Sprintf("config region %q does not match the janitor's --region %q", cfg.RegionOr(d.Matrix.Defaults.Region), o.Region)
		return base, nil
	}

	// G6: ownership beats age. A live workflow means hands off regardless of the clock.
	if ph, live := owners[runID]; live {
		base.State = StateActive
		base.WorkflowPhase = ph
		base.Reason = "owned by a live workflow (phase " + ph + ")"
		return base, nil
	}

	// G5: staleness, from the manifest's own declared TTL, never S3 LastModified.
	// Validate can poll BI health for up to 15 minutes without re-saving the manifest, so
	// write recency would read a healthy run as abandoned.
	if m.DeleteAfter == "" {
		base.State = StateNeedsReview
		base.Reason = "manifest has no delete_after"
		return base, nil
	}
	da, perr := time.Parse(time.RFC3339, m.DeleteAfter)
	if perr != nil {
		base.State = StateNeedsReview
		base.Reason = "delete_after is unparseable: " + perr.Error()
		return base, nil
	}
	if now.Before(da) {
		base.State = StateActive
		base.Reason = "inside its own declared lifetime (delete_after " + m.DeleteAfter + ")"
		return base, nil
	}
	if now.Before(da.Add(o.Grace)) {
		base.State = StatePending
		base.Reason = "past delete_after, inside the grace window"
		return base, nil
	}

	// G7: the lock. Its own timestamp, per exact LockID, never a table scan and never a
	// delete. cleanup-orphans.sh deletes every item whose LockID merely CONTAINS the
	// prefix, ungated by DRY_RUN: that breaks the state locking of whatever live run
	// happens to match. The janitor reads and reports, nothing more.
	fresh, age, lerr := lockState(ctx, d.Locks, d.S3, o.LockTable, bucket, prefix, now, o.LockFresh)
	if lerr != nil {
		base.State = StateUnknown
		base.Reason = "lock check inconclusive: " + lerr.Error()
		return base, nil
	}
	if fresh {
		base.State = StateActive
		base.Reason = "a state lock under this prefix is fresh; something is applying right now"
		return base, nil
	}

	// G8: live resources, confirmed by TAG VALUES recomputed from code. Customer is the
	// salted identity this run applied with; deleteAfter is present only when the harness
	// generated env.hcl (root.hcl only emits the tag when delete_after is non-empty, and
	// only withDeleteAfter ever sets it). A hand-authored stack cannot match either filter.
	n, anchored, terr := countTagged(ctx, d.Tags, []string{m.Region, m.DRRegion}, customer)
	if terr != nil {
		base.State = StateUnknown
		base.Reason = "tag lookup inconclusive: " + terr.Error()
		return base, nil
	}
	base.Resources = n
	if n == 0 {
		// Normal, not a leak: Teardown never deletes the manifest or the state prefix, so
		// every successfully torn-down run in history leaves this exact shape behind.
		base.State = StateClean
		base.Reason = "stale and unowned, no tagged resources remain"
		return base, nil
	}
	if !anchored {
		// Defect 2: every counted resource is a type in insufficientAloneTypes (today,
		// only security groups). Treat it the same as n==0 - see insufficientAloneTypes
		// for why a security group can never legitimately be the only thing left behind.
		// The count still goes in the report (base.Resources above) so the reason is
		// visible, but the state is Clean: Sweep never acts on Clean, and there is
		// nothing to destroy - AWS's own real state already has the VPC gone.
		base.State = StateClean
		base.Reason = fmt.Sprintf("%d tagged resources remain, but all are insufficient-alone evidence types (e.g. a stale security-group tag no longer backed by a real VPC); treating as torn down", n)
		return base, nil
	}
	if age > 0 {
		base.State = StateBlocked
		base.LockAge = age.String()
		base.Reason = fmt.Sprintf("%d resources still live but a stale lock (age %s) blocks an automatic destroy; needs a human force-unlock", n, age)
		return base, nil
	}
	// keep-on-failure, checked last and specifically here: everything above it (ownership,
	// staleness, lock) already had to pass for this candidate to even be orphan-shaped, so
	// this is the exact point where Sweep would otherwise destroy a stack a human
	// deliberately left up for debugging (Teardown's own keepOnFailure param, phases.go,
	// only protects the harness's OWN exit-handler teardown - the janitor calls Teardown
	// through RealTeardown, which does not thread this through, so nothing downstream of
	// this check would have caught it). StateKept, not StateActive, so the report says WHY
	// nothing happened instead of looking like an ordinary live run.
	if base.KeepOnFailure {
		base.State = StateKept
		base.Reason = fmt.Sprintf("%d resources still live past delete_after + grace, but the run set --keep-on-failure: left up on purpose, never auto-swept", n)
		return base, nil
	}
	base.State = StateOrphan
	base.Reason = fmt.Sprintf("%d resources still live past delete_after + grace with no owner: a teardown FAILED", n)
	return base, nil
}

// deniedResourceTypes is the short DENYLIST of types known to legitimately survive a
// SUCCESSFUL teardown. Everything NOT on this list counts by default - that inversion is
// the fix for defect 1: the old code sent these 17 types as an ALLOWLIST
// (ResourceTypeFilters), so any resource type not on it - dms:rep, dms:task,
// rds:global-cluster, ec2:natgateway, ec2:eip, lambda:function, acm:certificate, and
// every type CPI adds after this list was written - made a still-standing stack count as
// n==0, classify as "clean", and never get alerted or swept. That is permanent
// invisibility with no other signal to catch it (no manifest change, no state change,
// just quota silently gone). A short denylist means a new, untaught resource type is
// LOUD (it counts, a human investigates) rather than silent.
//
// A key with no colon denies the whole service; "service:type" denies only that one
// resource type, leaving the rest of that service counted normally.
var deniedResourceTypes = map[string]bool{
	// A successful Teardown schedules its customer-managed KMS key for deletion, and
	// AWS holds it in PendingDeletion for 7-30 days - the whole window it keeps its
	// Customer and deleteAfter tags. Measured against the real account: of 20 stacks
	// an unfiltered query once reported as orphans, 17 had zero non-PendingDeletion
	// resources. This was the original noise source that motivated filtering at all.
	"kms": true,
	// CloudWatch log groups outlive the stack that wrote to them and carry no signal
	// about whether anything is still running.
	"logs": true,
	// RDS/Aurora parameter groups are cheap, standalone config objects that commonly
	// survive a cluster delete without the cluster itself being alive.
	"rds:pg":         true,
	"rds:cluster-pg": true,
	// DMS certificates are uploaded once and reused across replication tasks; a
	// finished migration's cert says nothing about whether its task/instance is gone.
	"dms:cert": true,
	// EKS pod identity associations are known to trail a cluster delete asynchronously
	// and briefly outlive it without a live cluster behind them.
	"eks:podidentityassociation": true,
}

// insufficientAloneTypes are resource types whose presence, BY ITSELF with nothing else
// counted, is not enough to call a stack standing (defect 2). Security groups are the
// only entry today, and the reasoning is structural, not "this type is noisy": AWS
// refuses to delete a VPC while any non-default security group inside it still exists
// (DeleteVpc fails with DependencyViolation), so a real standing security group can never
// outlive its own VPC. That means "the tagging query found security groups for this
// customer and NOTHING else" is never what a genuinely standing stack looks like - it is
// exactly what the Resource Groups Tagging API's own indexing lag looks like. Measured
// against the real account: both of the two remaining false positives were security
// groups describe-security-groups reports as InvalidGroup.NotFound (5 of the 23
// deleteAfter-tagged security groups in the account are already gone this same way).
//
// This is deliberately NOT on deniedResourceTypes: a security group next to a live
// VPC/cluster/database/load balancer is completely ordinary and must still count. The
// check only fires when security groups are the WHOLE story, which is the one shape a
// real leak can never take.
var insufficientAloneTypes = map[string]bool{
	"ec2:security-group": true,
}

// arnResourceType splits a Resource Groups Tagging API ARN into its service
// ("ec2", "rds", ...) and, where the ARN carries one, its "service:resource-type" pair
// ("ec2:vpc", "rds:cluster-pg", ...). Resources whose ARN has no resource-type segment at
// all (S3 buckets, SNS topics, SQS queues addressed by name) fall back to the service
// name alone as the type. A malformed ARN (fewer than 6 colon-delimited fields, or no
// "arn" prefix) returns ("", "") - the caller must treat that as "count it": an ARN we
// cannot even parse is the last place to guess it is safe to ignore.
func arnResourceType(arnStr string) (service, resourceType string) {
	parts := strings.SplitN(arnStr, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return "", ""
	}
	service = parts[2]
	resource := parts[5]
	if resource == "" {
		return service, service
	}
	if sep := strings.IndexAny(resource, "/:"); sep >= 0 {
		return service, service + ":" + resource[:sep]
	}
	// No "/" or ":" in the resource segment: nothing further to key on beyond the
	// service itself (e.g. an SNS topic or SQS queue ARN, which names the resource
	// directly with no separate resource-type token).
	return service, service
}

func isDeniedResourceType(service, resourceType string) bool {
	return deniedResourceTypes[service] || deniedResourceTypes[resourceType]
}

// countTagged exhausts the paginator in every region and returns an error on any
// failure. Silent under-enumeration is the worst outcome available here: it makes the
// janitor declare a stack torn down when it is not, and then the leak is invisible
// forever (no manifest signal, no state, real infra still holding VPC quota).
//
// The query itself carries NO ResourceTypeFilters (defect 1's fix) - filtering happens
// after the fact, against each result's own ARN, so an unrecognized resource type is
// counted rather than silently dropped. anchored reports whether any counted resource is
// of a type OTHER than the ones in insufficientAloneTypes (defect 2's fix); the caller
// uses that to tell a genuinely standing stack apart from stale tagging-index residue.
func countTagged(ctx context.Context, tags map[string]TagAPI, regions []string, customer string) (total int, anchored bool, err error) {
	filters := []rgtypes.TagFilter{
		{Key: aws.String("Customer"), Values: []string{customer}},
		{Key: aws.String("deleteAfter")}, // key presence only; AND-ed with the filter above
	}
	// Fail closed when there is nothing to query. A manifest with no region and no
	// dr_region (any build predating those fields, or a partial write) would otherwise
	// skip the loop entirely and return (0, false, nil), which classify reads as n == 0
	// and reports as "no tagged resources remain" for a stack nobody actually looked at.
	// That is a silent miss, and silent misses are the worst outcome this file has: the
	// leak becomes invisible forever because clean is neither alerted nor swept. Every
	// other inconclusive path here fails closed to Unknown, so this one does too. G11
	// does not cover it: that gate checks the config's region, not the manifest's.
	queried := 0
	seenRegion := map[string]bool{}
	for _, region := range regions {
		if region == "" || seenRegion[region] {
			continue
		}
		queried++
		seenRegion[region] = true
		client, ok := tags[region]
		if !ok || client == nil {
			return 0, false, fmt.Errorf("no tagging client configured for region %q", region)
		}
		paginator := resourcegroupstaggingapi.NewGetResourcesPaginator(client, &resourcegroupstaggingapi.GetResourcesInput{
			TagFilters: filters,
			// Deliberately no ResourceTypeFilters: see the comment above. Sending one
			// here is exactly the defect-1 bug.
		})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return 0, false, fmt.Errorf("GetResources in %s: %w", region, err)
			}
			for _, res := range page.ResourceTagMappingList {
				service, resourceType := arnResourceType(aws.ToString(res.ResourceARN))
				if service != "" && isDeniedResourceType(service, resourceType) {
					continue
				}
				total++
				if service == "" || !insufficientAloneTypes[resourceType] {
					anchored = true
				}
			}
		}
	}
	if queried == 0 {
		return 0, false, fmt.Errorf("no usable region on the manifest (regions=%v): refusing to report a stack as clean without querying anything", regions)
	}
	return total, anchored, nil
}

// lockInfo mirrors the JSON the Terraform/OpenTofu S3 backend stores in a lock item's
// Info attribute (statemgr.LockInfo): ID/Operation/Info/Who/Version/Created/Path. Only
// Created is used here. NOT verified against a live item - see the janitor rollout notes.
type lockInfo struct {
	Created string `json:"Created"`
}

// lockState reads the lock item for every terraform.tfstate under prefix and returns
// whether any is "fresh" (younger than fresh) plus the oldest lock's age (0 when there is
// no lock at all). It lists state files under the prefix via S3 rather than assuming a
// fixed layout, because the physical/logical layer paths differ by env_path.
func lockState(ctx context.Context, locks LockAPI, api S3ListGetAPI, table, bucket, prefix string, now time.Time, freshWithin time.Duration) (bool, time.Duration, error) {
	var stateKeys []string
	var token *string
	for {
		out, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(bucket), Prefix: aws.String(prefix), ContinuationToken: token,
		})
		if err != nil {
			return false, 0, fmt.Errorf("list state objects under %s: %w", prefix, err)
		}
		for _, obj := range out.Contents {
			if obj.Key != nil && strings.HasSuffix(*obj.Key, "/terraform.tfstate") {
				stateKeys = append(stateKeys, *obj.Key)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}

	var oldestAge time.Duration
	found := false
	for _, key := range stateKeys {
		lockID := bucket + "/" + key
		out, err := locks.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(table),
			Key: map[string]ddbtypes.AttributeValue{
				"LockID": &ddbtypes.AttributeValueMemberS{Value: lockID},
			},
		})
		if err != nil {
			return false, 0, fmt.Errorf("GetItem %s: %w", lockID, err)
		}
		if len(out.Item) == 0 {
			continue // no lock held on this state file
		}
		infoAttr, ok := out.Item["Info"].(*ddbtypes.AttributeValueMemberS)
		if !ok || infoAttr.Value == "" {
			// A lock item with no readable Info: we cannot establish its age, so treat it
			// as fresh (fail closed) rather than assume it is safe to touch.
			return true, 0, nil
		}
		var li lockInfo
		if err := json.Unmarshal([]byte(infoAttr.Value), &li); err != nil || li.Created == "" {
			return true, 0, nil // same fail-closed reasoning
		}
		created, perr := time.Parse(time.RFC3339, li.Created)
		if perr != nil {
			return true, 0, nil
		}
		age := now.Sub(created)
		if age < freshWithin {
			return true, age, nil
		}
		if !found || age > oldestAge {
			oldestAge = age
			found = true
		}
	}
	return false, oldestAge, nil
}

// Sweep destroys the orphans a prior Scan found, capped at MaxSweeps. It calls the
// existing PhaseParams.Teardown (via d.Teardown) with failed=true so the full diagnostics
// dump happens BEFORE the stack goes away: an orphan is evidence that a teardown failed,
// and that evidence is the point. It deliberately does not purge S3 state, the manifest,
// or the lock table. Purging state is the one irreversible step, and Teardown already
// leaves state behind for the same reason: state pointing at nothing is recoverable,
// infra pointing at no state is not.
func Sweep(ctx context.Context, d JanitorDeps, o JanitorOptions, rep *Report) error {
	if d.Teardown == nil {
		return fmt.Errorf("janitor: Sweep called with no Teardown func wired")
	}
	// Defect 3: bound total attempts, not just successes. Each Teardown call can run up
	// to ~90 minutes in the historical worst case (10-scenario.yaml's own teardown
	// deadline is 10800s for exactly that reason), and the janitor pod's own
	// activeDeadlineSeconds (50-janitor-cron.yaml) was sized assuming "at most one
	// Teardown in-process" - the assumption defect 2's fix broke, because counting only
	// successes against the cap means a cycle with several distinct failing orphans
	// would attempt every one of them with nothing to stop it. maxFailures bounds that
	// without reintroducing defect 2: it still lets Sweep step past ONE persistently
	// failing candidate to reach the next real orphan in the SAME cycle (this is exactly
	// what TestSweepMakesProgressPastAPersistentlyFailingCandidate proves), it just stops
	// after a small, fixed number of failures instead of trying every orphan in the
	// report. A failed destroy usually fails FAST (a stale lock, an expired credential, a
	// dependency violation) rather than running the full worst case, so 2 is enough
	// headroom for the common "one bad apple" case while still keeping the cycle's total
	// worst-case Teardown time bounded and nowhere near the pod deadline.
	//
	// <= 0 falls back to this default rather than meaning "no cap" - a caller (or a
	// zero-value JanitorOptions in a future refactor) that forgets to set this field
	// must not silently reintroduce unbounded attempts.
	maxFailures := o.MaxSweepFailures
	if maxFailures <= 0 {
		maxFailures = 2
	}
	failedAttempts := 0
	for i := range rep.Candidates {
		c := &rep.Candidates[i]
		if c.State != StateOrphan {
			continue
		}
		if c.KeepOnFailure {
			// Defense in depth, same shape as G3 below: classify() already turns a
			// keep-on-failure orphan into StateKept before it ever reaches here, so
			// this only fires if a future refactor builds a Report by hand or a
			// candidate's State gets flipped back to Orphan without re-running
			// classify (TestSweepGuardsProtectedIdentityAgainAtDestroyTime exercises
			// the same scenario for guardProtected). Skip, don't destroy: a run
			// marked keep-on-failure was left up on purpose.
			c.SweepResult = "skipped: keep-on-failure"
			continue
		}
		// This cap counts SUCCESSES only, deliberately separate from maxFailures below.
		// Candidates arrive sorted by prefix (Scan, above) and that order is fixed for
		// the whole cycle, so counting failures against THIS cap let a single
		// persistently-failing orphan - a stack whose destroy keeps dying on the same
		// non-retryable reason a retry never fixes - sort first every cycle, burn the
		// one sweep slot on its own failure, and starve every real orphan behind it
		// forever. With max-sweeps 1 that was "retry candidate #1 until the heat death
		// of the universe, candidate #2 never runs." Counting only rep.Swept here means
		// one candidate's failure never costs the next candidate its turn in THIS SAME
		// cycle: the loop keeps walking the list and only stops once it has actually
		// destroyed MaxSweeps stacks (or run out of candidates, or hit maxFailures
		// below). The failing candidate still gets retried every cycle - that part is by
		// design, the evidence should keep surfacing - it just can no longer block
		// anyone else.
		if rep.Swept >= o.MaxSweeps {
			continue
		}
		if failedAttempts >= maxFailures {
			// Defect 3's bound. Enough candidates have already failed to destroy this
			// cycle - stop starting new attempts rather than risk the pod's
			// activeDeadlineSeconds killing one mid-destroy (which strands infra worse
			// than leaving it as a reported orphan). Every skipped candidate here is
			// still StateOrphan in the report and gets picked up again next cycle, same
			// as the one that failed.
			c.SweepResult = "skipped: max-sweep-failures reached this cycle"
			continue
		}
		// G3, second call site. The gate runs again immediately before the destroy, on
		// the same values, because between Scan and here is exactly where a refactor
		// would otherwise be free to lose it.
		if perr := guardProtected(c.Prefix, c.Identifier); perr != nil {
			return perr
		}
		p := PhaseParams{
			RepoDir: o.RepoDir, Matrix: d.Matrix,
			Store:      NewS3Store(d.S3, c.Bucket),
			ConfigName: c.ConfigName, RunID: c.RunID,
			AccountID: o.AccountID, Profile: o.Profile, Region: o.Region,
		}
		step("JANITOR sweep: %s (identifier %s, %d tagged resources still live)", c.Prefix, c.Identifier, c.Resources)
		if err := d.Teardown(ctx, p, true); err != nil {
			c.SweepResult = "failed: " + err.Error()
			rep.Failed++
			failedAttempts++
			continue
		}
		c.SweepResult = "destroyed"
		rep.Swept++
	}
	return nil
}

// RealTeardown wires JanitorDeps.Teardown to the actual PhaseParams.Teardown for
// production use (cmd/harness). Tests supply their own func instead.
func RealTeardown(ctx context.Context, p PhaseParams, failed bool) error {
	return p.Teardown(ctx, false, failed)
}
