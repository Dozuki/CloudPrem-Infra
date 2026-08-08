package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
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
	// StateResidue is set by Sweep only, after it has already tried: Teardown's
	// terragrunt destroy ran and reported success, but a re-query still finds tagged
	// resources standing. That is not a failed destroy, it is a destroy that succeeded
	// against everything terraform state actually tracked while something else
	// (created outside a tracked resource block, or already removed from state by a
	// prior run killed mid-teardown) survives untouched - a retry of Teardown cannot
	// reach it either, because Teardown only ever acts on what is in state. Reported
	// distinctly so a human sees "terraform can't reach this" instead of reading a
	// repeating, unexplained failure. See Sweep for why it does not count toward
	// MaxSweeps or MaxSweepFailures.
	StateResidue CandidateState = "residue"
)

// sweepResultDestroyed is the one SweepResult value that means a clean destroy. Named
// because two places have to agree on it: the writer below and the post-sweep orphan
// recount, which uses it to tell "still standing" from "gone" on a candidate whose
// State is deliberately left at Orphan.
const sweepResultDestroyed = "destroyed"

// residueIndexLagCaveat is appended to every StateResidue reason. The post-destroy
// re-query runs seconds after the destroy returns, and the Resource Groups Tagging API
// is an index, not the source of truth - the same lag that motivates
// insufficientAloneTypes can hand back entries for resources that are already gone.
// Waiting it out is not an option (a sleep inside Sweep spends the wall-clock budget
// the pod deadline depends on), so the honest fix is to say so in the text a human
// reads: a residue line that does not repeat next cycle was index lag, and one that
// does is real.
const residueIndexLagCaveat = " (note: these counts come from the tagging index, which can lag a destroy by minutes - residue that is genuinely gone will not be reported again next cycle)"

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
	// Customer, Region, DRRegion are the exact identity and regions classify() used
	// for its Resource Groups Tagging API query (G8). Carried on the candidate, not
	// just used and discarded, so Sweep can re-run the identical query after a
	// destroy to tell a clean success apart from state-orphaned residue (StateResidue)
	// without reloading the manifest a second time.
	Customer string `json:"customer,omitempty"`
	Region   string `json:"region,omitempty"`
	DRRegion string `json:"dr_region,omitempty"`
}

// JanitorReportSchemaVersion is stamped into every Report as schema_version. The Argo
// notify script (50-janitor-cron.yaml) asserts it before running its jq pipeline over
// the field names below: without a version, renaming a field here silently turns the
// Slack card into empty/null values instead of failing loud. Bump it whenever a field
// this report emits is renamed or removed, and update the script's assertion in the
// same change.
const JanitorReportSchemaVersion = 1

// Report is the whole cycle, emitted as JSON for the Slack step and printed as a table.
type Report struct {
	// SchemaVersion is always JanitorReportSchemaVersion. The json tag must stay
	// exactly "schema_version" - the notify script asserts on that literal key.
	SchemaVersion int         `json:"schema_version"`
	Mode          string      `json:"mode"` // "report" | "sweep"
	At            string      `json:"at"`
	Account       string      `json:"account"`
	Candidates    []Candidate `json:"candidates"`
	// Orphans counts StateOrphan candidates AS OF the last thing that touched the
	// report: Scan sets it, and Sweep recomputes it from the final candidate states
	// before returning. Without that recompute the post-sweep JSON kept Scan's
	// pre-sweep number, so the headline read "orphans=3" on a cycle that had just
	// destroyed all three - the notify script sidesteps it by counting candidates
	// itself, but any other parser reading the field would be wrong.
	Orphans int `json:"orphans"`
	Swept   int `json:"swept"`
	Failed  int `json:"failed"`
	// Residue counts StateResidue candidates: a destroy that succeeded against
	// terraform state but left tagged resources standing anyway (see StateResidue).
	// Kept apart from Failed on purpose - retrying Teardown cannot fix this, so
	// lumping it into "failed" would tell a human to do the one thing that provably
	// does not work.
	Residue int `json:"residue"`
	// Inconclusive counts candidates Sweep moved to StateUnknown: a destroy RAN and
	// the post-destroy verification query then failed, so nobody knows what is still
	// standing. Kept apart from Failed (the destroy itself did not fail) and from
	// Residue (residue is a known quantity; this is the absence of one), but it drives
	// the same non-zero exit code as Failed in runJanitor. Before this counter existed
	// the branch incremented nothing at all, so "we destroyed something and cannot see
	// what survived" was a green workflow - the one outcome in this file that most
	// needs a human was the only one with no signal attached.
	Inconclusive int `json:"inconclusive"`
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

// neverSweepConfigNames are the recovery-drill configs (matrix.yaml). They stand their
// stacks up in the DR region and are not wired through the Argo phases at all (README
// "Not covered"), so the janitor has never been able to destroy one correctly. Until
// this list existed they were shielded only by classify()'s G11 region check, which
// compares against the janitor's own --region flag: point a janitor at the DR region
// for any legitimate reason and G11 stops matching, leaving nothing between the
// recovery stacks and a destroy. G12 (classify) is the wall that does not move when a
// flag does.
//
// Deliberately NOT part of guardProtected. These configs are EXERCISED - a drill runs
// them regularly and leaves manifests under <runID>-recover/ and
// <runID>-recover_source/ behind - so a hit is an ordinary, expected candidate, not
// evidence that the detection logic is broken. guardProtected's contract is to abort
// the whole cycle on a hit, which would mean every janitor cycle overlapping or
// following a drill produces no report at all: no orphan detection, no Slack card,
// nothing, for as long as the drill's state prefix exists. A per-candidate wall
// (StateNeedsReview, cycle continues) protects the same stacks without taking the rest
// of the fleet's reporting down with them.
var neverSweepConfigNames = map[string]bool{"recover": true, "recover_source": true}

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
	// MaxSweeps caps SUCCESSFUL destroys in one cycle. <= 0 falls back to 1 in
	// sweepMax(), never to "no cap" and never to "sweep nothing" - see that accessor.
	MaxSweeps int
	// MaxSweepFailures caps FAILED destroy attempts in one cycle, independent of
	// MaxSweeps (defect 3). <= 0 falls back to a default of 2 in Sweep, not to
	// "unbounded" - see the comment on that fallback for why zero is never treated as
	// "no cap".
	MaxSweepFailures int
	// SweepBudget is the wall-clock ceiling on how long Sweep will keep STARTING new
	// destroy attempts, checked against o.now() before each one. A value > 0 is an
	// explicit operator instruction and sweepBudget() honors it verbatim; <= 0 means
	// "derive one" - see sweepBudget and DefaultSweepBudget for why this exists
	// (defect: Sweep used to bound attempt COUNT, not attempt TIME, and a slow Teardown
	// has no internal timeout of its own).
	SweepBudget time.Duration
	// ProcessStart is when the janitor PROCESS started, not when Sweep was entered. The
	// pod's activeDeadlineSeconds clock started at process start while Sweep's own
	// clock starts much later, and the gap between the two is what sweepSetupMargin
	// only ESTIMATES. When this is set, sweepBudget() derives the budget from the pod
	// deadline minus the MEASURED gap, so a Scan that runs long eats into the budget
	// instead of the pod deadline. Zero value means "no measurement available" and the
	// budget stays the flat estimate-derived one.
	ProcessStart time.Time
	Now          func() time.Time // injected for tests; production is time.Now
}

func (o JanitorOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// JanitorPodActiveDeadlineSeconds MUST match the `scan` container's
// activeDeadlineSeconds in 50-janitor-cron.yaml - that YAML value carries a comment
// pointing back here for the same reason. This is the single source of truth; the
// YAML is the consumer. If the pod deadline ever needs to change, change it here
// first and let DefaultSweepBudget's derivation carry the new number into Sweep
// automatically, then copy the same literal into the YAML (Go cannot reach into a
// YAML manifest at build time, so the copy is manual - but there is only one number
// to copy, not two independently-tuned ones to keep in sync by hand).
const JanitorPodActiveDeadlineSeconds = 12600

// sweepSetupMargin is reserved for everything in the pod that runs BEFORE Sweep's own
// clock starts and so is never inside its budget: image pull/container start, the
// workspace repo clone or incremental fetch (docker-entrypoint.sh, unconditional even
// in report mode), Vault login, and Scan itself (S3 listings + tag lookups, "minutes"
// per 50-janitor-cron.yaml's own comment). 30 minutes is generous against all of
// that put together, and generous is the right direction to round: undercounting the
// margin is what would let Sweep itself blow the pod deadline.
const sweepSetupMargin = 30 * time.Minute

// sweepPreProcessReserve covers the part of the pod's life that runs BEFORE this
// process exists and that ProcessStart therefore cannot measure: the image pull and
// container start, docker-entrypoint.sh's repo clone or incremental fetch, and the
// Vault login. ProcessStart is stamped at runJanitor's first line, so every one of
// those is already spent by the time there is anything to measure - subtracting only
// the measured elapsed time would hand Sweep a budget that overruns the pod deadline
// by exactly that unmeasured head start, and a destroy SIGKILLed mid-run strands infra
// worse than one never started (the whole reason this budget exists).
//
// 10 minutes against a measured worst case of ~2 (pull + clone + vault login on a warm
// node), rounded up hard because the cost of overestimating is a slightly smaller
// budget while the cost of underestimating is a killed destroy.
const sweepPreProcessReserve = 10 * time.Minute

// DefaultSweepBudget derives Sweep's wall-clock budget from
// JanitorPodActiveDeadlineSeconds by subtracting sweepSetupMargin, rather than being
// a second, independent magic number that could silently drift out of agreement with
// the pod deadline. At today's values that is 12600s - 1800s = 10800s (3h) - which,
// not by coincidence but because it is the same worst case, is exactly enough wall
// clock for two attempts at the historical ~90-minute worst case per Teardown call
// (10-scenario.yaml's own teardown deadline is 10800s for that identical reason). A
// third attempt started right after two such worst-case ones would push the pod past
// its deadline mid-destroy - the exact failure this budget exists to prevent - so the
// budget check in Sweep (elapsed >= budget) refuses to start it.
//
// sweepSetupMargin is an ESTIMATE of everything that runs before Sweep's clock starts.
// When JanitorOptions.ProcessStart is set (main.go stamps it at process start),
// sweepBudget() derives the budget from the pod deadline and the MEASURED elapsed time
// instead of calling this function at all, so a Scan that runs long eats into the
// budget rather than silently eating into the pod deadline.
func DefaultSweepBudget() time.Duration {
	total := time.Duration(JanitorPodActiveDeadlineSeconds) * time.Second
	if total <= sweepSetupMargin {
		// Defensive only: a misconfigured pod deadline shorter than the margin must
		// never derive a zero or negative budget (which Sweep would read as "no time
		// ever, skip everything" - silently doing nothing is its own failure mode).
		// Falling back to the margin itself keeps the budget small and finite, which
		// is loud in a report (almost nothing attempted) rather than silently wrong.
		return sweepSetupMargin
	}
	return total - sweepSetupMargin
}

// sweepBudget resolves the wall-clock ceiling Sweep checks before each destroy.
//
// An operator-supplied o.SweepBudget is returned verbatim, whatever ProcessStart says:
// it is an explicit "spend at most this long in Sweep" instruction (a --sweep-budget
// flag, or a test), and quietly shortening a number someone typed is worse than
// honoring it.
//
// Otherwise the budget is derived, and ProcessStart decides how. sweepSetupMargin is
// only an ESTIMATE of the pre-Sweep cost (image pull, repo clone, Vault login, Scan),
// so when main.go has stamped ProcessStart there is a real measurement to use instead:
// subtract the MEASURED elapsed time from the pod deadline directly rather than from
// DefaultSweepBudget(), which has already subtracted the estimate. Subtracting from
// DefaultSweepBudget() would charge setup twice - a fast 5-minute setup would still lose
// the full 30-minute margin on top of its own 5 - and the point of measuring is to
// replace the guess, not to stack on top of it. sweepPreProcessReserve still comes off
// the top, because the measurement starts at process start and the pod's clock started
// before that.
//
// The result can be zero or negative, and that is a real answer, not a bug to clamp
// away: it means the pod deadline is already spent and Sweep must start NOTHING. The
// previous version returned a small positive floor instead, which - because the budget
// check compares elapsed-INSIDE-Sweep and elapsed is 0 for the first candidate - let a
// pod at or past its deadline start one more multi-hour destroy and get SIGKILLed
// mid-run, the exact failure this budget exists to prevent. Sweep records the
// skip-everything outcome on each candidate instead (see its budget checks), so "did
// nothing" is stated in the report rather than silent.
func (o JanitorOptions) sweepBudget() time.Duration {
	if o.SweepBudget > 0 {
		return o.SweepBudget
	}
	if o.ProcessStart.IsZero() {
		return DefaultSweepBudget()
	}
	return time.Duration(JanitorPodActiveDeadlineSeconds)*time.Second -
		o.now().Sub(o.ProcessStart) - sweepPreProcessReserve
}

// sweepMax is the SUCCESSFUL-destroy cap, with the same "<= 0 means the conservative
// default, never a silent extreme" rule MaxSweepFailures already follows. Zero used to
// be read literally, so --max-sweeps=0 made every candidate skip while the cycle still
// reported success: a sweep that looks armed and destroys nothing, forever. 1 is the
// production value (50-janitor-cron.yaml) and the safe end of the range.
func (o JanitorOptions) sweepMax() int {
	if o.MaxSweeps <= 0 {
		return 1
	}
	return o.MaxSweeps
}

// JanitorDeps are the AWS/Argo surfaces, injected so the predicate is unit-testable
// without AWS or a cluster.
type JanitorDeps struct {
	S3   S3ListGetAPI
	Tags map[string]TagAPI // keyed by region
	// Locks is keyed by region, like Tags, because the lock table is per region. The
	// terraform S3 backend (live/root.hcl) sets `region = local.aws_region` on the
	// backend block while every region reuses the same table NAME
	// (dozuki-terraform-lock), so a DR-region unit's lock item lives in the DR
	// region's copy of the table - and the account really does have items in both
	// (us-east-1 and us-west-2 both hold live items today). A primary-only client
	// reads the DR table as permanently empty, which silently weakens G7's lock gate
	// for exactly the split-region runs candidateBuckets exists to cover.
	Locks map[string]LockAPI
	// Digests clears the S3 backend's -md5 consistency digest for a candidate's state
	// keys immediately before Sweep destroys it (a stale digest aborts terragrunt
	// destroy before it touches a single resource - see clearStateDigests). Keyed by
	// region for the same reason as Locks, and worse than a read gap when it is wrong:
	// DynamoDB DeleteItem on a missing item succeeds, so a primary-only client
	// "clears" DR digests that were never touched and the destroy then aborts on the
	// stale one it could not reach. Optional: nil (or an empty map) skips the step,
	// which is what every test that does not care about it wires by leaving this unset.
	Digests map[string]DigestAPI
	// DMS is the residue-reclaim client set, keyed by region like Tags - residue can be
	// in either the primary or DR region. Optional: nil (or a missing region key) just
	// means reclaimResidueByARN skips any ARN that would have needed it, leaving that
	// resource reported rather than deleted.
	DMS    map[string]DMSReclaimAPI
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

// DigestAPI is the WRITE surface for the S3 backend's -md5 consistency digest, and
// nothing else. LockAPI stays read-only on purpose: the janitor must never
// force-release a real state lock unattended. Kept as a distinct type so a future
// refactor cannot wire lock deletion through this path by widening one interface.
type DigestAPI interface {
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
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

// stateLocation is one state bucket plus the region it lives in. The region travels
// WITH the bucket rather than being parsed back out of the bucket name later: the
// DynamoDB lock table is per region, so every lock/digest query has to be issued by the
// client for that bucket's region, and recovering the region by substring-matching a
// name is the fragile shape this file avoids everywhere else.
type stateLocation struct {
	Region string
	Bucket string
}

// candidateBuckets returns every S3 state bucket a run's resources could plausibly be
// split across for a given (region, DR region) pair: the primary bucket always, plus
// the DR bucket when one is configured and distinct. A run's manifest and its actual
// terraform state do not always share a bucket (the recovery rebuild keeps its
// manifest in the primary bucket and its state in the DR one - see Scan's comment on
// this), so anything that needs to find a prefix's real state files - the lock check,
// the digest clear - must search this whole list rather than assume "the bucket a
// prefix's manifest happened to be found in" is also the bucket its state lives in;
// treating those as the same bucket is what silently reads a split-bucket run's lock
// table as empty.
func candidateBuckets(accountID, region, drRegion string) []stateLocation {
	locs := []stateLocation{{Region: region, Bucket: stateBucket(accountID, region)}}
	if drRegion != "" && drRegion != region {
		locs = append(locs, stateLocation{Region: drRegion, Bucket: stateBucket(accountID, drRegion)})
	}
	return locs
}

// Scan is the whole read-only half: enumerate, classify, return. It performs NO mutation
// and is what runs in report mode. Sweep calls it first and then acts on StateOrphan only.
func Scan(ctx context.Context, d JanitorDeps, o JanitorOptions, wfList JanitorWorkflowList) (*Report, error) {
	now := o.now().UTC()
	owners := runningOwners(wfList)
	byWF := byNameOrRunID(wfList)

	rep := &Report{SchemaVersion: JanitorReportSchemaVersion, Mode: "report", At: now.Format(time.RFC3339), Account: o.AccountID}
	if o.Sweep {
		rep.Mode = "sweep"
	}

	// Both buckets. A run's manifest is not always in the bucket holding its state: the
	// store is built once from the matrix default region while each unit's state follows
	// the region it deploys to, so the recovery rebuild keeps its manifest in the primary
	// bucket and its state in the DR one (phases.go stateBucket).
	buckets := candidateBuckets(o.AccountID, o.Region, o.DRRegion)

	seen := map[string]bool{}
	for _, loc := range buckets {
		bucket := loc.Bucket
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

// maxListPages bounds any single S3 listing. Generous on purpose: at 1000 keys per
// page this is ten million objects under one prefix, far past anything the harness
// buckets hold, so hitting it means the paginator is not converging (a repeated
// continuation token, an S3-compatible layer behaving oddly) rather than a genuinely
// enormous bucket. Without it that shape loops until the pod's activeDeadlineSeconds
// kills the cycle, which reads to a human as "the janitor hung", not "listing broke".
const maxListPages = 10000

// eachListPage runs the ListObjectsV2 paginator for one input and calls fn on every
// page. Both listing loops in this file go through it so the two failure rules are
// written once:
//
//   - truncated with no continuation token is an ERROR, not a break. The old loops
//     treated it as the end of the listing, which is a SILENT short read: fewer
//     candidates than really exist (a leak nobody sees) or fewer state keys than really
//     exist (digests left unclear). S3's contract says a truncated response always
//     carries a token, so this only fires when something upstream is violating it - and
//     that is exactly when guessing is worst.
//   - a NIL IsTruncated is the same class of error, by the same standard. Real S3
//     always sets the field on a ListObjectsV2 response, so nil means the response is
//     not the contract this loop reasons about (an S3-compatible layer, a mangled
//     proxy) - and reading nil as "final page", which this used to do, is the identical
//     silent short read as the case above with none of the loudness.
//   - a page cap, see maxListPages.
//
// The caller's input is copied per page rather than mutated, so a caller can safely
// reuse the input it passed in.
func eachListPage(ctx context.Context, api S3ListGetAPI, in *s3.ListObjectsV2Input, fn func(*s3.ListObjectsV2Output)) error {
	var token *string
	for page := 1; ; page++ {
		if page > maxListPages {
			return fmt.Errorf("listing %s (prefix %q) did not terminate within %d pages: refusing to keep paging",
				aws.ToString(in.Bucket), aws.ToString(in.Prefix), maxListPages)
		}
		req := *in
		req.ContinuationToken = token
		out, err := api.ListObjectsV2(ctx, &req)
		if err != nil {
			return err
		}
		fn(out)
		if out.IsTruncated == nil {
			return fmt.Errorf("listing %s (prefix %q) returned no IsTruncated flag: refusing to guess whether this page was the last one",
				aws.ToString(in.Bucket), aws.ToString(in.Prefix))
		}
		if !*out.IsTruncated {
			return nil
		}
		if out.NextContinuationToken == nil {
			return fmt.Errorf("listing %s (prefix %q) reported a truncated result with no continuation token: refusing to treat a short listing as complete",
				aws.ToString(in.Bucket), aws.ToString(in.Prefix))
		}
		token = out.NextContinuationToken
	}
}

// listTopPrefixes returns the top-level "directories" in bucket - the run prefixes plus
// whatever else lives at the top level (standard/, _templates/, ...). A delimited listing,
// not a recursive one: nothing below the first "/" is ever inspected here.
func listTopPrefixes(ctx context.Context, api S3ListGetAPI, bucket string) ([]string, error) {
	var prefixes []string
	err := eachListPage(ctx, api, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucket),
		Delimiter: aws.String("/"),
	}, func(out *s3.ListObjectsV2Output) {
		for _, cp := range out.CommonPrefixes {
			if cp.Prefix != nil {
				prefixes = append(prefixes, *cp.Prefix)
			}
		}
	})
	if err != nil {
		return nil, err
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
	// computedCustomer, not "customer": this is TODAY's recompute from the matrix in
	// the current checkout, which is not necessarily the identity the run applied
	// under. queryCustomer below is the one the AWS query actually uses, and G8's
	// identity-drift branch is entirely about the two disagreeing - naming them apart
	// is what keeps that readable.
	computedCustomer, _ := salted.FeatureFlags["customer"].(string)
	identifier := ""
	if computedCustomer != "" {
		identifier = computedCustomer + "-" + cfg.Env
	}

	// G3, first call site. Abort the whole cycle, do not skip this one candidate.
	if perr := guardProtected(prefix, identifier); perr != nil {
		return nil, perr
	}

	base := &Candidate{
		Prefix: prefix, Bucket: bucket, RunID: runID, ConfigName: m.ConfigName,
		Identifier: identifier, DeleteAfter: m.DeleteAfter,
		Customer: computedCustomer, Region: m.Region, DRRegion: m.DRRegion,
	}
	if wf, ok := byWF[runID]; ok {
		base.WorkflowPhase = wf.Status.Phase
		base.KeepOnFailure = wf.param("keep-on-failure") == "true"
	}
	// Defect P1 fix: the workflow-index value set just above is a best-effort fallback
	// only now, not the source of truth. Argo TTLs the Workflow CR three days after it
	// finishes (10-scenario.yaml ttlStrategy) - well inside "someone left a stack up
	// over a weekend to debug it" - and once the CR is gone byWF has no entry at all, so
	// the index silently reads back to false regardless of what a human actually chose.
	// "a human explicitly asked to keep this" is exactly the intent a sweeper must never
	// override on a technicality like an expired index. phases.go's Teardown now writes
	// this decision into the manifest on every call (durable, outlives the CR), so it
	// wins whenever it has an opinion. A manifest from before that write existed has no
	// opinion (KeepOnFailureRecorded is false), so it falls back to whatever the
	// workflow index above found - the same as today's behavior for an old run.
	if m.KeepOnFailureRecorded {
		base.KeepOnFailure = m.KeepOnFailure
	}

	// A salt that did not change anything (customer already at the 10-char terraform cap,
	// or the config carries no "customer" flag at all) means the identifier is not
	// run-unique and could collide with a long-lived stack. Refuse to reason about it
	// rather than work out whether this particular one is safe.
	unsaltedCustomer, _ := cfg.FeatureFlags["customer"].(string)
	if computedCustomer == "" || unsaltedCustomer == computedCustomer {
		base.State = StateNeedsReview
		base.Reason = "salted customer did not change from the base config value; identifier may not be run-unique"
		return base, nil
	}

	// G12: the recovery-drill configs, by name, independent of any region flag. G11
	// below shields the same stacks only while the janitor is pointed at the primary
	// region; this one holds when it is not. Per candidate, not a cycle abort: these
	// configs are exercised regularly and a drill's own state prefix is an ordinary
	// thing to find, so aborting on it would silence the whole report - see
	// neverSweepConfigNames.
	if neverSweepConfigNames[m.ConfigName] {
		base.State = StateNeedsReview
		base.Reason = "recovery drill config, never sweepable: " + m.ConfigName + " stands its stack up in the DR region and is not wired through the Argo phases, so the janitor cannot destroy it correctly"
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
	//
	// Search every bucket the manifest's OWN recorded region split could put state in
	// (m.Region/m.DRRegion, not o.Region/o.DRRegion) - not just `bucket`, the one this
	// prefix's manifest happened to be found in. Manifest and state can live in
	// different buckets (candidateBuckets), and using only `bucket` here would read a
	// split-bucket run's lock table as permanently empty.
	lockBuckets := candidateBuckets(o.AccountID, m.Region, m.DRRegion)
	fresh, age, corruptLock, lerr := lockState(ctx, d.Locks, d.S3, o.LockTable, lockBuckets, prefix, now, o.LockFresh)
	if lerr != nil {
		base.State = StateUnknown
		base.Reason = "lock check inconclusive: " + lerr.Error()
		return base, nil
	}
	if fresh {
		base.State = StateActive
		base.Reason = "a state lock under this prefix is fresh; something is applying right now"
		if corruptLock {
			// Same verdict, different fact. A lock whose Info payload is missing or
			// unparseable is treated as fresh (fail closed, lockState), but it reads
			// identically to a real in-progress apply in the report - and the two want
			// opposite responses from a human, so say which one this was.
			base.Reason = "a state lock under this prefix has a corrupt or unreadable Info payload, so its age could not be established; treated as fresh (fail closed) - a human may need to inspect the lock item"
		}
		return base, nil
	}

	// G8: live resources, confirmed by TAG VALUES. Defect P2 fix: query with the customer
	// this run ACTUALLY applied, not a fresh recompute of Config.Salted against today's
	// matrix checkout. The matrix is a live file in the repo - if a config's "customer"
	// feature flag is edited after a run starts and that run later leaks, recomputing
	// here would query AWS for a tag the run never carried, find nothing, and
	// permanently report a genuine leak Clean. Clean is neither alerted nor swept, so
	// that is the silent under-detection this file's own countTaggedDetailed comment
	// calls "the worst outcome available here". Provision now writes the identity it
	// actually applied into the manifest at apply time (phases.go, RunManifest.
	// AppliedCustomer) - that recorded value is authoritative here whenever present.
	queryCustomer := computedCustomer
	identityNote := ""
	// identityUnverified: this config salts a real customer identity, but the manifest
	// predates recording what was actually applied (a pre-fix run, or a write that
	// failed after the field was added). The query below still runs against the
	// RECOMPUTED value in that case, on purpose, rather than jumping straight to
	// NeedsReview: the recompute is only WRONG when the config's customer flag was
	// edited after this particular run applied, which is rare, and refusing to trust
	// EVERY pre-fix manifest would quarantine every already-confirmed historical orphan
	// behind a human gate for no reason. What the recompute can never be trusted for is
	// a NEGATIVE result: if the guess is wrong, it queries a tag nothing carries, finds
	// zero, and that is defect P2's exact failure (a real leak reported permanently
	// Clean). So only the "found nothing" branches below downgrade to NeedsReview when
	// this flag is set; a POSITIVE match (something real IS tagged under the recomputed
	// value) is trustworthy regardless, because Config.Salted appends a run-and-config
	// SHA256 suffix - two unrelated runs coincidentally salting to the same customer
	// value is not a realistic risk.
	identityUnverified := unsaltedCustomer != "" && m.AppliedCustomer == ""
	if !identityUnverified && unsaltedCustomer != "" {
		queryCustomer = m.AppliedCustomer
		if queryCustomer != computedCustomer {
			// The matrix now salts this config to a different customer than the run
			// recorded - the config's "customer" flag was very likely edited after this
			// run applied. Trust the recorded value (it is what actually owns the AWS
			// resources) but say so plainly rather than silently preferring one, and run
			// guardProtected again against it: this is a NEW identity the first gate
			// (computed from today's matrix, above) never had the chance to see.
			base.Identifier = queryCustomer + "-" + cfg.Env
			base.Customer = queryCustomer
			if perr := guardProtected(prefix, base.Identifier); perr != nil {
				return nil, perr
			}
			identityNote = fmt.Sprintf(" [identity drift: this run recorded customer %q; config %q now salts to %q instead - using the run's recorded identity, not today's]", queryCustomer, m.ConfigName, computedCustomer)
		}
	} else if identityUnverified {
		identityNote = " [identity unverified: manifest predates applied-customer recording, so this query used today's recomputed matrix value rather than a recorded one]"
	}

	// deleteAfter is present only when the harness generated env.hcl (root.hcl only
	// emits the tag when delete_after is non-empty, and only withDeleteAfter ever sets
	// it). A hand-authored stack cannot match either filter.
	n, anchored, terr := countTagged(ctx, d.Tags, []string{m.Region, m.DRRegion}, queryCustomer)
	if terr != nil {
		base.State = StateUnknown
		base.Reason = "tag lookup inconclusive: " + terr.Error() + identityNote
		return base, nil
	}
	base.Resources = n
	if n == 0 {
		if identityUnverified {
			// The exact P2 failure mode: a "found nothing" verdict built on a GUESSED
			// identity is not trustworthy enough to call this stack torn down. DO NOT
			// guess - see the identityUnverified comment above.
			base.State = StateNeedsReview
			base.Reason = "manifest predates applied-customer recording (pre-fix run) and the recomputed identity found no tagged resources: a false negative here is exactly the P2 failure mode this fix closes (the recompute could be wrong if the config's customer flag changed since this run applied) - needs human review before trusting this as torn down"
			return base, nil
		}
		// Normal, not a leak: Teardown never deletes the manifest or the state prefix, so
		// every successfully torn-down run in history leaves this exact shape behind.
		base.State = StateClean
		base.Reason = "stale and unowned, no tagged resources remain" + identityNote
		return base, nil
	}
	if !anchored {
		// Defect 2: every counted resource is a type in insufficientAloneTypes (today,
		// only security groups). Treat it the same as n==0 - see insufficientAloneTypes
		// for why a security group can never legitimately be the only thing left behind.
		if identityUnverified {
			base.State = StateNeedsReview
			base.Reason = fmt.Sprintf("manifest predates applied-customer recording (pre-fix run); the recomputed identity found only %d insufficient-alone-evidence resources, functionally a negative result - needs human review before trusting this as torn down", n)
			return base, nil
		}
		// The count still goes in the report (base.Resources above) so the reason is
		// visible, but the state is Clean: Sweep never acts on Clean, and there is
		// nothing to destroy - AWS's own real state already has the VPC gone.
		base.State = StateClean
		base.Reason = fmt.Sprintf("%d tagged resources remain, but all are insufficient-alone evidence types (e.g. a stale security-group tag no longer backed by a real VPC); treating as torn down%s", n, identityNote)
		return base, nil
	}
	if age > 0 {
		base.State = StateBlocked
		base.LockAge = age.String()
		base.Reason = fmt.Sprintf("%d resources still live but a stale lock (age %s) blocks an automatic destroy; needs a human force-unlock%s", n, age, identityNote)
		return base, nil
	}
	// keep-on-failure, checked last and specifically here: everything above it (ownership,
	// staleness, lock) already had to pass for this candidate to even be orphan-shaped, so
	// this is the exact point where Sweep would otherwise destroy a stack a human
	// deliberately left up for debugging. StateKept, not StateActive, so the report says
	// WHY nothing happened instead of looking like an ordinary live run.
	if base.KeepOnFailure {
		base.State = StateKept
		base.Reason = fmt.Sprintf("%d resources still live past delete_after + grace, but the run set --keep-on-failure: left up on purpose, never auto-swept%s", n, identityNote)
		return base, nil
	}
	base.State = StateOrphan
	base.Reason = fmt.Sprintf("%d resources still live past delete_after + grace with no owner: a teardown FAILED%s", n, identityNote)
	return base, nil
}

// deniedResourceTypes is the short DENYLIST of types known to legitimately survive a
// SUCCESSFUL teardown. Everything NOT on this list counts by default - that inversion is
// the fix for defect 1: the old code sent these types as an ALLOWLIST
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
//
// KMS is the ONLY entry, and deliberately so. The account owner's rule for everything
// else this janitor looks at is "if it carries the harness's Customer + deleteAfter
// tags, it is disposable, so count it" - there is no policy reason for a dangling
// eks:podidentityassociation to be invisible while a dangling dms:endpoint anchors an
// orphan. That asymmetry used to exist (logs, rds:pg, rds:cluster-pg, dms:cert,
// eks:podidentityassociation were all denied) and it is gone: those five types now
// count like anything else. KMS survives that same directive not as a policy
// exception but because excluding it is MECHANICAL, not a judgment call about
// disposability - see below.
var deniedResourceTypes = map[string]bool{
	// A SUCCESSFUL Teardown schedules its customer-managed KMS key for deletion via
	// ScheduleKeyDeletion. Verified against the AWS KMS docs (deleting-keys.html,
	// "About the waiting period"): AWS enforces a mandatory 7-30 day waiting period
	// (default 30) in KeyState=PendingDeletion before the key is actually deleted,
	// and "after the waiting period ends, AWS KMS deletes the KMS key, its aliases,
	// and all related AWS KMS metadata" - i.e. the key's tags (Customer, deleteAfter)
	// are part of that metadata and are NOT removed until the key is actually,
	// finally deleted at the end of the window. For up to 30 days after a teardown
	// that fully succeeded, the key still exists, still carries both tags the
	// Resource Groups Tagging API filters on, and CANNOT be deleted any further by
	// this janitor or anyone else - it is already scheduled, and there is nothing a
	// Teardown retry could do to it. Counting it would therefore nuke nothing while
	// marking every successfully-torn-down stack an orphan for up to a month. This
	// was the original false-positive source: of 20 stacks an unfiltered query once
	// reported as orphans, 17 had zero non-PendingDeletion resources.
	"kms": true,
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
//
// SURVIVES the "anything smoke-tagged is disposable, count it" directive that shrank
// deniedResourceTypes to KMS only (see that var's comment). The two rules answer
// different questions. deniedResourceTypes is a POLICY call about whether a real,
// present resource is worth reporting on - and under the new directive, almost
// nothing is exempt. insufficientAloneTypes is not policy at all: it exists because
// AWS's own tagging index can return an entry for a security group that
// describe-security-groups already reports as InvalidGroup.NotFound - the object is
// gone, the index just has not caught up. That is a data-quality problem, not a
// disposability judgment, and the fix for KMS's mechanical exemption applies here for
// the identical reason: counting stale index residue nukes nothing (there is nothing
// left to nuke) while manufacturing a false orphan. The directive that resolved the
// pod-identity/dms-endpoint asymmetry has nothing to say about a resource the index
// is simply wrong about, so this rule stays.
//
// eks:podidentityassociation joined for the identical reason, measured 2026-08-05: 4 of the
// 4 deleteAfter-tagged associations in the account name clusters that no longer exist
// (describe-pod-identity-association returns ResourceNotFoundException "No cluster found",
// and eks list-clusters shows only dev-min). An association cannot outlive its cluster, so
// like a security group it can never legitimately be the whole story. It is worse than
// cosmetic under sweep: the one candidate it manufactured has a physical state with ZERO
// resources and a logical state whose kubernetes and vault providers point at the deleted
// cluster, so Teardown dies at provider init rather than reaching StateResidue, and would
// burn a sweep-failure slot every cycle forever without ever converging.
var insufficientAloneTypes = map[string]bool{
	"ec2:security-group":         true,
	"eks:podidentityassociation": true,
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

// arnRegion returns an ARN's region field ("" on a malformed ARN, same fail-open-to-
// empty posture as arnResourceType - the caller treats an unmatched region as "no
// client configured, skip" rather than guessing one). Residue can legitimately be in
// either the primary or DR region, so the reclaim dispatcher picks the DMS client by
// what the ARN itself says rather than assuming the candidate's primary region.
func arnRegion(arnStr string) string {
	parts := strings.SplitN(arnStr, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ""
	}
	return parts[3]
}

// arnResourceID returns the segment of an ARN's resource part after the first "/" or
// ":" separator (the DMS replication-subnet-group ARN shape is
// "arn:aws:dms:<region>:<account>:subgrp:<name>", and DeleteReplicationSubnetGroup
// takes that trailing <name>, not the ARN). Empty on a malformed ARN or one with no
// separator in its resource segment.
func arnResourceID(arnStr string) string {
	parts := strings.SplitN(arnStr, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ""
	}
	resource := parts[5]
	if sep := strings.IndexAny(resource, "/:"); sep >= 0 {
		return resource[sep+1:]
	}
	return ""
}

// DMSReclaimAPI is the minimal DMS surface residue reclaim needs: one delete call per
// registered resourceType. Kept separate from any wider DMS interface so this stays
// obviously incapable of anything but the two calls the registry actually issues.
type DMSReclaimAPI interface {
	DeleteEndpoint(context.Context, *databasemigrationservice.DeleteEndpointInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DeleteEndpointOutput, error)
	DeleteReplicationSubnetGroup(context.Context, *databasemigrationservice.DeleteReplicationSubnetGroupInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DeleteReplicationSubnetGroupOutput, error)
}

// residueDeleters maps a resourceType to a single-call delete. Deliberately tiny and
// evidence-driven: these are the types actually observed surviving a successful
// destroy (smoke4879-bi's DMS endpoints and subnet group, where VPC and RDS were
// already gone - see the StateResidue comment and TestSweepDetectsResidueAfterA
// SuccessfulDestroy). Anything not listed here stays reported-only, unchanged. A
// universal "delete anything by ARN" dispatcher is a much bigger blast radius than this
// round asked for, and most AWS services have no single-call delete anyway (a security
// group needs its rules stripped first, an ENI needs to be detached, ...).
//
// ec2:volume and ec2:launch-template are deliberately NOT here: phases.go's
// reclaimOutOfStateResources already reclaims both inside Teardown, which runs before
// this re-query, so a genuinely leaked one of either cannot still be standing by the
// time residue reclaim looks.
var residueDeleters = map[string]func(context.Context, DMSReclaimAPI, string) error{
	"dms:endpoint": func(ctx context.Context, api DMSReclaimAPI, arn string) error {
		_, err := api.DeleteEndpoint(ctx, &databasemigrationservice.DeleteEndpointInput{EndpointArn: aws.String(arn)})
		return err
	},
	"dms:subgrp": func(ctx context.Context, api DMSReclaimAPI, arn string) error {
		id := arnResourceID(arn)
		if id == "" {
			return fmt.Errorf("could not extract subnet group identifier from arn %q", arn)
		}
		_, err := api.DeleteReplicationSubnetGroup(ctx, &databasemigrationservice.DeleteReplicationSubnetGroupInput{ReplicationSubnetGroupIdentifier: aws.String(id)})
		return err
	},
}

// reclaimResidueByARN attempts a single registered delete for every arn whose
// arnResourceType has an entry in residueDeleters, using the DMS client for the ARN's
// OWN region (never the candidate's primary region blindly - residue can be in either).
// Deletion is attempted ONLY for ARNs the caller passes in; this issues no Describe or
// List call of its own to go looking for more. Best-effort per ARN: one failure does not
// stop the rest, and the caller's re-verify tag query - not this function's return value
// - is what actually confirms what cleared.
//
// Every entry in residueDeleters is a REGIONAL resource type, and that is a contract,
// not a coincidence: the client is picked by the ARN's own region field, so a global
// type (an IAM-style ARN, whose region field is empty) would find no client under the
// "" key and be skipped with no signal at all. Registering one is therefore reported
// as a failure below rather than silently ignored - the day someone adds a global type
// here, the report says so instead of quietly doing nothing forever.
//
// failed counts ARNs that WERE eligible by type but did not get deleted: the delete
// call returned an error, or the ARN carries no region to route it by (the global-type
// case above). Kept deliberately separate from attempted
// (successes only) so the caller can tell "nothing here was eligible for a targeted
// delete" apart from "everything eligible was tried and every one of them failed" -
// those read identically to a caller that only checks attempted == 0, discarding the
// one diagnostic signal (that a delete was actually attempted and errored) an operator
// needs to tell a permissions/dependency problem apart from genuine, untouchable
// residue. Every failure is also logged here, matching every sibling reclaim helper in
// this file - it was the only one silently swallowing its error before this fix.
//
// These deletes run AFTER Sweep's wall-clock budget check for the candidate, so they
// are outside the budget by construction. That is deliberate and rests on an
// assumption worth stating: every registered deleter is a single control-plane API
// call that returns in well under a second (DMS DeleteEndpoint and
// DeleteReplicationSubnetGroup return as soon as the delete is accepted, they do not
// wait for the resource to disappear), and the ARN list is one candidate's own
// survivors - single digits in every case measured. Total time here is therefore noise
// against the ~90-minute Teardown the budget actually exists to bound. If a
// long-running or waiter-style deleter is ever added to residueDeleters, that
// assumption breaks and this loop needs its own bound.
func reclaimResidueByARN(ctx context.Context, dms map[string]DMSReclaimAPI, arns []string) (attempted, failed int, byType map[string]int) {
	for _, a := range arns {
		service, resourceType := arnResourceType(a)
		if service == "" {
			continue
		}
		deleter, ok := residueDeleters[resourceType]
		if !ok {
			continue
		}
		region := arnRegion(a)
		if region == "" {
			// A registered type whose ARN has no region: either a global-ARN type was
			// added to residueDeleters (which the registry's regional-only contract
			// forbids) or the ARN is malformed. Both used to fall into the client
			// lookup below and vanish as a silent `continue`.
			step("WARNING: residue reclaim skipped %s %s: the ARN carries no region, but residueDeleters is regional-only", resourceType, a)
			failed++
			continue
		}
		client, ok := dms[region]
		if !ok || client == nil {
			continue
		}
		if err := deleter(ctx, client, a); err != nil {
			step("WARNING: residue reclaim failed for %s %s: %v", resourceType, a, err)
			failed++
			continue
		}
		attempted++
		if byType == nil {
			byType = map[string]int{}
		}
		byType[resourceType]++
	}
	return attempted, failed, byType
}

// taggedResources is the full result of a tag-query enumeration - what countTagged
// collapses to (total, anchored) for classify()'s G8, plus the per-type breakdown
// Sweep needs to name specifically which resources survived a destroy (StateResidue).
// Keeping one enumeration behind both callers means the denylist/anchoring logic is
// written and tested in exactly one place instead of two loops that could quietly
// diverge.
type taggedResources struct {
	total    int
	anchored bool
	// byType counts surviving (post-denylist) resources by arnResourceType's
	// resourceType, falling back to "unknown" for an ARN that failed to parse -
	// countTagged's caller treats an unparseable ARN as "count it" for the same
	// fail-loud reason (see arnResourceType), so it needs a bucket here too.
	byType map[string]int
	// arns is every surviving resource's exact ARN, in page order. Sweep's residue
	// reclaim (reclaimResidueByARN) dispatches deletes against exactly these ARNs and
	// nothing else - it never issues its own Describe/List call to go looking for more,
	// so a targeted delete can only ever touch a resource this same tag query already
	// confirmed belongs to the candidate.
	arns []string
}

// countTaggedDetailed exhausts the paginator in every region and returns an error on
// any failure. Silent under-enumeration is the worst outcome available here: it makes
// the janitor declare a stack torn down when it is not, and then the leak is
// invisible forever (no manifest signal, no state, real infra still holding VPC
// quota).
//
// The query itself carries NO ResourceTypeFilters (defect 1's fix) - filtering happens
// after the fact, against each result's own ARN, so an unrecognized resource type is
// counted rather than silently dropped. anchored reports whether any counted resource is
// of a type OTHER than the ones in insufficientAloneTypes (defect 2's fix); the caller
// uses that to tell a genuinely standing stack apart from stale tagging-index residue.
func countTaggedDetailed(ctx context.Context, tags map[string]TagAPI, regions []string, customer string) (taggedResources, error) {
	var r taggedResources
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
			return taggedResources{}, fmt.Errorf("no tagging client configured for region %q", region)
		}
		paginator := resourcegroupstaggingapi.NewGetResourcesPaginator(client, &resourcegroupstaggingapi.GetResourcesInput{
			TagFilters: filters,
			// Deliberately no ResourceTypeFilters: see the comment above. Sending one
			// here is exactly the defect-1 bug.
		})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return taggedResources{}, fmt.Errorf("GetResources in %s: %w", region, err)
			}
			for _, res := range page.ResourceTagMappingList {
				service, resourceType := arnResourceType(aws.ToString(res.ResourceARN))
				if service != "" && isDeniedResourceType(service, resourceType) {
					continue
				}
				r.total++
				key := resourceType
				if service == "" {
					key = "unknown"
				}
				if r.byType == nil {
					r.byType = map[string]int{}
				}
				r.byType[key]++
				r.arns = append(r.arns, aws.ToString(res.ResourceARN))
				if service == "" || !insufficientAloneTypes[resourceType] {
					r.anchored = true
				}
			}
		}
	}
	if queried == 0 {
		return taggedResources{}, fmt.Errorf("no usable region on the manifest (regions=%v): refusing to report a stack as clean without querying anything", regions)
	}
	return r, nil
}

// countTagged is the (total, anchored) view classify() needs for G8. A thin wrapper
// over countTaggedDetailed - see that function for the actual query and filtering.
func countTagged(ctx context.Context, tags map[string]TagAPI, regions []string, customer string) (total int, anchored bool, err error) {
	r, err := countTaggedDetailed(ctx, tags, regions, customer)
	if err != nil {
		return 0, false, err
	}
	return r.total, r.anchored, nil
}

// formatByType renders a byType breakdown deterministically ("dms:endpoint x2,
// dms:subgrp x1"), sorted by key, for a human-readable residue reason. Empty input
// renders "", not a panic or a placeholder string, so a caller can safely inline it.
func formatByType(byType map[string]int) string {
	if len(byType) == 0 {
		return ""
	}
	keys := make([]string, 0, len(byType))
	for k := range byType {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s x%d", k, byType[k]))
	}
	return strings.Join(parts, ", ")
}

// listStateKeys returns every object key under prefix in bucket that ends in
// "/terraform.tfstate" - the same enumeration lockState needs for the lock check and
// clearStateDigests needs to compose exact digest item keys. Extracted so both callers
// share one S3 listing loop instead of two that could quietly diverge.
func listStateKeys(ctx context.Context, api S3ListGetAPI, bucket, prefix string) ([]string, error) {
	var stateKeys []string
	err := eachListPage(ctx, api, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix),
	}, func(out *s3.ListObjectsV2Output) {
		for _, obj := range out.Contents {
			if obj.Key != nil && strings.HasSuffix(*obj.Key, "/terraform.tfstate") {
				stateKeys = append(stateKeys, *obj.Key)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return stateKeys, nil
}

// lockInfo mirrors the JSON the Terraform/OpenTofu S3 backend stores in a lock item's
// Info attribute (statemgr.LockInfo): ID/Operation/Info/Who/Version/Created/Path. Only
// Created is used here. NOT verified against a live item - see the janitor rollout notes.
type lockInfo struct {
	Created string `json:"Created"`
}

// lockState reads the lock item for every terraform.tfstate under prefix, searched
// across every location in locs (not just one), and returns whether any is "fresh"
// (younger than fresh), the oldest lock's age (0 when there is no lock at all), and
// whether the freshness verdict came from a lock whose Info payload could not be read
// (corrupt). It lists state files under the prefix via S3 rather than assuming a fixed
// layout, because the physical/logical layer paths differ by env_path, AND because the
// bucket holding a run's state is not always the bucket its manifest was found in - see
// candidateBuckets. Searching every candidate bucket, not just the one the caller
// happened to resolve the manifest from, is what keeps a split-bucket run's lock
// visible here instead of silently reading back "no lock at all".
//
// locks is keyed by region and the item for a bucket's state is looked up through the
// client for THAT bucket's region: one table name, one table per region (see
// JanitorDeps.Locks). A region with no client configured is an error, not a skip - a
// lock we could not look at is exactly the thing that must not read back as "no lock".
func lockState(ctx context.Context, locks map[string]LockAPI, api S3ListGetAPI, table string, locs []stateLocation, prefix string, now time.Time, freshWithin time.Duration) (fresh bool, oldest time.Duration, corrupt bool, err error) {
	var oldestAge time.Duration
	found := false
	for _, loc := range locs {
		client, ok := locks[loc.Region]
		if !ok || client == nil {
			return false, 0, false, fmt.Errorf("no lock-table client configured for region %q (bucket %s)", loc.Region, loc.Bucket)
		}
		stateKeys, err := listStateKeys(ctx, api, loc.Bucket, prefix)
		if err != nil {
			return false, 0, false, fmt.Errorf("list state objects under %s in %s: %w", prefix, loc.Bucket, err)
		}
		for _, key := range stateKeys {
			lockID := loc.Bucket + "/" + key
			out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
				TableName: aws.String(table),
				Key: map[string]ddbtypes.AttributeValue{
					"LockID": &ddbtypes.AttributeValueMemberS{Value: lockID},
				},
			})
			if err != nil {
				return false, 0, false, fmt.Errorf("GetItem %s in %s: %w", lockID, loc.Region, err)
			}
			if len(out.Item) == 0 {
				continue // no lock held on this state file
			}
			infoAttr, ok := out.Item["Info"].(*ddbtypes.AttributeValueMemberS)
			if !ok || infoAttr.Value == "" {
				// A lock item with no readable Info: we cannot establish its age, so treat it
				// as fresh (fail closed) rather than assume it is safe to touch. corrupt=true
				// so the report can say WHY - "fresh" and "unreadable, assumed fresh" are the
				// same verdict but very different things for a human deciding what to do.
				return true, 0, true, nil
			}
			var li lockInfo
			if err := json.Unmarshal([]byte(infoAttr.Value), &li); err != nil || li.Created == "" {
				return true, 0, true, nil // same fail-closed reasoning
			}
			created, perr := time.Parse(time.RFC3339, li.Created)
			if perr != nil {
				return true, 0, true, nil
			}
			age := now.Sub(created)
			if age < freshWithin {
				return true, age, false, nil
			}
			if !found || age > oldestAge {
				oldestAge = age
				found = true
			}
		}
	}
	return false, oldestAge, false, nil
}

// clearStateDigests deletes the S3 backend's -md5 consistency-digest item for every
// state key under prefix, immediately before Sweep destroys the candidate.
// cleanup-orphans.sh does the equivalent (step 2) by scanning the WHOLE lock table for
// any LockID that CONTAINS the prefix and deleting it, ungated by DRY_RUN - both bugs
// this port must not carry: a substring scan can match a live, unrelated run's lock,
// and the bash delete has no report-mode guard at all. Neither is possible here by
// construction. There is no Scan and no substring match: every key comes from an exact
// S3 listing (listStateKeys), so the only DeleteItem this function can ever issue is
// for an ID it composed itself from a real object key. And report mode is enforced by
// placement, not a flag: this is only ever called from inside Sweep, on the path that
// requires o.Sweep == true - see the call site below and the package comment on that
// pattern.
//
// This never touches the plain LockID (the real mutex) - only the derived "<lockID>-md5"
// digest item. classify()'s G7 already guarantees a candidate reaches StateOrphan only
// when lockState found no lock at all (a present-but-stale lock routes to StateBlocked
// and is never swept), so there is nothing live under LockID here to force-release. The
// -md5 item is a separate, always-present consistency checksum the S3 backend keeps for
// every written state key, not a lock - clearing it just lets the destroy read the real
// S3 object instead of aborting on a stale checksum from an interrupted apply.
//
// Best-effort: returns the number of digests cleared and the first error encountered,
// but does not stop on an error - caller logs and continues rather than blocking a
// destroy attempt on a diagnostic side channel.
//
// Searches every location in locs, not just one - a split-bucket run (candidateBuckets)
// has its state, and therefore its digest items, in the DR bucket while its manifest
// (and c.Bucket) point at the primary one; clearing only c.Bucket would silently clear
// nothing for exactly the runs this function exists to unblock. Each location's deletes
// go through the digest client for THAT region: the table is per region (see
// JanitorDeps.Digests), and DeleteItem against a missing item succeeds, so a
// primary-only client would report a DR digest "cleared" and the destroy would still
// abort on it.
// digestItemID composes the "-md5" digest item id for a lock id. Composition and the
// safety assert are separate functions on purpose: composing right above the assert
// makes the assert unreachable from here (that is the point - it is belt and braces on
// top of the DigestAPI/LockAPI interface split, for a future refactor that changes how
// the id is built or passes one in from elsewhere), but a defense nothing can execute
// is also a defense nothing can prove. assertDigestID takes the finished id, so a test
// can hand it one that was composed wrong and watch it refuse.
func digestItemID(lockID string) (string, error) {
	return assertDigestID(lockID + "-md5")
}

// assertDigestID is the last gate before a DeleteItem: an id that does not end in
// "-md5" is a plain LockID, i.e. a real state mutex, and this package must never
// delete one of those unattended.
func assertDigestID(digestID string) (string, error) {
	if !strings.HasSuffix(digestID, "-md5") {
		return "", fmt.Errorf("refusing to delete a digest id not ending in -md5: %q", digestID)
	}
	return digestID, nil
}

func clearStateDigests(ctx context.Context, d JanitorDeps, o JanitorOptions, locs []stateLocation, prefix string) (int, error) {
	cleared := 0
	var firstErr error
	for _, loc := range locs {
		bucket := loc.Bucket
		client, ok := d.Digests[loc.Region]
		if !ok || client == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("no digest-table client configured for region %q (bucket %s)", loc.Region, bucket)
			}
			continue
		}
		stateKeys, err := listStateKeys(ctx, d.S3, bucket, prefix)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("list state objects under %s in %s: %w", prefix, bucket, err)
			}
			continue
		}
		for _, key := range stateKeys {
			digestID, aerr := digestItemID(bucket + "/" + key)
			if aerr != nil {
				if firstErr == nil {
					firstErr = aerr
				}
				continue
			}
			_, derr := client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(o.LockTable),
				Key: map[string]ddbtypes.AttributeValue{
					"LockID": &ddbtypes.AttributeValueMemberS{Value: digestID},
				},
			})
			if derr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("DeleteItem %s: %w", digestID, derr)
				}
				continue
			}
			cleared++
		}
	}
	return cleared, firstErr
}

// Janitor binds the dependencies needed to re-scan and sweep exactly one Reaper
// finding. ScanFunc and FinalVeto are injected seams for deterministic safety tests;
// production leaves ScanFunc nil and supplies a control-plane veto immediately before
// the existing Sweep path is allowed to mutate anything.
type Janitor struct {
	Deps            JanitorDeps
	Options         JanitorOptions
	Workflows       JanitorWorkflowList
	ScanFunc        func(context.Context, JanitorDeps, JanitorOptions, JanitorWorkflowList) (*Report, error)
	FinalVeto       func(context.Context, Candidate) error
	actionRequest   ReaperActionRequest
	actionFinalVeto func(context.Context, ReaperActionRequest, Candidate) error
}

func (janitor *Janitor) PrepareReaperAction(request ReaperActionRequest, veto func(context.Context, ReaperActionRequest, Candidate) error) {
	janitor.actionRequest = request
	janitor.actionFinalVeto = veto
}

func (janitor *Janitor) scan(ctx context.Context) (*Report, error) {
	if janitor.ScanFunc != nil {
		return janitor.ScanFunc(ctx, janitor.Deps, janitor.Options, janitor.Workflows)
	}
	return Scan(ctx, janitor.Deps, janitor.Options, janitor.Workflows)
}

// SweepSelected re-runs Scan, revalidates the current finding, and delegates one
// candidate to the existing bounded Sweep implementation. It never trusts the report
// that originally produced the Slack action.
func (janitor *Janitor) SweepSelected(ctx context.Context, output *Report, prefix, expectedVersion string) (Candidate, error) {
	fresh, err := janitor.scan(ctx)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: fresh janitor scan: %v", ErrRetryable, err)
	}
	cleanPrefix := strings.Trim(prefix, "/")
	var matches []Candidate
	for _, candidate := range fresh.Candidates {
		if strings.Trim(candidate.Prefix, "/") == cleanPrefix {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return Candidate{}, ErrAlreadyGone
	}
	if len(matches) != 1 {
		return Candidate{}, fmt.Errorf("%w: prefix %q matched %d candidates", ErrBlocked, prefix, len(matches))
	}
	candidate := matches[0]
	decision, _, err := decisionForCandidate(candidate)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	if decision == "already_gone" {
		return candidate, ErrAlreadyGone
	}
	if decision != "eligible" || candidate.State != StateOrphan || candidate.KeepOnFailure {
		return candidate, ErrBlocked
	}
	if FindingVersion(candidate, decision) != expectedVersion {
		return candidate, ErrStale
	}
	if err := guardProtected(candidate.Prefix, candidate.Identifier); err != nil {
		return candidate, fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	if janitor.FinalVeto != nil {
		if err := janitor.FinalVeto(ctx, candidate); err != nil {
			return candidate, err
		}
	}
	if janitor.actionFinalVeto != nil {
		if err := janitor.actionFinalVeto(ctx, janitor.actionRequest, candidate); err != nil {
			return candidate, err
		}
	}

	selected := &Report{
		SchemaVersion: fresh.SchemaVersion,
		Mode:          "sweep",
		At:            fresh.At,
		Account:       fresh.Account,
		Candidates:    []Candidate{candidate},
		Orphans:       1,
	}
	options := janitor.Options
	options.Sweep = true
	options.MaxSweeps = 1
	if err := Sweep(ctx, janitor.Deps, options, selected); err != nil {
		return selected.Candidates[0], err
	}
	if output != nil {
		*output = *selected
	}
	result := selected.Candidates[0]
	if strings.HasPrefix(result.SweepResult, "failed:") {
		return result, fmt.Errorf("%w: %s", ErrTeardown, result.SweepResult)
	}
	if result.State == StateOrphan && result.SweepResult != sweepResultDestroyed {
		return result, fmt.Errorf("%w: %s", ErrBlocked, result.SweepResult)
	}
	return result, nil
}

// Sweep destroys the orphans a prior Scan found, capped at MaxSweeps. It calls the
// existing PhaseParams.Teardown (via d.Teardown) with failed=true so the full diagnostics
// dump happens BEFORE the stack goes away: an orphan is evidence that a teardown failed,
// and that evidence is the point. It deliberately does not purge S3 state, the manifest,
// or the lock table. Purging state is the one irreversible step, and Teardown already
// leaves state behind for the same reason: state pointing at nothing is recoverable,
// infra pointing at no state is not.
func Sweep(ctx context.Context, d JanitorDeps, o JanitorOptions, rep *Report) error {
	if !o.Sweep {
		// The report/sweep gate must live here, not only at the one call site main.go
		// happens to have today. Every mutating call below (clearStateDigests, Teardown,
		// reclaimResidueByARN) is reachable only through this function, so this is the
		// actual enforcement point for "report mode never mutates anything" - a doc
		// claim this package makes in more than one place. Before this check existed,
		// that claim was only true because the sole caller happened to gate it; any
		// future caller that skipped the gate would have swept for real under a
		// report-mode flag.
		return fmt.Errorf("janitor: Sweep called with o.Sweep=false; report mode must never reach Sweep")
	}
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
	// The wall-clock companion to maxFailures above. maxFailures bounds the number of
	// BAD attempts; nothing before this bounded the number of SLOW ones, and Teardown
	// has no internal timeout of its own (tg.Destroy takes no context) - the pod's
	// activeDeadlineSeconds is the only thing that would ever stop a hung or simply
	// slow destroy, and killing a Teardown mid-destroy strands infra worse than
	// leaving it reported as an orphan (same reasoning as the maxFailures skip below).
	// budget is checked BEFORE every attempt starts, never mid-attempt (there is no
	// way to interrupt tg.Destroy once called) - see DefaultSweepBudget for how it is
	// derived from the pod deadline with margin for everything that runs before Sweep
	// is even called.
	budget := o.sweepBudget()
	start := o.now()
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
		if neverSweepConfigNames[c.ConfigName] {
			// G12 again, at destroy time, same defense-in-depth shape as the
			// keep-on-failure check above: classify() routes a recovery-drill config to
			// StateNeedsReview long before it could reach here, so this only fires on a
			// hand-built Report or a future refactor that flips a State back to Orphan.
			// A skip rather than an error - see neverSweepConfigNames for why a drill
			// config must never take the whole cycle down.
			c.SweepResult = "skipped: recovery drill config, never sweepable"
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
		// anyone else. StateResidue candidates (below) never increment rep.Swept
		// either, for the same reason: a candidate Sweep already knows it cannot fix
		// must never be the thing that eats the one real orphan's turn.
		if rep.Swept >= o.sweepMax() {
			// Say so, like every other skip path here does. A cap-skipped orphan with an
			// empty SweepResult is indistinguishable in the report (and in Slack) from
			// one the loop never reached at all.
			c.SweepResult = "skipped: max-sweeps cap met"
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
		if budget <= 0 {
			// The pod deadline was already spent before Sweep began (a long image pull,
			// a slow clone, a Scan that ran long). Starting even the first destroy here
			// means starting one that cannot finish: the pod is killed mid-teardown and
			// the stack is left in a worse state than the reported orphan it was. Record
			// it per candidate rather than returning early so the report - and the Slack
			// card built from it - says explicitly that this cycle swept nothing and why.
			c.SweepResult = "skipped: pod deadline exhausted before the sweep began; no time left to start a destroy that could finish"
			continue
		}
		if elapsed := o.now().Sub(start); elapsed >= budget {
			// The wall-clock bound. Everything already attempted this cycle (however
			// many successes, failures, or residue outcomes) ran long enough to use up
			// the time this cycle can safely spend inside Sweep - starting one more
			// risks the pod deadline killing it mid-destroy. Every skipped candidate
			// here is still StateOrphan and gets picked up again next cycle, same as
			// the maxFailures skip above.
			c.SweepResult = fmt.Sprintf("skipped: sweep wall-clock budget (%s) exhausted after %s", budget, elapsed.Round(time.Second))
			continue
		}
		// G3, second call site. The gate runs again immediately before the destroy, on
		// the same values, because between Scan and here is exactly where a refactor
		// would otherwise be free to lose it.
		if perr := guardProtected(c.Prefix, c.Identifier); perr != nil {
			return perr
		}
		// No recorded customer, no destroy. classify() cannot produce this shape today
		// (G8 requires a non-empty customer before StateOrphan is reachable), so in
		// practice it only appears on a hand-built Report or after a future refactor -
		// the same class of caller the keep-on-failure and guardProtected checks above
		// exist for. It used to destroy anyway and skip the post-destroy re-verify,
		// which made this the one fail-OPEN branch in a file that is fail-closed
		// everywhere else: a destroy nothing could verify, counted as a clean success.
		// Skip it instead - a candidate whose identity we cannot re-query is a candidate
		// we cannot prove anything about afterwards.
		//
		// State stays StateOrphan on purpose. The refusal belongs in SweepResult and
		// Reason, not in a state change: the notify script's jq selector matches
		// orphan/blocked/residue only, so moving this one to needs-review would delete
		// the single candidate the janitor explicitly refused to touch from the Slack
		// alert, and the post-sweep orphan recount below would stop counting it too. The
		// whole point of this branch is that a human has to look at it, so it has to stay
		// in the set a human is shown. SweepResult is not sweepResultDestroyed, so the
		// recount keeps it in rep.Orphans.
		if c.Customer == "" {
			c.Reason = "refused: no customer identity recorded on this candidate, so a post-destroy verification query is impossible; needs review before anything destroys something whose result cannot be checked"
			c.SweepResult = "refused: no customer identity recorded; needs review"
			continue
		}
		p := PhaseParams{
			RepoDir: o.RepoDir, Matrix: d.Matrix,
			Store:      NewS3Store(d.S3, c.Bucket),
			ConfigName: c.ConfigName, RunID: c.RunID,
			AccountID: o.AccountID, Profile: o.Profile, Region: o.Region,
			// The guard just above checked c.Identifier, not whatever Teardown would
			// recompute fresh from today's matrix - force them to be the same value so
			// every out-of-band mutation Teardown makes (NLB, MSK, container-insights,
			// category-B reclaim) acts on the identity that was actually guarded. See
			// PhaseParams.IdentifierOverride.
			IdentifierOverride: c.Identifier,
		}
		if len(d.Digests) > 0 {
			// c.Region/c.DRRegion are the candidate's OWN recorded region split (from the
			// manifest), not o.Region/o.DRRegion - same reasoning as classify's G7 lock
			// check: a split-bucket run's state digests live in a bucket c.Bucket (the
			// manifest's bucket) may not be, and in the DR region's copy of the lock
			// table rather than the primary one.
			digestBuckets := candidateBuckets(o.AccountID, c.Region, c.DRRegion)
			if n, derr := clearStateDigests(ctx, d, o, digestBuckets, c.Prefix); derr != nil {
				step("JANITOR: could not clear state digests for %s (non-fatal, destroy may abort on a stale digest): %v", c.Prefix, derr)
			} else if n > 0 {
				step("JANITOR: cleared %d stale state digest(s) under %s", n, c.Prefix)
			}
		}
		step("JANITOR sweep: %s (identifier %s, %d tagged resources still live)", c.Prefix, c.Identifier, c.Resources)
		if err := d.Teardown(ctx, p, true); err != nil {
			c.SweepResult = "failed: " + err.Error()
			rep.Failed++
			failedAttempts++
			continue
		}
		// Teardown succeeded, but "succeeded" only means terragrunt destroy removed
		// everything ITS state still tracked. Re-query the same tag filter classify()
		// used to build this candidate: if it still turns up real (anchored) results,
		// those resources are orphaned FROM state - created outside a tracked
		// resource block, or already state-rm'd by a run killed mid-teardown before
		// this cycle ever started (smoke4879-bi's DMS endpoints in the measured
		// baseline are exactly this shape: VPC and RDS gone, meaning a destroy DID
		// run at some point, but 2 endpoints and a subnet group were never in state
		// to remove). Retrying Teardown cannot reach them either, so this is not a
		// failure to retry - see StateResidue for why it gets its own state instead
		// of either "destroyed" or "failed".
		//
		// c.Customer is guaranteed non-empty here: the guard above this destroy routes
		// a candidate without one to NeedsReview rather than letting it through, so the
		// re-verify below always has an identity to query.
		r, terr := countTaggedDetailed(ctx, d.Tags, []string{c.Region, c.DRRegion}, c.Customer)
		switch {
		case terr != nil:
			// Inconclusive, not success: a query we could not complete must never
			// be reported as "destroyed" on trust. Fail closed to Unknown, same
			// posture as every other inconclusive AWS call in this file.
			c.State = StateUnknown
			c.SweepResult = "destroy ran, but the post-destroy verification query failed: " + terr.Error()
			rep.Inconclusive++
		case r.total > 0 && r.anchored:
			c.State = StateResidue
			c.Resources = r.total
			// Category C: the tag-confirmed survivors. Attempt a targeted, single-call
			// delete for exactly the ARNs this query returned (never a wider search -
			// see reclaimResidueByARN), then re-verify with the identical query before
			// saying anything cleared. State never promotes past StateResidue here even
			// on a full clear - a candidate this function already knows it could not
			// fully fix on the first try does not get to claim "destroyed" without a
			// clean re-verify proving it, matching the fail-closed posture everywhere
			// else in this file.
			attempted, failedReclaims, attemptedByType := reclaimResidueByARN(ctx, d.DMS, r.arns)
			switch {
			case attempted == 0 && failedReclaims == 0:
				// Nothing here was even eligible for a targeted delete (no registered
				// deleter for its type, or no DMS client configured for its region) -
				// distinct from the case below, where deletes WERE tried and every one
				// of them errored. Conflating the two throws away the one diagnostic
				// signal (an actual AWS error, already logged by reclaimResidueByARN)
				// an operator needs to tell "genuinely untouchable" apart from "a
				// permissions or dependency problem worth investigating".
				c.Reason = fmt.Sprintf(
					"terraform destroy completed, but %d tagged resources survived (%s): orphaned from state, not reachable by retrying Teardown; needs manual cleanup%s",
					r.total, formatByType(r.byType), residueIndexLagCaveat)
				c.SweepResult = "residue: needs manual cleanup"
				rep.Residue++
			case attempted == 0 && failedReclaims > 0:
				c.Reason = fmt.Sprintf(
					"terraform destroy completed; %d targeted delete(s) failed or could not be attempted (%s) - see the pod log for the AWS error(s); needs manual cleanup%s",
					failedReclaims, formatByType(r.byType), residueIndexLagCaveat)
				c.SweepResult = fmt.Sprintf("residue: %d targeted delete(s), none succeeded", failedReclaims)
				rep.Residue++
			default:
				r2, terr2 := countTaggedDetailed(ctx, d.Tags, []string{c.Region, c.DRRegion}, c.Customer)
				if terr2 != nil {
					// Same fail-closed reasoning as the outer terr case: a re-verify
					// query we could not complete must never be read as "it worked".
					c.State = StateUnknown
					c.SweepResult = fmt.Sprintf("residue: attempted %d targeted delete(s) (%s), but the re-verify query failed: %v", attempted, formatByType(attemptedByType), terr2)
					rep.Inconclusive++
					break
				}
				// The mixed outcome: some targeted deletes worked, some errored. The
				// failed count has to travel with it. This function's own contract
				// (reclaimResidueByARN) calls that count the one signal separating a
				// permissions or dependency problem from genuinely untouchable residue,
				// and reporting only what was attempted hides it on exactly the cycle
				// where both things happened at once.
				failedNote := ""
				if failedReclaims > 0 {
					failedNote = fmt.Sprintf("; %d targeted delete(s) FAILED - see the pod log for the AWS error(s)", failedReclaims)
				}
				c.Resources = r2.total
				c.Reason = fmt.Sprintf(
					"terraform destroy completed; attempted %d targeted delete(s) (%s)%s; %d tagged resource(s) still standing (%s): needs manual cleanup%s",
					attempted, formatByType(attemptedByType), failedNote, r2.total, formatByType(r2.byType), residueIndexLagCaveat)
				c.SweepResult = fmt.Sprintf("residue: attempted %d targeted delete(s) (%s)%s, %d remain", attempted, formatByType(attemptedByType), failedNote, r2.total)
				rep.Residue++
			}
		default:
			// n==0, or the only survivors are insufficient-alone types (stale
			// tagging-index residue, defect 2) - a genuinely clean destroy.
			c.SweepResult = sweepResultDestroyed
			rep.Swept++
		}
	}
	// Recompute the headline from the FINAL states. Scan's count is pre-sweep, and by
	// here candidates have moved to residue/unknown/needs-review or been destroyed, so
	// leaving the old number in place would publish a JSON report whose orphans= field
	// contradicts its own candidate list. A destroyed candidate keeps State=Orphan on
	// purpose (Sweep only rewrites State when the outcome was NOT a clean destroy, so
	// the report still says what it was), which is why the count reads SweepResult too:
	// orphans= means "orphans still standing at the end of this cycle".
	rep.Orphans = 0
	for _, c := range rep.Candidates {
		if c.State == StateOrphan && c.SweepResult != sweepResultDestroyed {
			rep.Orphans++
		}
	}
	return nil
}

// JanitorReportPrefix is where sweep reports are archived in the primary state bucket.
// A prefix of its own, well away from the run prefixes classify() enumerates: a top
// level "directory" with no harness-manifest.json under it is not a candidate (G4), so
// the janitor's own artifacts can never become something it reasons about.
const JanitorReportPrefix = "janitor-reports/"

// WriteSweepReport archives the report JSON in the primary state bucket. Until this
// existed the only record that the janitor destroyed something was the Argo pod log
// (short retention) and a Slack message (editable, and gone from anyone's scrollback
// in a week), so reconstructing a wrong destroy days later was not possible. The
// bucket is versioned and already the janitor's own data store, which makes it the
// cheapest durable place to put this.
//
// Sweep cycles only. A report-only cycle mutates nothing and its report is
// reconstructible by re-running the scan, so archiving those would just be noise (and
// a write from a mode that is supposed to be side-effect free). Called on the safety
// abort path too: a cycle that stopped mid-sweep is exactly when the record matters.
func WriteSweepReport(ctx context.Context, d JanitorDeps, o JanitorOptions, rep *Report) (string, error) {
	if d.S3 == nil {
		return "", fmt.Errorf("no S3 client wired")
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}
	// Name it after the janitor workflow when there is one (that is the run id a human
	// searching Argo already has) and fall back to the timestamp alone otherwise. The
	// timestamp leads either way so a plain S3 listing sorts chronologically.
	name := o.now().UTC().Format("20060102T150405Z")
	if o.SelfWorkflow != "" {
		name += "-" + o.SelfWorkflow
	}
	key := JanitorReportPrefix + name + ".json"
	bucket := stateBucket(o.AccountID, o.Region)
	if _, err := d.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	}); err != nil {
		return "", fmt.Errorf("put %s/%s: %w", bucket, key, err)
	}
	step("JANITOR: sweep report archived at s3://%s/%s", bucket, key)
	return key, nil
}

// RealTeardown wires JanitorDeps.Teardown to the actual PhaseParams.Teardown for
// production use (cmd/harness). Tests supply their own func instead.
//
// keepOnFailure is hardcoded false, and that is not a lost parameter: keep-on-failure
// is a decision about the RUN that failed, and it has already been honored long before
// this point - classify() routes a keep-on-failure candidate to StateKept (never
// sweepable), and Sweep re-checks c.KeepOnFailure immediately before the destroy.
// Anything that reaches here has passed both, so passing true would mean "keep the
// stack" on a candidate the janitor exists specifically to remove: the sweep would
// silently no-op forever. failed=true is passed instead so the full diagnostics dump
// runs before the stack goes away.
func RealTeardown(ctx context.Context, p PhaseParams, failed bool) error {
	return p.Teardown(ctx, false, failed)
}
