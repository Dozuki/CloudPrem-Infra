package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ---- fakes ----

// fakeJanitorS3 is an in-memory, bucket-aware S3 double covering GetObject/PutObject
// (the manifest store) and ListObjectsV2 (prefix enumeration + the lock check's state-key
// listing). Bucket-aware because Scan queries the primary and DR buckets independently -
// a bucket-blind fake would make a primary-only object visible from the DR bucket too and
// hide bugs a real two-bucket account can hit.
type fakeJanitorS3 struct {
	buckets map[string]map[string][]byte
	listErr error // when set, ListObjectsV2 always fails - simulates a listing outage
}

func newFakeJanitorS3() *fakeJanitorS3 {
	return &fakeJanitorS3{buckets: map[string]map[string][]byte{}}
}

func (f *fakeJanitorS3) bucket(name string) map[string][]byte {
	b, ok := f.buckets[name]
	if !ok {
		b = map[string][]byte{}
		f.buckets[name] = b
	}
	return b
}

func (f *fakeJanitorS3) put(bucket, key, body string) { f.bucket(bucket)[key] = []byte(body) }

func (f *fakeJanitorS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.bucket(*in.Bucket)[*in.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeJanitorS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.bucket(*in.Bucket)[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeJanitorS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	prefix := aws.ToString(in.Prefix)
	delim := aws.ToString(in.Delimiter)
	out := &s3.ListObjectsV2Output{}
	seenCommon := map[string]bool{}
	var keys []string
	for k := range f.bucket(*in.Bucket) {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		if delim != "" {
			if idx := strings.Index(rest, delim); idx >= 0 {
				cp := prefix + rest[:idx+len(delim)]
				if !seenCommon[cp] {
					seenCommon[cp] = true
					out.CommonPrefixes = append(out.CommonPrefixes, s3types.CommonPrefix{Prefix: aws.String(cp)})
				}
				continue
			}
		}
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	return out, nil
}

// fakeTagAPI answers GetResources with a fixed resource count (or a fixed error, to model
// a paginator failure). byType models a per-resource-type inventory (e.g. "kms:key",
// "eks:cluster") and builds a real-shaped ARN for each entry
// ("arn:aws:<service>:<region>:<account>:<resourcetype>/<id>") so a test exercises the
// SAME ARN parsing (arnResourceType) production code runs - a fake that filtered on a
// flat type string internally would keep passing even if arnResourceType itself broke.
type fakeTagAPI struct {
	resources int
	err       error
	byType    map[string]int
	// sawResourceTypeFilter flips true if ANY call ever carried a non-empty
	// ResourceTypeFilters, across every call this fake has seen. countTagged must never
	// set one (defect 1: an allowlist there is the exact bug) - post-hoc filtering on
	// the ARN only works if the query itself asks for everything.
	sawResourceTypeFilter bool
	calls                 int
}

func (f *fakeTagAPI) GetResources(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	f.calls++
	if len(in.ResourceTypeFilters) > 0 {
		f.sawResourceTypeFilter = true
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.byType != nil {
		var list []rgtypes.ResourceTagMapping
		n := 0
		for typ, count := range f.byType {
			service, rtype := typ, typ
			if idx := strings.Index(typ, ":"); idx >= 0 {
				service, rtype = typ[:idx], typ[idx+1:]
			}
			for i := 0; i < count; i++ {
				arn := "arn:aws:" + service + ":us-east-1:123456789012:" + rtype + "/res-" + string(rune('a'+n))
				list = append(list, rgtypes.ResourceTagMapping{ResourceARN: aws.String(arn)})
				n++
			}
		}
		return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: list}, nil
	}
	var list []rgtypes.ResourceTagMapping
	for i := 0; i < f.resources; i++ {
		list = append(list, rgtypes.ResourceTagMapping{ResourceARN: aws.String("arn:aws:ec2:us-east-1:123456789012:instance/i-" + string(rune('a'+i)))})
	}
	return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: list}, nil
}

// fakeLockAPI answers GetItem from a map keyed by LockID ("<bucket>/<key>"); a missing
// key means no lock is held, exactly like a real empty GetItem response (no error).
type fakeLockAPI struct {
	items map[string]map[string]ddbtypes.AttributeValue
}

func newFakeLockAPI() *fakeLockAPI {
	return &fakeLockAPI{items: map[string]map[string]ddbtypes.AttributeValue{}}
}

func (f *fakeLockAPI) setLock(lockID string, created time.Time) {
	body, _ := json.Marshal(lockInfo{Created: created.UTC().Format(time.RFC3339Nano)})
	f.items[lockID] = map[string]ddbtypes.AttributeValue{
		"LockID": &ddbtypes.AttributeValueMemberS{Value: lockID},
		"Info":   &ddbtypes.AttributeValueMemberS{Value: string(body)},
	}
}

func (f *fakeLockAPI) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	id := in.Key["LockID"].(*ddbtypes.AttributeValueMemberS).Value
	item, ok := f.items[id]
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

// ---- shared fixtures ----

const (
	testAccount = "076248559428"
	testRegion  = "us-east-1"
	testDR      = "us-west-2"
)

func testMatrix() *Matrix {
	return &Matrix{
		Defaults: Defaults{Region: testRegion, DRRegion: testDR, ReaperTTLHours: 24},
		Configs: []Config{
			{Name: "min_default", Env: "min", FeatureFlags: map[string]interface{}{"customer": "smoke"}},
			{Name: "recover", Env: "min", Region: testDR, FeatureFlags: map[string]interface{}{"customer": "smokerec"}},
		},
	}
}

// testOptions defaults Sweep to true. Scan tests never call Sweep at all, and every
// test that does call Sweep is testing what Sweep actually does when invoked for real
// (matching how main.go only ever calls Sweep behind --sweep); the one test that cares
// about the o.Sweep=false refusal (TestSweepRefusesToRunWhenSweepIsFalse) sets it back
// to false explicitly.
func testOptions(now time.Time) JanitorOptions {
	return JanitorOptions{
		AccountID: testAccount, Region: testRegion, DRRegion: testDR,
		LockTable: "dozuki-terraform-lock",
		Grace:     6 * time.Hour, LockFresh: 4 * time.Hour,
		MaxSweeps: 1,
		Sweep:     true,
		Now:       func() time.Time { return now },
	}
}

func primaryBucket() string { return stateBucket(testAccount, testRegion) }
func drBucket() string      { return stateBucket(testAccount, testDR) }

// seedManifest writes a manifest at prefix in bucket via the real S3Store, so the key
// shape matches exactly what production code writes and janitor.go's classify() reads.
func seedManifest(t *testing.T, f *fakeJanitorS3, bucket, prefix string, rm *RunManifest) {
	t.Helper()
	store := NewS3Store(f, bucket)
	if err := store.Save(context.Background(), prefix, rm); err != nil {
		t.Fatalf("seed manifest at %s/%s: %v", bucket, prefix, err)
	}
}

// appliedCustomerFor computes the exact salted customer value production code would
// (Config.Salted, config.go) for a given config+runID against testMatrix(). Used to
// seed a test manifest's AppliedCustomer so it matches what a real Provision would
// have written for a normal, non-drifted run - i.e. a well-formed POST-fix manifest,
// as opposed to the pre-fix (field-absent) or drifted (deliberately different)
// manifests the P2 regression tests construct on purpose.
func appliedCustomerFor(t *testing.T, cfgName, runID string) string {
	t.Helper()
	cfg, err := testMatrix().Config(cfgName)
	if err != nil {
		t.Fatalf("appliedCustomerFor: %v", err)
	}
	salted := cfg.Salted(runID)
	customer, _ := salted.FeatureFlags["customer"].(string)
	return customer
}

func baseDeps(f *fakeJanitorS3, tagResources int, tagErr error) JanitorDeps {
	tag := &fakeTagAPI{resources: tagResources, err: tagErr}
	return JanitorDeps{
		S3:       f,
		Tags:     map[string]TagAPI{testRegion: tag, testDR: tag},
		Locks:    newFakeLockAPI(),
		Matrix:   testMatrix(),
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
}

// ---- guardProtected ----

func TestGuardProtected(t *testing.T) {
	cases := []struct {
		name       string
		prefix     string
		identifier string
		wantErr    bool
	}{
		{"ordinary run", "run1-min_default/", "smokeab12-min", false},
		{"standard prefix", "standard/x/", "smoke-min", true},
		{"templates prefix", "_templates/x/", "smoke-min", true},
		{"global prefix", "_global/x/", "smoke-min", true},
		{"dev-min identifier", "run1-min_default/", "dev-min", true},
		{"dozuki-min identifier", "run1-min_default/", "dozuki-min", true},
		{"min-min identifier", "run1-min_default/", "min-min", true},
		{"empty prefix", "", "smoke-min", true},
		{"prefix without trailing slash", "run1-min_default", "smoke-min", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardProtected(c.prefix, c.identifier)
			if c.wantErr && !errors.Is(err, ErrProtected) {
				t.Fatalf("guardProtected(%q, %q) = %v, want ErrProtected", c.prefix, c.identifier, err)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("guardProtected(%q, %q) = %v, want nil", c.prefix, c.identifier, err)
			}
		})
	}
}

// ---- structural candidacy (dev-min case) ----

func TestScanNoManifestIsNotACandidate(t *testing.T) {
	f := newFakeJanitorS3()
	// dev-min's physical state lives at the unprefixed top level, with no
	// harness-manifest.json anywhere under "standard/". Only S3Store.Save ever writes
	// that object name, and nothing here does.
	f.put(primaryBucket(), "standard/us-east-1/min/physical/terraform.tfstate", "{}")

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 0, nil)
	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(rep.Candidates) != 0 {
		t.Fatalf("expected no candidates, got %+v", rep.Candidates)
	}
}

// ---- G3: protected identity aborts the whole cycle ----

func TestClassifyAbortsOnProtectedPrefix(t *testing.T) {
	// A real top-level common prefix can never structurally start with "standard/" AND
	// also match RunID-ConfigName/ for a real config (statePrefix is always exactly one
	// path segment, so a manifest can never land two levels deep under "standard/"). That
	// is exactly why this is a direct classify() test rather than a Scan() one: it proves
	// guardProtected fires even if some future change to prefix construction ever made
	// such a candidate reachable, without requiring today's S3 layout to produce one.
	f := newFakeJanitorS3()
	seedManifest(t, f, primaryBucket(), "standard/run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: "2020-01-01T00:00:00Z", Region: testRegion, DRRegion: testDR,
	})
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 1, nil)
	_, err := classify(context.Background(), deps, testOptions(now), primaryBucket(), "standard/run1-min_default/", now, map[string]string{}, map[string]JanitorWorkflow{})
	if !errors.Is(err, ErrProtected) {
		t.Fatalf("classify err = %v, want ErrProtected", err)
	}
}

func TestScanAbortsOnProtectedIdentifier(t *testing.T) {
	f := newFakeJanitorS3()
	// An empty run id makes Salted a documented no-op (config.go), so the identifier is
	// exactly "<base customer>-<env>" with no salt. customer=dev + env=min reproduces the
	// protected "dev-min" identifier without needing to reverse-engineer a SHA prefix.
	m := testMatrix()
	m.Configs = append(m.Configs, Config{Name: "min_default2", Env: "min", FeatureFlags: map[string]interface{}{"customer": "dev"}})
	seedManifest(t, f, primaryBucket(), "-min_default2/", &RunManifest{
		ConfigName: "min_default2", DeleteAfter: "2020-01-01T00:00:00Z",
	})

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 1, nil)
	deps.Matrix = m
	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if !errors.Is(err, ErrProtected) {
		t.Fatalf("Scan err = %v, want ErrProtected", err)
	}
	if rep != nil {
		t.Fatalf("expected no report on an aborted cycle, got %+v", rep)
	}
}

// ---- G6: ownership ----

func TestOwnershipByWorkflowName(t *testing.T) {
	f := newFakeJanitorS3()
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: "2020-01-01T00:00:00Z", // deep in the past; ownership must still win
	})
	wf := JanitorWorkflowList{Items: []JanitorWorkflow{
		{Metadata: WorkflowMetadata{Name: "run1"}, Status: WorkflowStatus{Phase: "Running"}},
	}}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 5, nil)
	rep, err := Scan(context.Background(), deps, testOptions(now), wf)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateActive {
		t.Fatalf("state = %q, want active; reason=%q", c.State, c.Reason)
	}
}

// wfWithParam builds a JanitorWorkflow with a single spec.arguments.parameters entry,
// avoiding an unwieldy anonymous-struct literal at every call site.
func wfWithParam(name, phase, paramName, paramValue string) JanitorWorkflow {
	var w JanitorWorkflow
	w.Metadata = WorkflowMetadata{Name: name}
	w.Status = WorkflowStatus{Phase: phase}
	w.Spec.Arguments.Parameters = append(w.Spec.Arguments.Parameters, struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}{Name: paramName, Value: paramValue})
	return w
}

func TestOwnershipByRunIDParameter(t *testing.T) {
	f := newFakeJanitorS3()
	seedManifest(t, f, primaryBucket(), "run2-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: "2020-01-01T00:00:00Z",
	})
	wf := JanitorWorkflowList{Items: []JanitorWorkflow{
		wfWithParam("harness-scenario-abc123", "Pending", "run-id", "run2"),
	}}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 5, nil)
	rep, err := Scan(context.Background(), deps, testOptions(now), wf)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run2-min_default/")
	if c.State != StateActive {
		t.Fatalf("state = %q, want active; reason=%q", c.State, c.Reason)
	}
}

func TestOwnershipByEmptyPhaseIsActive(t *testing.T) {
	f := newFakeJanitorS3()
	seedManifest(t, f, primaryBucket(), "run3-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: "2020-01-01T00:00:00Z",
	})
	wf := JanitorWorkflowList{Items: []JanitorWorkflow{
		{Metadata: WorkflowMetadata{Name: "run3"}, Status: WorkflowStatus{Phase: ""}},
	}}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 5, nil)
	rep, err := Scan(context.Background(), deps, testOptions(now), wf)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run3-min_default/")
	if c.State != StateActive {
		t.Fatalf("state = %q, want active (unstamped phase must read as live); reason=%q", c.State, c.Reason)
	}
}

// ---- G5: staleness ----

func TestStalenessBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		deleteAfter time.Time
		resources   int
		want        CandidateState
	}{
		{"inside ttl", now.Add(1 * time.Hour), 5, StateActive},
		{"past ttl inside grace", now.Add(-1 * time.Hour), 5, StatePending}, // grace=6h
		{"past ttl+grace, no resources", now.Add(-10 * time.Hour), 0, StateClean},
		{"past ttl+grace, resources live", now.Add(-10 * time.Hour), 3, StateOrphan},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeJanitorS3()
			seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
				ConfigName: "min_default", DeleteAfter: c.deleteAfter.Format(time.RFC3339),
				Region: testRegion, DRRegion: testDR,
				AppliedCustomer: appliedCustomerFor(t, "min_default", "run1"),
			})
			deps := baseDeps(f, c.resources, nil)
			rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			got := mustCandidate(t, rep, "run1-min_default/")
			if got.State != c.want {
				t.Fatalf("state = %q, want %q; reason=%q", got.State, c.want, got.Reason)
			}
		})
	}
}

// ---- G7: lock freshness ----

func TestFreshLockIsActive(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, // production manifests always carry this (loadOrInitManifest); the
		// lock check now searches candidateBuckets(m.Region, m.DRRegion), not just the bucket
		// the manifest happened to be found in, so a real region must be present here too.
	})
	key := "run1-min_default/standard/us-east-1/min/physical/terraform.tfstate"
	f.put(primaryBucket(), key, "{}")
	deps := baseDeps(f, 5, nil)
	lock := deps.Locks.(*fakeLockAPI)
	lock.setLock(primaryBucket()+"/"+key, now.Add(-1*time.Hour)) // fresh: LockFresh default is 4h

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateActive {
		t.Fatalf("state = %q, want active (fresh lock); reason=%q", c.State, c.Reason)
	}
}

func TestStaleLockOnOrphanIsBlockedAndSkippedBySweep(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer: appliedCustomerFor(t, "min_default", "run1"),
	})
	key := "run1-min_default/standard/us-east-1/min/physical/terraform.tfstate"
	f.put(primaryBucket(), key, "{}")
	deps := baseDeps(f, 5, nil)
	lock := deps.Locks.(*fakeLockAPI)
	lock.setLock(primaryBucket()+"/"+key, now.Add(-24*time.Hour)) // well past LockFresh (4h)

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateBlocked {
		t.Fatalf("state = %q, want blocked; reason=%q", c.State, c.Reason)
	}
	if c.LockAge == "" {
		t.Fatalf("expected LockAge to be set on a blocked candidate")
	}

	opts := testOptions(now)
	opts.Sweep = true
	swept := &teardownRecorder{}
	deps.Teardown = swept.teardown
	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept.calls != 0 {
		t.Fatalf("Sweep called Teardown %d times, want 0 (blocked must never be swept)", swept.calls)
	}
	if rep.Swept != 0 || rep.Failed != 0 {
		t.Fatalf("Swept=%d Failed=%d, want 0/0", rep.Swept, rep.Failed)
	}
}

// ---- G7: split-bucket lock visibility ----

// TestLockStateFindsAFreshLockInASecondBucket pins the fix for a real gap: a run's
// manifest and its actual terraform state do not always share a bucket (the recovery
// rebuild keeps its manifest in the primary bucket and its state in the DR one -
// candidateBuckets). Before this fix, classify's G7 lock check searched only the
// bucket a prefix's manifest happened to be found in, so a split-bucket run's lock -
// living in the OTHER bucket - was invisible: lockState found zero state keys there and
// silently reported "no lock at all", the permissive answer, for a stack that was
// actively being applied. Searching every candidate bucket closes that.
func TestLockStateFindsAFreshLockInASecondBucket(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	prefix := "run1-recover/"
	key := prefix + "standard/us-west-2/min/physical/terraform.tfstate"
	// State (and therefore the lock) lives ONLY in the DR bucket.
	f.put(drBucket(), key, "{}")
	locks := newFakeLockAPI()
	locks.setLock(drBucket()+"/"+key, now.Add(-1*time.Minute)) // fresh: well under LockFresh

	buckets := candidateBuckets(testAccount, testRegion, testDR)
	if len(buckets) != 2 {
		t.Fatalf("candidateBuckets = %v, want 2 buckets (primary + DR)", buckets)
	}
	fresh, _, err := lockState(context.Background(), locks, f, "dozuki-terraform-lock", buckets, prefix, now, 4*time.Hour)
	if err != nil {
		t.Fatalf("lockState: %v", err)
	}
	if !fresh {
		t.Fatalf("lockState fresh = false, want true (a fresh lock exists, just not in the first bucket searched)")
	}
}

// TestLockStateSingleBucketOnlySearchesThatBucket is the control for the test above:
// with only the primary bucket in play, a lock sitting in the (unsearched) DR bucket
// must not be found - this is not "search everywhere", it is "search every bucket the
// run's own recorded region split says is in play".
func TestLockStateSingleBucketOnlySearchesThatBucket(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	prefix := "run1-min_default/"
	key := prefix + "standard/us-east-1/min/physical/terraform.tfstate"
	f.put(drBucket(), key, "{}") // deliberately NOT in the bucket being searched
	locks := newFakeLockAPI()
	locks.setLock(drBucket()+"/"+key, now.Add(-1*time.Minute))

	fresh, age, err := lockState(context.Background(), locks, f, "dozuki-terraform-lock", []string{primaryBucket()}, prefix, now, 4*time.Hour)
	if err != nil {
		t.Fatalf("lockState: %v", err)
	}
	if fresh || age != 0 {
		t.Fatalf("fresh=%v age=%v, want false/0 (the lock is in a bucket not being searched)", fresh, age)
	}
}

// TestClearStateDigestsSearchesEveryCandidateBucket is clearStateDigests' analogue of
// the lockState fix above: a split-bucket run's state (and its digest items) live in
// the DR bucket, not the primary one the manifest/c.Bucket points at.
func TestClearStateDigestsSearchesEveryCandidateBucket(t *testing.T) {
	f := newFakeJanitorS3()
	prefix := "run1-recover/"
	key := prefix + "standard/us-west-2/min/physical/terraform.tfstate"
	f.put(drBucket(), key, "{}") // only in the DR bucket

	digests := &fakeDigestAPI{}
	deps := JanitorDeps{S3: f, Digests: digests}
	opts := testOptions(time.Now())

	n, err := clearStateDigests(context.Background(), deps, opts, candidateBuckets(testAccount, testRegion, testDR), prefix)
	if err != nil {
		t.Fatalf("clearStateDigests: %v", err)
	}
	if n != 1 {
		t.Fatalf("cleared = %d, want 1", n)
	}
	want := drBucket() + "/" + key + "-md5"
	if len(digests.deleted) != 1 || digests.deleted[0] != want {
		t.Fatalf("deleted = %v, want [%q]", digests.deleted, want)
	}
}

// ---- G8: tag lookup failure ----

func TestTagLookupErrorIsUnknownAndNeverSwept(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer: appliedCustomerFor(t, "min_default", "run1"),
	})
	deps := baseDeps(f, 0, errors.New("throttled"))

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateUnknown {
		t.Fatalf("state = %q, want unknown; reason=%q", c.State, c.Reason)
	}

	opts := testOptions(now)
	opts.Sweep = true
	swept := &teardownRecorder{}
	deps.Teardown = swept.teardown
	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept.calls != 0 {
		t.Fatalf("Sweep touched an unknown candidate: %d calls", swept.calls)
	}
}

// ---- defect 1: denylist, not allowlist ----

// TestCountTaggedExcludesKMSPendingDeletionNoise is the regression test for the original
// bug measured against the real account: a successful teardown leaves its CMK in
// PendingDeletion for 7-30 days, still carrying Customer + deleteAfter, so an unfiltered
// tag query counted it as a "live resource" and called a torn-down stack an orphan (17 of
// 20 reported orphans had zero non-KMS resources). countTagged must query with NO
// ResourceTypeFilters and drop KMS from the RESULT instead, so the KMS residue is
// invisible to the count even though it is real, tagged, and still there.
func TestCountTaggedExcludesKMSPendingDeletionNoise(t *testing.T) {
	tag := &fakeTagAPI{byType: map[string]int{
		"kms:key":     5, // PendingDeletion residue from a teardown that already succeeded
		"eks:cluster": 2, // the actual "still standing" signal
	}}
	tags := map[string]TagAPI{testRegion: tag}

	n, anchored, err := countTagged(context.Background(), tags, []string{testRegion}, "smokeab12")
	if err != nil {
		t.Fatalf("countTagged: %v", err)
	}
	if n != 2 {
		t.Fatalf("countTagged = %d, want 2 (the 5 PendingDeletion KMS keys must not count)", n)
	}
	if !anchored {
		t.Fatal("anchored = false, want true (eks:cluster is real standing evidence)")
	}
	if tag.sawResourceTypeFilter {
		t.Fatal("countTagged sent a ResourceTypeFilters - that allowlist shape is defect 1 itself")
	}
}

// TestCountTaggedCountsFormerlyDenylistedTypes is the regression test for the account
// owner's directive: "anything tagged as a smoke test resource is disposable" resolves
// the old denylist's asymmetry (a dangling eks:podidentityassociation was invisible
// while a dangling dms:endpoint anchored an orphan) in favour of counting everything.
// logs, rds:pg, rds:cluster-pg, dms:cert, and eks:podidentityassociation used to be
// denied; none of them are anymore, so every one of them must now count exactly like
// rds:db always did.
func TestCountTaggedCountsFormerlyDenylistedTypes(t *testing.T) {
	tag := &fakeTagAPI{byType: map[string]int{
		"logs:log-group":             3,
		"rds:pg":                     1,
		"rds:cluster-pg":             1,
		"dms:cert":                   1,
		"eks:podidentityassociation": 1,
		"rds:db":                     4,
	}}
	tags := map[string]TagAPI{testRegion: tag}

	n, anchored, err := countTagged(context.Background(), tags, []string{testRegion}, "smokeab12")
	if err != nil {
		t.Fatalf("countTagged: %v", err)
	}
	if n != 11 {
		t.Fatalf("countTagged = %d, want 11 (every type here now counts; only kms is still denied)", n)
	}
	if !anchored {
		t.Fatal("anchored = false, want true")
	}
}

// TestCountTaggedCountsUnrecognizedTypesByDefault is the regression test for defect 1's
// actual root cause, not just its KMS symptom: the OLD allowlist made any resource type
// it did not already know about invisible - dms:rep, dms:task, rds:global-cluster,
// ec2:natgateway, lambda:function, and everything CPI adds after the list was last
// updated. None of these are on deniedResourceTypes, so they must count. This is the
// test that would have caught the original bug directly instead of one KMS type at a
// time.
func TestCountTaggedCountsUnrecognizedTypesByDefault(t *testing.T) {
	tag := &fakeTagAPI{byType: map[string]int{
		"dms:rep":                          1,
		"dms:task":                         1,
		"dms:replication-config":           1,
		"dms:subgrp":                       1,
		"rds:global-cluster":               1,
		"ec2:natgateway":                   1,
		"ec2:eip":                          1,
		"ec2:subnet":                       1,
		"ec2:vpc-endpoint":                 1,
		"lambda:function":                  1,
		"events:rule":                      1,
		"dynamodb:table":                   1,
		"acm:certificate":                  1,
		"route53:hostedzone":               1,
		"elasticloadbalancing:targetgroup": 1,
	}}
	tags := map[string]TagAPI{testRegion: tag}

	n, anchored, err := countTagged(context.Background(), tags, []string{testRegion}, "smokeab12")
	if err != nil {
		t.Fatalf("countTagged: %v", err)
	}
	if n != 15 {
		t.Fatalf("countTagged = %d, want 15 (every type here is unrecognized and must count)", n)
	}
	if !anchored {
		t.Fatal("anchored = false, want true (none of these are insufficient-alone types)")
	}
}

// ---- defect 2: a lone security group is not standing evidence ----

// TestCountTaggedSecurityGroupAloneIsNotAnchored is the regression test for defect 2,
// measured against the real account: 2 security groups the tagging API still lists are
// already gone (describe-security-groups returns InvalidGroup.NotFound), and AWS refuses
// to delete a VPC while a non-default security group inside it still exists - so a
// security group can never legitimately be the only thing left standing.
func TestCountTaggedSecurityGroupAloneIsNotAnchored(t *testing.T) {
	tag := &fakeTagAPI{byType: map[string]int{"ec2:security-group": 2}}
	tags := map[string]TagAPI{testRegion: tag}

	n, anchored, err := countTagged(context.Background(), tags, []string{testRegion}, "smokeab12")
	if err != nil {
		t.Fatalf("countTagged: %v", err)
	}
	if n != 2 {
		t.Fatalf("countTagged = %d, want 2 (the count itself is unchanged, only anchoring)", n)
	}
	if anchored {
		t.Fatal("anchored = true, want false (security groups alone must not anchor a candidate as standing)")
	}
}

// TestCountTaggedSecurityGroupWithVPCIsAnchored proves defect 2's fix is narrow: a
// security group next to a real VPC is completely ordinary and must still count as
// standing evidence. Only "security groups and nothing else" is insufficient.
func TestCountTaggedSecurityGroupWithVPCIsAnchored(t *testing.T) {
	tag := &fakeTagAPI{byType: map[string]int{"ec2:security-group": 2, "ec2:vpc": 1}}
	tags := map[string]TagAPI{testRegion: tag}

	n, anchored, err := countTagged(context.Background(), tags, []string{testRegion}, "smokeab12")
	if err != nil {
		t.Fatalf("countTagged: %v", err)
	}
	if n != 3 {
		t.Fatalf("countTagged = %d, want 3", n)
	}
	if !anchored {
		t.Fatal("anchored = false, want true (a real VPC is present alongside the security groups)")
	}
}

// TestClassifySecurityGroupAloneIsCleanNotOrphan exercises the fix end to end through
// classify(): a candidate whose only tagged resources are stale-tagging-index security
// groups must classify Clean (and therefore never be swept), not Orphan.
func TestClassifySecurityGroupAloneIsCleanNotOrphan(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer: appliedCustomerFor(t, "min_default", "run1"),
	})
	tag := &fakeTagAPI{byType: map[string]int{"ec2:security-group": 2}}
	deps := JanitorDeps{
		S3: f, Tags: map[string]TagAPI{testRegion: tag, testDR: tag},
		Locks: newFakeLockAPI(), Matrix: testMatrix(),
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateClean {
		t.Fatalf("state = %q, want clean (security groups alone are not standing evidence); reason=%q", c.State, c.Reason)
	}
	if rep.Orphans != 0 {
		t.Fatalf("rep.Orphans = %d, want 0", rep.Orphans)
	}
}

// ---- S3 listing failure fails the whole cycle ----

func TestScanFailsClosedOnListingError(t *testing.T) {
	f := newFakeJanitorS3()
	f.listErr = errors.New("access denied")
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	deps := baseDeps(f, 0, nil)
	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err == nil {
		t.Fatal("expected Scan to fail when listing errors, got nil")
	}
	if rep != nil {
		t.Fatalf("expected no partial report on a listing failure, got %+v", rep)
	}
}

// ---- max-sweeps cap ----

type teardownRecorder struct {
	calls    int
	fail     bool
	lastArgs PhaseParams
}

func (r *teardownRecorder) teardown(_ context.Context, p PhaseParams, _ bool) error {
	r.calls++
	r.lastArgs = p
	if r.fail {
		return errors.New("destroy failed")
	}
	return nil
}

// noResidueTags wires a post-destroy tag recheck that finds nothing left, so a
// candidate hand-built with a real Customer/Region (needed to exercise the residue
// check's gate at all) still resolves to a plain "destroyed" success. Tests that only
// care about maxFailures/MaxSweeps/wall-clock-budget behavior use this to keep the
// residue check (Change 3) a no-op rather than re-testing it in every one of them.
func noResidueTags() map[string]TagAPI {
	clean := &fakeTagAPI{resources: 0}
	return map[string]TagAPI{testRegion: clean, testDR: clean}
}

// TestSweepRefusesToRunWhenSweepIsFalse pins the enforcement point for "report mode
// never mutates anything": Sweep itself must refuse to run when o.Sweep is false,
// rather than relying on main.go being the only caller that happens to gate it. Before
// this guard existed, calling Sweep directly with Sweep:false ran the full destroy path
// anyway - report mode was safe only by accident of there being one caller.
func TestSweepRefusesToRunWhenSweepIsFalse(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
	}}
	recorder := &teardownRecorder{}
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: recorder.teardown, Tags: noResidueTags()}
	opts := testOptions(time.Now())
	opts.Sweep = false

	err := Sweep(context.Background(), deps, opts, rep)
	if err == nil {
		t.Fatalf("Sweep with o.Sweep=false: want an error, got nil")
	}
	if recorder.calls != 0 {
		t.Fatalf("Sweep called Teardown %d times with o.Sweep=false, want 0", recorder.calls)
	}
	if rep.Swept != 0 || rep.Failed != 0 || rep.Residue != 0 {
		t.Fatalf("Swept=%d Failed=%d Residue=%d, want 0/0/0", rep.Swept, rep.Failed, rep.Residue)
	}
}

func TestSweepRespectsMaxSweepsCap(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3, Customer: "smokebb", Region: testRegion, DRRegion: testDR},
	}}
	recorder := &teardownRecorder{}
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: recorder.teardown, Tags: noResidueTags()}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("Teardown called %d times, want 1 (max-sweeps cap)", recorder.calls)
	}
	swept, untouched := 0, 0
	for _, c := range rep.Candidates {
		switch c.SweepResult {
		case "destroyed":
			swept++
		case "":
			untouched++
		}
	}
	if swept != 1 || untouched != 1 {
		t.Fatalf("swept=%d untouched=%d, want 1/1", swept, untouched)
	}
	if rep.Swept != 1 {
		t.Fatalf("rep.Swept = %d, want 1", rep.Swept)
	}
}

// TestSweepPassesGuardedIdentifierNotARecompute proves Sweep forces Teardown's
// out-of-band mutations (NLB/MSK/container-insights/category-B reclaim) to act on the
// SAME identifier guardProtected just checked, not a fresh recompute from today's
// matrix. testMatrix's "min_default" config salts to "smoke-min" if recomputed live,
// but the candidate here carries "smokeaa-min" (standing in for classify()'s recorded,
// already-guarded identity after identity drift) - Teardown must receive that value,
// not "smoke-min".
func TestSweepPassesGuardedIdentifierNotARecompute(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
	}}
	recorder := &teardownRecorder{}
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: recorder.teardown, Tags: noResidueTags()}
	opts := testOptions(time.Now())

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("Teardown called %d times, want 1", recorder.calls)
	}
	if recorder.lastArgs.IdentifierOverride != "smokeaa-min" {
		t.Fatalf("IdentifierOverride = %q, want the candidate's guarded identifier %q", recorder.lastArgs.IdentifierOverride, "smokeaa-min")
	}
}

func TestSweepGuardsProtectedIdentityAgainAtDestroyTime(t *testing.T) {
	// A candidate that somehow reached Sweep with a protected identity (e.g. Scan's guard
	// was bypassed by a caller building a Report by hand, as production code near this
	// path might in a future refactor) must still never be destroyed.
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "dev-min", State: StateOrphan, Resources: 3},
	}}
	recorder := &teardownRecorder{}
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: recorder.teardown}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 5

	err := Sweep(context.Background(), deps, opts, rep)
	if !errors.Is(err, ErrProtected) {
		t.Fatalf("Sweep err = %v, want ErrProtected", err)
	}
	if recorder.calls != 0 {
		t.Fatalf("Teardown called %d times, want 0", recorder.calls)
	}
}

// ---- Sweep progress guarantee (a failing candidate must not starve the rest) ----

// TestSweepMakesProgressPastAPersistentlyFailingCandidate is the regression test for
// defect 2: candidates are sorted by prefix, so with max-sweeps 1 a candidate that always
// fails (a lock held by a dead process, say) sorted first every cycle used to burn the
// one sweep slot on its own failure and starve every real orphan behind it, forever. run1
// fails here on every call; run2 is the real orphan behind it and must still get a
// destroy attempt (and succeed) in THIS SAME cycle.
func TestSweepMakesProgressPastAPersistentlyFailingCandidate(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3, Customer: "smokebb", Region: testRegion, DRRegion: testDR},
	}}
	var calls []string
	deps := JanitorDeps{Matrix: testMatrix(), Tags: noResidueTags(), Teardown: func(_ context.Context, p PhaseParams, _ bool) error {
		calls = append(calls, p.RunID)
		if p.RunID == "run1" {
			return errors.New("destroy failed: lock held by a dead process")
		}
		return nil
	}}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1 // the production value (50-janitor-cron.yaml)

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(calls) != 2 || calls[0] != "run1" || calls[1] != "run2" {
		t.Fatalf("Teardown calls = %v, want [run1 run2] - run1 failing must not stop run2 from being attempted", calls)
	}
	if rep.Swept != 1 || rep.Failed != 1 {
		t.Fatalf("Swept=%d Failed=%d, want 1/1", rep.Swept, rep.Failed)
	}
	if rep.Candidates[1].SweepResult != "destroyed" {
		t.Fatalf("run2.SweepResult = %q, want destroyed - a failing run1 must not block it", rep.Candidates[1].SweepResult)
	}
}

// TestSweepStopsAfterMaxSweepsSuccesses proves the cap still means something once fixed:
// it stops counting failures, not counting successes. run1 fails, run2 succeeds and fills
// the one-success budget, run3 - a perfectly good orphan - must be left untouched because
// the cap was already met, not because anything is wrong with it.
func TestSweepStopsAfterMaxSweepsSuccesses(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3, Customer: "smokebb", Region: testRegion, DRRegion: testDR},
		{Prefix: "run3-min_default/", Bucket: primaryBucket(), RunID: "run3", ConfigName: "min_default", Identifier: "smokecc-min", State: StateOrphan, Resources: 3, Customer: "smokecc", Region: testRegion, DRRegion: testDR},
	}}
	var calls []string
	deps := JanitorDeps{Matrix: testMatrix(), Tags: noResidueTags(), Teardown: func(_ context.Context, p PhaseParams, _ bool) error {
		calls = append(calls, p.RunID)
		if p.RunID == "run1" {
			return errors.New("destroy failed")
		}
		return nil
	}}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(calls) != 2 || calls[0] != "run1" || calls[1] != "run2" {
		t.Fatalf("Teardown calls = %v, want [run1 run2] - run3 must never be attempted once the cap is met", calls)
	}
	if rep.Candidates[2].SweepResult != "" {
		t.Fatalf("run3.SweepResult = %q, want untouched", rep.Candidates[2].SweepResult)
	}
}

// ---- defect 3: bounded total attempts, not just bounded successes ----

// TestSweepBoundsFailedAttemptsPerCycle is the regression test for defect 3: with
// MaxSweeps wide open, an explicit MaxSweepFailures must still stop new attempts once
// that many have failed in this cycle, rather than trying every orphan in the report.
func TestSweepBoundsFailedAttemptsPerCycle(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3},
		{Prefix: "run3-min_default/", Bucket: primaryBucket(), RunID: "run3", ConfigName: "min_default", Identifier: "smokecc-min", State: StateOrphan, Resources: 3},
		{Prefix: "run4-min_default/", Bucket: primaryBucket(), RunID: "run4", ConfigName: "min_default", Identifier: "smokedd-min", State: StateOrphan, Resources: 3},
	}}
	var calls []string
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: func(_ context.Context, p PhaseParams, _ bool) error {
		calls = append(calls, p.RunID)
		return errors.New("destroy failed: quota exceeded")
	}}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 10 // success cap wide open; the failure cap is what must bind here
	opts.MaxSweepFailures = 2

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(calls) != 2 || calls[0] != "run1" || calls[1] != "run2" {
		t.Fatalf("Teardown calls = %v, want [run1 run2] (stop after 2 failures)", calls)
	}
	if rep.Failed != 2 {
		t.Fatalf("rep.Failed = %d, want 2", rep.Failed)
	}
	for _, c := range rep.Candidates[2:] {
		if c.SweepResult != "skipped: max-sweep-failures reached this cycle" {
			t.Fatalf("%s.SweepResult = %q, want the max-sweep-failures skip message", c.RunID, c.SweepResult)
		}
	}
}

// TestSweepDefaultMaxSweepFailuresWhenUnset proves the fallback in Sweep actually bounds
// something: testOptions() never sets MaxSweepFailures, so the zero value must fall back
// to a small default rather than being read as "unbounded" (which is defect 3 itself).
func TestSweepDefaultMaxSweepFailuresWhenUnset(t *testing.T) {
	var candidates []Candidate
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		candidates = append(candidates, Candidate{
			Prefix: "run" + id + "-min_default/", Bucket: primaryBucket(), RunID: "run" + id,
			ConfigName: "min_default", Identifier: "smoke" + id + "-min", State: StateOrphan, Resources: 3,
		})
	}
	rep := &Report{Candidates: candidates}
	calls := 0
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: func(context.Context, PhaseParams, bool) error {
		calls++
		return errors.New("destroy failed")
	}}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 10 // wide open; only the failure default should bind
	// opts.MaxSweepFailures left at its zero value on purpose.

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if calls != 2 {
		t.Fatalf("Teardown attempted %d times, want 2 (the default max-sweep-failures fallback)", calls)
	}
}

// TestSweepFailureCapStillMakesProgressPastOneBadApple confirms the default fallback
// (2) is not so tight it reintroduces defect 2: with the default in effect, one failing
// candidate must not stop a real orphan right behind it from being attempted and
// destroyed in the same cycle.
func TestSweepFailureCapStillMakesProgressPastOneBadApple(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3, Customer: "smokebb", Region: testRegion, DRRegion: testDR},
	}}
	deps := JanitorDeps{Matrix: testMatrix(), Tags: noResidueTags(), Teardown: func(_ context.Context, p PhaseParams, _ bool) error {
		if p.RunID == "run1" {
			return errors.New("destroy failed")
		}
		return nil
	}}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1
	// opts.MaxSweepFailures left at its zero value (falls back to the default of 2).

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.Candidates[1].SweepResult != "destroyed" {
		t.Fatalf("run2.SweepResult = %q, want destroyed - the default failure cap must not block the candidate right behind a single failure", rep.Candidates[1].SweepResult)
	}
}

// ---- Sweep wall-clock budget (bound by TIME, not just attempt count) ----

// TestDefaultSweepBudgetDerivesFromPodDeadline proves DefaultSweepBudget is arithmetic
// on JanitorPodActiveDeadlineSeconds, not an independent number someone could tune out
// of sync with 50-janitor-cron.yaml's activeDeadlineSeconds.
func TestDefaultSweepBudgetDerivesFromPodDeadline(t *testing.T) {
	want := time.Duration(JanitorPodActiveDeadlineSeconds)*time.Second - sweepSetupMargin
	if got := DefaultSweepBudget(); got != want {
		t.Fatalf("DefaultSweepBudget() = %s, want %s (podDeadline - sweepSetupMargin)", got, want)
	}
	// 50-janitor-cron.yaml's own comment claims this literal; keep the claim honest.
	if JanitorPodActiveDeadlineSeconds != 12600 {
		t.Fatalf("JanitorPodActiveDeadlineSeconds = %d, want 12600 to match 50-janitor-cron.yaml activeDeadlineSeconds - update the YAML comment (and literal) if this ever changes", JanitorPodActiveDeadlineSeconds)
	}
}

// TestJanitorOptionsSweepBudgetOverride proves an explicit SweepBudget wins over the
// derived default, the same override shape MaxSweepFailures already has.
func TestJanitorOptionsSweepBudgetOverride(t *testing.T) {
	o := JanitorOptions{}
	if got := o.sweepBudget(); got != DefaultSweepBudget() {
		t.Fatalf("sweepBudget() = %s, want the derived default %s", got, DefaultSweepBudget())
	}
	o.SweepBudget = 5 * time.Minute
	if got := o.sweepBudget(); got != 5*time.Minute {
		t.Fatalf("sweepBudget() = %s, want the explicit override 5m", got)
	}
}

// TestSweepStopsStartingNewAttemptsPastWallClockBudget is the regression test for the
// wall-clock bound: Teardown has no internal timeout (tg.Destroy takes no context), so
// nothing used to stop several slow attempts from together outlasting the pod's
// activeDeadlineSeconds. Each attempt here "takes" 50 minutes (simulated by advancing
// a fake clock inside the Teardown func); with a 90-minute budget, run1 and run2 both
// fit (0m and 50m elapsed when each STARTS), but run3 would start at 100m - past
// budget - so it must be skipped without ever calling Teardown.
func TestSweepStopsStartingNewAttemptsPastWallClockBudget(t *testing.T) {
	start := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	clock := start
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3},
		{Prefix: "run3-min_default/", Bucket: primaryBucket(), RunID: "run3", ConfigName: "min_default", Identifier: "smokecc-min", State: StateOrphan, Resources: 3},
	}}
	var calls []string
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: func(_ context.Context, p PhaseParams, _ bool) error {
		calls = append(calls, p.RunID)
		clock = clock.Add(50 * time.Minute) // simulate a slow destroy
		return nil
	}}
	opts := testOptions(start)
	opts.Now = func() time.Time { return clock }
	opts.MaxSweeps = 10 // wide open; the wall-clock budget is what must bind here
	opts.SweepBudget = 90 * time.Minute

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(calls) != 2 || calls[0] != "run1" || calls[1] != "run2" {
		t.Fatalf("Teardown calls = %v, want [run1 run2] - run3 must never be attempted once the budget is exhausted", calls)
	}
	if rep.Swept != 2 {
		t.Fatalf("rep.Swept = %d, want 2", rep.Swept)
	}
	got := rep.Candidates[2].SweepResult
	if !strings.Contains(got, "skipped: sweep wall-clock budget") {
		t.Fatalf("run3.SweepResult = %q, want a wall-clock-budget skip message", got)
	}
}

// TestSweepWallClockBudgetStillMakesProgressPastAFailingCandidate proves the wall-clock
// bound does not reintroduce defect 2's starvation: a slow-but-failing run1 must not
// stop run2, a fast real orphan right behind it, from being attempted and destroyed in
// the same cycle, as long as the budget has not actually run out yet.
func TestSweepWallClockBudgetStillMakesProgressPastAFailingCandidate(t *testing.T) {
	start := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	clock := start
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3},
		{Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default", Identifier: "smokebb-min", State: StateOrphan, Resources: 3},
	}}
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: func(_ context.Context, p PhaseParams, _ bool) error {
		clock = clock.Add(10 * time.Minute)
		if p.RunID == "run1" {
			return errors.New("destroy failed")
		}
		return nil
	}}
	opts := testOptions(start)
	opts.Now = func() time.Time { return clock }
	opts.MaxSweeps = 10
	opts.SweepBudget = 90 * time.Minute

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.Candidates[1].SweepResult != "destroyed" {
		t.Fatalf("run2.SweepResult = %q, want destroyed", rep.Candidates[1].SweepResult)
	}
}

// ---- state-orphaned residue (a destroy that succeeded but left tagged resources) ----

// TestSweepDetectsResidueAfterASuccessfulDestroy is the regression test for the
// smoke4879-bi shape in the measured baseline: Teardown's terragrunt destroy reports
// success (nothing left in STATE), but a post-destroy re-query of the same tag filter
// classify() used still finds real, anchored resources - proof they were never
// reachable from state at all. That candidate must land on StateResidue, not
// "destroyed" and not "failed" (a retry cannot fix state that never had them), and it
// must NOT consume the MaxSweeps success budget - see the next test for why that part
// matters.
func TestSweepDetectsResidueAfterASuccessfulDestroy(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{
			// DRRegion deliberately empty: the fixture below models a single-region
			// query. countTaggedDetailed dedups identical region strings but not a
			// distinct primary+DR pair, so wiring both to the same fake would double
			// every count - this candidate only ever had one region to begin with.
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 3,
			Customer: "smoke4879", Region: testRegion,
		},
	}}
	// The destroy itself succeeds (err == nil): terragrunt had nothing left to
	// destroy for the surviving objects because they were never in its state.
	residueTags := &fakeTagAPI{byType: map[string]int{"dms:endpoint": 2, "dms:subgrp": 1}}
	deps := JanitorDeps{
		Matrix: testMatrix(),
		Tags:   map[string]TagAPI{testRegion: residueTags, testDR: residueTags},
		Teardown: func(context.Context, PhaseParams, bool) error {
			return nil
		},
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateResidue {
		t.Fatalf("State = %q, want residue; reason=%q sweep_result=%q", c.State, c.Reason, c.SweepResult)
	}
	if c.SweepResult != "residue: needs manual cleanup" {
		t.Fatalf("SweepResult = %q, want %q", c.SweepResult, "residue: needs manual cleanup")
	}
	if !strings.Contains(c.Reason, "dms:endpoint x2") || !strings.Contains(c.Reason, "dms:subgrp x1") {
		t.Fatalf("Reason = %q, want it to name the surviving resource types", c.Reason)
	}
	if rep.Swept != 0 {
		t.Fatalf("rep.Swept = %d, want 0 (residue is not a clean success)", rep.Swept)
	}
	if rep.Failed != 0 {
		t.Fatalf("rep.Failed = %d, want 0 (residue is not a failed destroy - a retry cannot fix it)", rep.Failed)
	}
	if rep.Residue != 1 {
		t.Fatalf("rep.Residue = %d, want 1", rep.Residue)
	}
}

// TestSweepResidueDoesNotConsumeSweepBudget is the "cannot consume the sweep budget
// every cycle forever" requirement: with MaxSweeps=1, run1 resolves to residue (a
// successful destroy with survivors) and must NOT count against the one-success
// budget, so run2 - a real, fully clean orphan sorted right behind it - still gets
// attempted and destroyed in the SAME cycle.
func TestSweepResidueDoesNotConsumeSweepBudget(t *testing.T) {
	residueTags := &fakeTagAPI{byType: map[string]int{"dms:endpoint": 1}}
	cleanTags := &fakeTagAPI{resources: 0}

	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default",
			Identifier: "smokeaa-min", State: StateOrphan, Resources: 3,
			Customer: "smokeaa", Region: testRegion, DRRegion: testDR,
		},
		{
			Prefix: "run2-min_default/", Bucket: primaryBucket(), RunID: "run2", ConfigName: "min_default",
			Identifier: "smokebb-min", State: StateOrphan, Resources: 3,
			Customer: "smokebb", Region: testRegion, DRRegion: testDR,
		},
	}}
	// Route the post-destroy tag recheck by which run is currently being torn down:
	// run1's customer sees the residue fixture, run2's sees a clean account. A real
	// GetResources call is already scoped by TagFilters{Customer: <value>}, so this
	// fake stands in for "two different customers get two different answers" without
	// needing a per-customer TagAPI plumbed through JanitorDeps.
	router := &routingTagAPI{byCustomerTail: map[byte]*fakeTagAPI{
		'a': residueTags, // smokeaa
		'b': cleanTags,   // smokebb
	}}
	deps := JanitorDeps{
		Matrix: testMatrix(),
		Tags:   map[string]TagAPI{testRegion: router, testDR: router},
		Teardown: func(context.Context, PhaseParams, bool) error {
			return nil
		},
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rep.Candidates[0].State != StateResidue {
		t.Fatalf("run1.State = %q, want residue", rep.Candidates[0].State)
	}
	if rep.Candidates[1].SweepResult != "destroyed" {
		t.Fatalf("run2.SweepResult = %q, want destroyed - residue on run1 must not block run2's turn", rep.Candidates[1].SweepResult)
	}
	if rep.Swept != 1 {
		t.Fatalf("rep.Swept = %d, want 1 (only run2's clean destroy counts)", rep.Swept)
	}
	if rep.Residue != 1 {
		t.Fatalf("rep.Residue = %d, want 1", rep.Residue)
	}
}

// routingTagAPI answers GetResources differently depending on the last byte of the
// Customer tag filter's value, so a single fake JanitorDeps.Tags client can stand in
// for what production's per-customer-scoped queries would actually return.
type routingTagAPI struct {
	byCustomerTail map[byte]*fakeTagAPI
}

func (r *routingTagAPI) GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, opts ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	var customer string
	for _, f := range in.TagFilters {
		if aws.ToString(f.Key) == "Customer" && len(f.Values) > 0 {
			customer = f.Values[0]
		}
	}
	if customer == "" {
		return &resourcegroupstaggingapi.GetResourcesOutput{}, nil
	}
	fake, ok := r.byCustomerTail[customer[len(customer)-1]]
	if !ok {
		return &resourcegroupstaggingapi.GetResourcesOutput{}, nil
	}
	return fake.GetResources(ctx, in, opts...)
}

// TestSweepResiduePostCheckFailureFailsClosedToUnknown proves the post-destroy
// recheck follows the same fail-closed posture as every other AWS call in this file:
// a query it could not complete must never be silently read as "destroyed".
func TestSweepResiduePostCheckFailureFailsClosedToUnknown(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default",
			Identifier: "smokeaa-min", State: StateOrphan, Resources: 3,
			Customer: "smokeaa", Region: testRegion, DRRegion: testDR,
		},
	}}
	failing := &fakeTagAPI{err: errors.New("throttled")}
	deps := JanitorDeps{
		Matrix: testMatrix(),
		Tags:   map[string]TagAPI{testRegion: failing, testDR: failing},
		Teardown: func(context.Context, PhaseParams, bool) error {
			return nil
		},
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateUnknown {
		t.Fatalf("State = %q, want unknown (a failed verification query must not be read as success)", c.State)
	}
	if rep.Swept != 0 {
		t.Fatalf("rep.Swept = %d, want 0", rep.Swept)
	}
	if rep.Residue != 0 {
		t.Fatalf("rep.Residue = %d, want 0 (inconclusive is not the same as confirmed residue)", rep.Residue)
	}
}

// TestSweepSecurityGroupOnlySurvivorIsStillDestroyedNotResidue proves the post-destroy
// recheck reuses defect 2's anchoring rule: if the only thing GetResources still
// returns is stale tagging-index security-group residue, that is not a real leak
// terraform failed to reach, and the candidate must resolve to a clean "destroyed",
// exactly as classify() already treats the same shape pre-destroy.
func TestSweepSecurityGroupOnlySurvivorIsStillDestroyedNotResidue(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default",
			Identifier: "smokeaa-min", State: StateOrphan, Resources: 3,
			Customer: "smokeaa", Region: testRegion, DRRegion: testDR,
		},
	}}
	staleSG := &fakeTagAPI{byType: map[string]int{"ec2:security-group": 2}}
	deps := JanitorDeps{
		Matrix: testMatrix(),
		Tags:   map[string]TagAPI{testRegion: staleSG, testDR: staleSG},
		Teardown: func(context.Context, PhaseParams, bool) error {
			return nil
		},
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateOrphan {
		// Sweep never rewrites State on the clean path - it only sets SweepResult.
		t.Fatalf("State = %q, want unchanged (orphan); Sweep only updates State on residue/unknown", c.State)
	}
	if c.SweepResult != "destroyed" {
		t.Fatalf("SweepResult = %q, want destroyed (security groups alone are not residue)", c.SweepResult)
	}
	if rep.Swept != 1 || rep.Residue != 0 {
		t.Fatalf("Swept=%d Residue=%d, want 1/0", rep.Swept, rep.Residue)
	}
}

// ---- keep-on-failure ----

// TestClassifyKeepOnFailureIsKeptNotOrphan is the regression test for defect 3: a run
// whose workflow set --keep-on-failure and then failed is orphan-SHAPED (stale, unowned,
// tagged resources live, no lock in the way) but must classify as StateKept, never
// StateOrphan, so Sweep - which only ever acts on StateOrphan - leaves it alone.
func TestClassifyKeepOnFailureIsKeptNotOrphan(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer: appliedCustomerFor(t, "min_default", "run1"),
	})
	wf := JanitorWorkflowList{Items: []JanitorWorkflow{
		wfWithParam("run1", "Failed", "keep-on-failure", "true"),
	}}
	deps := baseDeps(f, 3, nil)

	rep, err := Scan(context.Background(), deps, testOptions(now), wf)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateKept {
		t.Fatalf("state = %q, want kept; reason=%q", c.State, c.Reason)
	}
	if !c.KeepOnFailure {
		t.Fatal("KeepOnFailure = false, want true")
	}

	opts := testOptions(now)
	opts.Sweep = true
	swept := &teardownRecorder{}
	deps.Teardown = swept.teardown
	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept.calls != 0 {
		t.Fatalf("Sweep called Teardown %d times, want 0 (kept must never be swept)", swept.calls)
	}
	if rep.Orphans != 0 {
		t.Fatalf("rep.Orphans = %d, want 0 (kept is not counted as an orphan)", rep.Orphans)
	}
}

// TestSweepSkipsKeepOnFailureAtDestroyTime is the same defense-in-depth shape as
// TestSweepGuardsProtectedIdentityAgainAtDestroyTime: a candidate that somehow reaches
// Sweep with State still Orphan but KeepOnFailure true (a hand-built Report, or a future
// refactor that stops re-deriving State from classify) must still never be destroyed.
func TestSweepSkipsKeepOnFailureAtDestroyTime(t *testing.T) {
	rep := &Report{Candidates: []Candidate{
		{Prefix: "run1-min_default/", Bucket: primaryBucket(), RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, KeepOnFailure: true},
	}}
	recorder := &teardownRecorder{}
	deps := JanitorDeps{Matrix: testMatrix(), Teardown: recorder.teardown}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 5

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if recorder.calls != 0 {
		t.Fatalf("Teardown called %d times, want 0", recorder.calls)
	}
	if rep.Candidates[0].SweepResult != "skipped: keep-on-failure" {
		t.Fatalf("SweepResult = %q, want %q", rep.Candidates[0].SweepResult, "skipped: keep-on-failure")
	}
	if rep.Swept != 0 || rep.Failed != 0 {
		t.Fatalf("Swept=%d Failed=%d, want 0/0", rep.Swept, rep.Failed)
	}
}

// ---- defect P1: keep-on-failure must outlive the Workflow CR ----

// TestClassifyKeepOnFailureSurvivesWorkflowCRExpiry is the regression test for defect
// P1: a run that set --keep-on-failure and failed must stay StateKept, and therefore
// never sweepable, even after Argo has already TTL'd its Workflow CR away
// (10-scenario.yaml ttlStrategy reaps a failed workflow after three days - well inside
// "left up over a weekend"). Before the fix, KeepOnFailure came ONLY from the workflow
// index (janitor.go byWF): with no workflow object at all in the wfList passed to Scan
// (simulating the CR being gone), that index has no entry for this run id, base.
// KeepOnFailure silently defaults to false, and the candidate reads Orphan - sweepable.
// The fix makes the manifest (written durably by every Teardown call, phases.go)
// authoritative whenever it has an opinion, so this must classify Kept from the
// manifest alone with zero workflow objects present.
func TestClassifyKeepOnFailureSurvivesWorkflowCRExpiry(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer:       appliedCustomerFor(t, "min_default", "run1"),
		KeepOnFailure:         true,
		KeepOnFailureRecorded: true,
	})
	deps := baseDeps(f, 3, nil)

	// No workflow objects at all - this is what the account looks like three-plus days
	// after the run failed, once ttlStrategy has reaped the CR.
	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateKept {
		t.Fatalf("state = %q, want kept (manifest must protect this run with no workflow object present); reason=%q", c.State, c.Reason)
	}
	if !c.KeepOnFailure {
		t.Fatal("KeepOnFailure = false, want true (read from the manifest, not the empty workflow index)")
	}

	opts := testOptions(now)
	opts.Sweep = true
	swept := &teardownRecorder{}
	deps.Teardown = swept.teardown
	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if swept.calls != 0 {
		t.Fatalf("Sweep called Teardown %d times, want 0 (a kept run with an expired CR must still never be swept)", swept.calls)
	}
	if rep.Orphans != 0 {
		t.Fatalf("rep.Orphans = %d, want 0", rep.Orphans)
	}
}

// TestClassifyKeepOnFailureRecordedFalseOverridesStaleWorkflowIndex is the flip side:
// a manifest that explicitly recorded KeepOnFailure=false must win even if a leftover
// (or malformed) workflow entry claims otherwise - the manifest is authoritative
// whenever KeepOnFailureRecorded is true, not just when it happens to say true.
func TestClassifyKeepOnFailureRecordedFalseOverridesStaleWorkflowIndex(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer:       appliedCustomerFor(t, "min_default", "run1"),
		KeepOnFailure:         false,
		KeepOnFailureRecorded: true,
	})
	wf := JanitorWorkflowList{Items: []JanitorWorkflow{
		wfWithParam("run1", "Failed", "keep-on-failure", "true"),
	}}
	deps := baseDeps(f, 3, nil)

	rep, err := Scan(context.Background(), deps, testOptions(now), wf)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateOrphan {
		t.Fatalf("state = %q, want orphan (recorded keep-on-failure=false must win over the workflow index); reason=%q", c.State, c.Reason)
	}
}

// ---- defect P2: identity must come from the manifest, not a live recompute ----

// TestClassifyMissingAppliedCustomerIsNeedsReviewNotClean is the regression test for
// defect P2's stated failure mode itself: a manifest written before AppliedCustomer
// existed, whose recomputed identity happens to find NO tagged resources (exactly what
// a config-drifted, still-genuinely-leaked stack looks like once the janitor queries
// under the wrong, recomputed customer). The old code reported this permanently Clean
// - neither alerted nor swept, the leak invisible forever. The fix must never let an
// unverified identity produce a Clean verdict: this must classify NeedsReview instead,
// and say why, so a human decides rather than trusting a guess that found nothing.
func TestClassifyMissingAppliedCustomerIsNeedsReviewNotClean(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		// AppliedCustomer deliberately omitted: this is what every manifest written
		// before this fix looks like.
	})
	deps := baseDeps(f, 0, nil) // recomputed identity finds nothing - the danger case

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateNeedsReview {
		t.Fatalf("state = %q, want needs-review (an unverified identity must never produce a clean verdict); reason=%q", c.State, c.Reason)
	}
	if c.State == StateClean {
		t.Fatal("state must never be clean for a manifest with no recorded identity - that is exactly the silent-miss bug this fix closes")
	}
	if !strings.Contains(c.Reason, "applied-customer") {
		t.Fatalf("reason = %q, want it to explain the missing applied-customer recording", c.Reason)
	}
}

// TestClassifyMissingAppliedCustomerStillDetectsARealOrphan proves the fix is
// surgical, not a blanket quarantine: a pre-fix manifest (no AppliedCustomer) whose
// config has NOT drifted still correctly classifies Orphan when the recomputed
// identity finds real, live resources - exactly the shape of the two genuine leaks
// already confirmed against the live account in earlier review rounds. Downgrading
// every old manifest to NeedsReview regardless of outcome would have hidden those
// same leaks behind a human gate for no reason; only a "found nothing" result is
// untrustworthy enough to withhold (see TestClassifyMissingAppliedCustomerIsNeedsReviewNotClean).
func TestClassifyMissingAppliedCustomerStillDetectsARealOrphan(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		// AppliedCustomer omitted, but nothing has drifted: the recomputed identity is
		// still the real one, so a positive match here must be trusted.
	})
	deps := baseDeps(f, 3, nil)

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateOrphan {
		t.Fatalf("state = %q, want orphan (a genuine leak under an unchanged identity must still be caught); reason=%q", c.State, c.Reason)
	}
	if !strings.Contains(c.Reason, "unverified") {
		t.Fatalf("reason = %q, want it to note the identity was recomputed rather than recorded", c.Reason)
	}
}

// TestClassifyManifestMissingBothFieldsFailsClosed covers a manifest from before EITHER
// durability fix existed (no AppliedCustomer, no KeepOnFailureRecorded), together with a
// workflow entry that claims keep-on-failure=false (so, absent the identity gate, this
// would otherwise sail through to a tag query and come back Orphan or Clean depending on
// the fake). The missing identity must dominate and produce NeedsReview regardless of
// what the keep-on-failure signal says - fail closed, never Clean, never a sweepable
// Orphan built on a guessed identity.
func TestClassifyManifestMissingBothFieldsFailsClosed(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		// Neither AppliedCustomer nor KeepOnFailureRecorded set: an ancient manifest.
	})
	wf := JanitorWorkflowList{Items: []JanitorWorkflow{
		wfWithParam("run1", "Failed", "keep-on-failure", "false"),
	}}
	deps := baseDeps(f, 0, nil) // 0 tagged resources: would read Clean if the gate were skipped

	rep, err := Scan(context.Background(), deps, testOptions(now), wf)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateNeedsReview {
		t.Fatalf("state = %q, want needs-review (fail closed on a manifest missing both fields); reason=%q", c.State, c.Reason)
	}
}

// customerAwareTagAPI answers GetResources based on the "Customer" tag filter value the
// request actually carries, unlike fakeTagAPI (which ignores filters and returns a
// fixed count regardless of who asked). Needed to prove classify() queries AWS with the
// manifest's RECORDED identity and not a fresh recompute: the two customer values must
// behave differently for the test to be able to tell them apart.
type customerAwareTagAPI struct {
	resourcesFor map[string]int
}

func (f *customerAwareTagAPI) GetResources(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	var customer string
	for _, tf := range in.TagFilters {
		if aws.ToString(tf.Key) == "Customer" && len(tf.Values) > 0 {
			customer = tf.Values[0]
		}
	}
	var list []rgtypes.ResourceTagMapping
	for i := 0; i < f.resourcesFor[customer]; i++ {
		list = append(list, rgtypes.ResourceTagMapping{
			ResourceARN: aws.String("arn:aws:ec2:us-east-1:123456789012:instance/i-" + string(rune('a'+i))),
		})
	}
	return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: list}, nil
}

// TestClassifyUsesRecordedIdentityNotRecomputedMatrix is the direct regression test for
// defect P2 itself: the matrix's config has since drifted from what this run actually
// applied (simulated by seeding an AppliedCustomer the current matrix would never
// recompute for this run id), and the account's real tagged resources sit under the
// OLD, recorded customer value. The old, buggy behavior recomputes Config.Salted
// against today's matrix, queries under the NEW value, finds nothing, and reports the
// leak Clean forever. The fix must query under the recorded value, find the leak, and
// classify it Orphan.
func TestClassifyUsesRecordedIdentityNotRecomputedMatrix(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	recomputed := appliedCustomerFor(t, "min_default", "run1") // what today's matrix salts to
	const recorded = "smokeold1"                               // what the run actually applied, back when the config's customer flag was different
	if recorded == recomputed {
		t.Fatalf("test fixture bug: recorded and recomputed must differ (%q)", recorded)
	}
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		// No DRRegion: only one region is queried, so the resource count below is
		// unambiguous (countTaggedDetailed queries every non-empty, distinct region on
		// the manifest, and a real single-region config carries no dr_region either).
		Region:          testRegion,
		AppliedCustomer: recorded,
	})
	tag := &customerAwareTagAPI{resourcesFor: map[string]int{recorded: 3}} // nothing under "recomputed"
	deps := JanitorDeps{
		S3: f, Tags: map[string]TagAPI{testRegion: tag, testDR: tag},
		Locks: newFakeLockAPI(), Matrix: testMatrix(),
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}

	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := mustCandidate(t, rep, "run1-min_default/")
	if c.State != StateOrphan {
		t.Fatalf("state = %q, want orphan (querying under the recomputed identity would silently miss this leak and report clean); reason=%q", c.State, c.Reason)
	}
	if c.Customer != recorded {
		t.Fatalf("Customer = %q, want the recorded identity %q, not a recompute", c.Customer, recorded)
	}
	if c.Resources != 3 {
		t.Fatalf("Resources = %d, want 3", c.Resources)
	}
	if !strings.Contains(c.Reason, "drift") {
		t.Fatalf("reason = %q, want it to call out the identity drift explicitly", c.Reason)
	}
}

// ---- helpers ----

func mustCandidate(t *testing.T, rep *Report, prefix string) Candidate {
	t.Helper()
	for _, c := range rep.Candidates {
		if c.Prefix == prefix {
			return c
		}
	}
	t.Fatalf("no candidate for prefix %q in %+v", prefix, rep.Candidates)
	return Candidate{}
}

// A manifest with no region and no dr_region must fail closed to Unknown, not report the
// stack as clean. Returning (0, nil) there would mark a stack nobody queried as having no
// resources left, and clean is neither alerted nor swept, so the leak would be invisible
// permanently.
func TestCountTaggedFailsClosedWhenNoRegionToQuery(t *testing.T) {
	_, _, err := countTagged(context.Background(), map[string]TagAPI{}, []string{"", ""}, "smoke1234-bi")
	if err == nil {
		t.Fatal("expected an error when no region is usable, got nil (a stack would be reported clean without being queried)")
	}
}

// A pod identity association cannot outlive its cluster, so like a security group it is
// never the whole story for a genuinely standing stack. Measured: every deleteAfter-tagged
// association in the account names a cluster that no longer exists.
func TestPodIdentityAssociationAloneIsInsufficient(t *testing.T) {
	if !insufficientAloneTypes["eks:podidentityassociation"] {
		t.Fatal("eks:podidentityassociation must be insufficient-alone evidence")
	}
	if deniedResourceTypes["eks:podidentityassociation"] {
		t.Fatal("it must NOT be denylisted: alongside a live cluster it is ordinary and must count")
	}
}

// ---- round 5: state-digest clearing (category A) ----

// fakeDigestAPI records every DeleteItem call by the exact LockID it was given, so a
// test can assert clearStateDigests never issues one for a plain (non "-md5") id - the
// one thing this function must never do, since that would be force-releasing a real
// state lock unattended.
type fakeDigestAPI struct {
	deleted []string
	err     error
}

func (f *fakeDigestAPI) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	id := in.Key["LockID"].(*ddbtypes.AttributeValueMemberS).Value
	f.deleted = append(f.deleted, id)
	return &dynamodb.DeleteItemOutput{}, nil
}

func TestClearStateDigestsComposesExactMD5KeysFromS3Listing(t *testing.T) {
	f := newFakeJanitorS3()
	bucket := primaryBucket()
	prefix := "run1-min_default/"
	f.put(bucket, prefix+"standard/us-east-1/min/physical/terraform.tfstate", "{}")
	f.put(bucket, prefix+"standard/us-east-1/min/logical/terraform.tfstate", "{}")
	// A non-state object under the same prefix must never generate a digest delete -
	// listStateKeys only picks up keys ending in "/terraform.tfstate".
	f.put(bucket, prefix+"harness-manifest.json", "{}")

	digests := &fakeDigestAPI{}
	deps := JanitorDeps{S3: f, Digests: digests}
	opts := testOptions(time.Now())

	n, err := clearStateDigests(context.Background(), deps, opts, []string{bucket}, prefix)
	if err != nil {
		t.Fatalf("clearStateDigests: %v", err)
	}
	if n != 2 {
		t.Fatalf("cleared = %d, want 2", n)
	}
	want := map[string]bool{
		bucket + "/" + prefix + "standard/us-east-1/min/physical/terraform.tfstate-md5": true,
		bucket + "/" + prefix + "standard/us-east-1/min/logical/terraform.tfstate-md5":  true,
	}
	if len(digests.deleted) != 2 {
		t.Fatalf("DeleteItem calls = %v, want 2", digests.deleted)
	}
	for _, id := range digests.deleted {
		if !strings.HasSuffix(id, "-md5") {
			t.Fatalf("DeleteItem id %q does not end in -md5 - a plain LockID must never be deleted", id)
		}
		if !want[id] {
			t.Fatalf("unexpected DeleteItem id %q, want one of %v", id, want)
		}
	}
}

// TestSweepClearsStateDigestsBeforeDestroy proves the report-mode gate is placement,
// not a flag: Digests is wired, and Sweep (a real sweep, not a dry-run report) is the
// only path that ever reaches clearStateDigests.
func TestSweepClearsStateDigestsBeforeDestroy(t *testing.T) {
	f := newFakeJanitorS3()
	bucket := primaryBucket()
	prefix := "run1-min_default/"
	f.put(bucket, prefix+"standard/us-east-1/min/physical/terraform.tfstate", "{}")

	rep := &Report{Candidates: []Candidate{
		{Prefix: prefix, Bucket: bucket, RunID: "run1", ConfigName: "min_default", Identifier: "smokeaa-min", State: StateOrphan, Resources: 3, Customer: "smokeaa", Region: testRegion, DRRegion: testDR},
	}}
	digests := &fakeDigestAPI{}
	deps := JanitorDeps{S3: f, Matrix: testMatrix(), Digests: digests, Tags: noResidueTags(), Teardown: func(context.Context, PhaseParams, bool) error { return nil }}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(digests.deleted) != 1 {
		t.Fatalf("digests.deleted = %v, want 1 entry cleared before the destroy", digests.deleted)
	}
	if rep.Candidates[0].SweepResult != "destroyed" {
		t.Fatalf("SweepResult = %q, want destroyed", rep.Candidates[0].SweepResult)
	}
}

// TestScanNeverClearsDigestsEvenWithDigestsWired proves Scan (report mode) never
// reaches clearStateDigests regardless of what is wired - the gate is that only Sweep
// calls it, not a conditional inside Scan that could be gotten wrong.
func TestScanNeverClearsDigestsEvenWithDigestsWired(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	f := newFakeJanitorS3()
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
		AppliedCustomer: appliedCustomerFor(t, "min_default", "run1"),
	})
	f.put(primaryBucket(), "run1-min_default/standard/us-east-1/min/physical/terraform.tfstate", "{}")

	digests := &fakeDigestAPI{}
	tag := &fakeTagAPI{resources: 3}
	deps := JanitorDeps{
		S3: f, Tags: map[string]TagAPI{testRegion: tag, testDR: tag},
		Locks: newFakeLockAPI(), Digests: digests, Matrix: testMatrix(),
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	rep, err := Scan(context.Background(), deps, testOptions(now), JanitorWorkflowList{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := mustCandidate(t, rep, "run1-min_default/").State; got != StateOrphan {
		t.Fatalf("state = %q, want orphan (setup sanity check)", got)
	}
	if len(digests.deleted) != 0 {
		t.Fatalf("digests.deleted = %v, want 0 - Scan must never clear a digest", digests.deleted)
	}
}

// ---- round 5: residue targeted reclaim by exact ARN (category C) ----

// fakeSequencedTagAPI returns a different byType inventory on each successive call,
// modeling the "before reclaim" and "after reclaim" tag-query snapshots Sweep's residue
// path takes. errOnCall, when >= 0, makes that 0-indexed call return an error instead.
type fakeSequencedTagAPI struct {
	responses []map[string]int
	errOnCall int
	calls     int
}

func (f *fakeSequencedTagAPI) GetResources(_ context.Context, _ *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	idx := f.calls
	f.calls++
	if f.errOnCall >= 0 && idx == f.errOnCall {
		return nil, errors.New("tagging api unavailable")
	}
	if idx >= len(f.responses) {
		idx = len(f.responses) - 1
	}
	byType := f.responses[idx]
	var list []rgtypes.ResourceTagMapping
	n := 0
	for typ, count := range byType {
		service, rtype := typ, typ
		if i := strings.Index(typ, ":"); i >= 0 {
			service, rtype = typ[:i], typ[i+1:]
		}
		for i := 0; i < count; i++ {
			arn := fmt.Sprintf("arn:aws:%s:%s:123456789012:%s/res-%d", service, testRegion, rtype, n)
			list = append(list, rgtypes.ResourceTagMapping{ResourceARN: aws.String(arn)})
			n++
		}
	}
	return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: list}, nil
}

// fakeDMSReclaim records every delete call, and can be told to fail one call kind so a
// test can exercise a PARTIAL reclaim.
type fakeDMSReclaim struct {
	failEndpoint bool
	failSubgrp   bool

	deletedEndpoints []string
	deletedSubgrps   []string
}

func (f *fakeDMSReclaim) DeleteEndpoint(_ context.Context, in *databasemigrationservice.DeleteEndpointInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DeleteEndpointOutput, error) {
	if f.failEndpoint {
		return nil, errors.New("delete endpoint failed")
	}
	f.deletedEndpoints = append(f.deletedEndpoints, aws.ToString(in.EndpointArn))
	return &databasemigrationservice.DeleteEndpointOutput{}, nil
}

func (f *fakeDMSReclaim) DeleteReplicationSubnetGroup(_ context.Context, in *databasemigrationservice.DeleteReplicationSubnetGroupInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DeleteReplicationSubnetGroupOutput, error) {
	if f.failSubgrp {
		return nil, errors.New("delete subnet group failed")
	}
	f.deletedSubgrps = append(f.deletedSubgrps, aws.ToString(in.ReplicationSubnetGroupIdentifier))
	return &databasemigrationservice.DeleteReplicationSubnetGroupOutput{}, nil
}

// TestSweepResidueTargetedReclaimFullClearStaysResidue is the full-clear case: every
// surviving ARN has a registered deleter, all succeed, and the re-verify query comes
// back empty - but the candidate must still read StateResidue, never promoted to
// "destroyed", because that promotion requires a clean FIRST-pass destroy, not a
// second-pass targeted one.
func TestSweepResidueTargetedReclaimFullClearStaysResidue(t *testing.T) {
	tags := &fakeSequencedTagAPI{errOnCall: -1, responses: []map[string]int{
		{"dms:endpoint": 2, "dms:subgrp": 1},
		{},
	}}
	dms := &fakeDMSReclaim{}
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 3,
			Customer: "smoke4879", Region: testRegion,
		},
	}}
	deps := JanitorDeps{
		Matrix:   testMatrix(),
		Tags:     map[string]TagAPI{testRegion: tags},
		DMS:      map[string]DMSReclaimAPI{testRegion: dms},
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateResidue {
		t.Fatalf("State = %q, want residue even after a full targeted clear; sweep_result=%q reason=%q", c.State, c.SweepResult, c.Reason)
	}
	if len(dms.deletedEndpoints) != 2 {
		t.Fatalf("DeleteEndpoint calls = %v, want 2", dms.deletedEndpoints)
	}
	if len(dms.deletedSubgrps) != 1 {
		t.Fatalf("DeleteReplicationSubnetGroup calls = %v, want 1", dms.deletedSubgrps)
	}
	if c.Resources != 0 {
		t.Fatalf("Resources = %d, want 0 after the re-verify found nothing left", c.Resources)
	}
	if rep.Residue != 1 {
		t.Fatalf("rep.Residue = %d, want 1", rep.Residue)
	}
	if rep.Swept != 0 {
		t.Fatalf("rep.Swept = %d, want 0 - residue never counts as a clean success", rep.Swept)
	}
	if !strings.Contains(c.SweepResult, "attempted 3 targeted delete") {
		t.Fatalf("SweepResult = %q, want it to name the attempted-delete count", c.SweepResult)
	}
}

// TestSweepResidueTargetedReclaimPartialClearReportsWhatRemains: the subnet-group
// delete fails, so the re-verify still finds it standing - the candidate must report
// exactly what remains, not claim a full clear.
func TestSweepResidueTargetedReclaimPartialClearReportsWhatRemains(t *testing.T) {
	tags := &fakeSequencedTagAPI{errOnCall: -1, responses: []map[string]int{
		{"dms:endpoint": 2, "dms:subgrp": 1},
		{"dms:subgrp": 1},
	}}
	dms := &fakeDMSReclaim{failSubgrp: true}
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 3,
			Customer: "smoke4879", Region: testRegion,
		},
	}}
	deps := JanitorDeps{
		Matrix:   testMatrix(),
		Tags:     map[string]TagAPI{testRegion: tags},
		DMS:      map[string]DMSReclaimAPI{testRegion: dms},
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateResidue {
		t.Fatalf("State = %q, want residue", c.State)
	}
	if c.Resources != 1 {
		t.Fatalf("Resources = %d, want 1 (the subnet group that failed to delete)", c.Resources)
	}
	if len(dms.deletedEndpoints) != 2 {
		t.Fatalf("DeleteEndpoint calls = %v, want 2 (both endpoints still cleared)", dms.deletedEndpoints)
	}
	if len(dms.deletedSubgrps) != 0 {
		t.Fatalf("DeleteReplicationSubnetGroup calls = %v, want 0 (it was made to fail)", dms.deletedSubgrps)
	}
	if !strings.Contains(c.SweepResult, "1 remain") {
		t.Fatalf("SweepResult = %q, want it to say 1 remains", c.SweepResult)
	}
}

// TestSweepResidueAllEligibleDeletesFailedIsDistinctFromNothingEligible proves that a
// candidate whose targeted deletes were all ELIGIBLE and all FAILED is reported
// differently from one where nothing was eligible for a targeted delete in the first
// place - before this fix both read as the identical "residue: needs manual cleanup",
// discarding the fact that a delete was actually attempted and errored (already logged
// by reclaimResidueByARN, visible in the pod log).
func TestSweepResidueAllEligibleDeletesFailedIsDistinctFromNothingEligible(t *testing.T) {
	tags := &fakeSequencedTagAPI{errOnCall: -1, responses: []map[string]int{
		{"dms:endpoint": 2},
	}}
	dms := &fakeDMSReclaim{failEndpoint: true}
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 3,
			Customer: "smoke4879", Region: testRegion,
		},
	}}
	deps := JanitorDeps{
		Matrix:   testMatrix(),
		Tags:     map[string]TagAPI{testRegion: tags},
		DMS:      map[string]DMSReclaimAPI{testRegion: dms},
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateResidue {
		t.Fatalf("State = %q, want residue", c.State)
	}
	if len(dms.deletedEndpoints) != 0 {
		t.Fatalf("DeleteEndpoint successes = %v, want 0 (every attempt was made to fail)", dms.deletedEndpoints)
	}
	if c.SweepResult == "residue: needs manual cleanup" {
		t.Fatalf("SweepResult = %q, must not read identically to the nothing-was-eligible case", c.SweepResult)
	}
	if !strings.Contains(c.SweepResult, "attempted") || !strings.Contains(c.SweepResult, "all failed") {
		t.Fatalf("SweepResult = %q, want it to say deletes were attempted and all failed", c.SweepResult)
	}
	if rep.Residue != 1 {
		t.Fatalf("rep.Residue = %d, want 1", rep.Residue)
	}
}

// TestSweepResidueReclaimSkipsUnregisteredTypes proves deletion is attempted ONLY for
// ARNs whose type is in residueDeleters - an anchored survivor of a type with no
// registered deleter (rds:cluster here) must be reported, never deleted, and must never
// cause a DMS call to be issued at all.
func TestSweepResidueReclaimSkipsUnregisteredTypes(t *testing.T) {
	tags := &fakeSequencedTagAPI{errOnCall: -1, responses: []map[string]int{
		{"rds:cluster": 1},
	}}
	dms := &fakeDMSReclaim{}
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 3,
			Customer: "smoke4879", Region: testRegion,
		},
	}}
	deps := JanitorDeps{
		Matrix:   testMatrix(),
		Tags:     map[string]TagAPI{testRegion: tags},
		DMS:      map[string]DMSReclaimAPI{testRegion: dms},
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateResidue {
		t.Fatalf("State = %q, want residue", c.State)
	}
	if len(dms.deletedEndpoints) != 0 || len(dms.deletedSubgrps) != 0 {
		t.Fatalf("a type with no registered deleter must never trigger a DMS call: endpoints=%v subgrps=%v", dms.deletedEndpoints, dms.deletedSubgrps)
	}
	if c.SweepResult != "residue: needs manual cleanup" {
		t.Fatalf("SweepResult = %q, want the plain unattempted-reclaim message", c.SweepResult)
	}
	// Only one tag query should have run at all: with nothing to attempt, there is
	// nothing to re-verify.
	if tags.calls != 1 {
		t.Fatalf("tag query calls = %d, want 1 (no re-verify when nothing was attempted)", tags.calls)
	}
}

// TestSweepResidueReclaimPostVerifyFailureFailsClosedToUnknown: the targeted delete
// succeeds, but the re-verify tag query itself fails - this must never be read as
// success. StateUnknown, not StateResidue and not "destroyed".
func TestSweepResidueReclaimPostVerifyFailureFailsClosedToUnknown(t *testing.T) {
	tags := &fakeSequencedTagAPI{errOnCall: 1, responses: []map[string]int{
		{"dms:endpoint": 1},
	}}
	dms := &fakeDMSReclaim{}
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 3,
			Customer: "smoke4879", Region: testRegion,
		},
	}}
	deps := JanitorDeps{
		Matrix:   testMatrix(),
		Tags:     map[string]TagAPI{testRegion: tags},
		DMS:      map[string]DMSReclaimAPI{testRegion: dms},
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := rep.Candidates[0]
	if c.State != StateUnknown {
		t.Fatalf("State = %q, want unknown - a failed re-verify must never be read as success", c.State)
	}
	if len(dms.deletedEndpoints) != 1 {
		t.Fatalf("DeleteEndpoint calls = %v, want 1 (the delete itself still happened)", dms.deletedEndpoints)
	}
	if rep.Residue != 0 {
		t.Fatalf("rep.Residue = %d, want 0 - an unknown outcome must not count as residue", rep.Residue)
	}
	if rep.Swept != 0 {
		t.Fatalf("rep.Swept = %d, want 0", rep.Swept)
	}
}

// TestSweepResidueReclaimUsesARNsOwnRegionNotCandidatePrimary: the residue ARN's own
// region is the DR region, not the candidate's primary Region field. Only a DMS client
// keyed by the ARN's region must be used - proving reclaimResidueByARN never assumes
// the candidate's primary region.
func TestSweepResidueReclaimUsesARNsOwnRegionNotCandidatePrimary(t *testing.T) {
	tags := &fakeSequencedTagAPI{errOnCall: -1, responses: []map[string]int{
		{"dms:endpoint": 1},
		{},
	}}
	primaryDMS := &fakeDMSReclaim{}
	drDMS := &fakeDMSReclaim{}
	rep := &Report{Candidates: []Candidate{
		{
			Prefix: "smoke4879-bi/", Bucket: primaryBucket(), RunID: "smoke4879", ConfigName: "min_default",
			Identifier: "smoke4879-bi", State: StateOrphan, Resources: 1,
			Customer: "smoke4879", Region: testDR, // candidate primary region IS the DR region here
		},
	}}
	deps := JanitorDeps{
		Matrix:   testMatrix(),
		Tags:     map[string]TagAPI{testDR: tags},
		DMS:      map[string]DMSReclaimAPI{testRegion: primaryDMS, testDR: drDMS},
		Teardown: func(context.Context, PhaseParams, bool) error { return nil },
	}
	opts := testOptions(time.Now())
	opts.MaxSweeps = 1

	if err := Sweep(context.Background(), deps, opts, rep); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	// fakeSequencedTagAPI always builds its ARNs in testRegion (see the fake), so the
	// delete must land on the testRegion client even though the candidate's own
	// "primary" Region field here is testDR - proving the dispatch reads the ARN, not
	// the candidate.
	if len(primaryDMS.deletedEndpoints) != 1 {
		t.Fatalf("primary-region DMS client calls = %v, want 1 (the ARN says testRegion)", primaryDMS.deletedEndpoints)
	}
	if len(drDMS.deletedEndpoints) != 0 {
		t.Fatalf("DR-region DMS client calls = %v, want 0", drDMS.deletedEndpoints)
	}
}
