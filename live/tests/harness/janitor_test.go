package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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

func testOptions(now time.Time) JanitorOptions {
	return JanitorOptions{
		AccountID: testAccount, Region: testRegion, DRRegion: testDR,
		LockTable: "dozuki-terraform-lock",
		Grace:     6 * time.Hour, LockFresh: 4 * time.Hour,
		MaxSweeps: 1,
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

// ---- G8: tag lookup failure ----

func TestTagLookupErrorIsUnknownAndNeverSwept(t *testing.T) {
	f := newFakeJanitorS3()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	seedManifest(t, f, primaryBucket(), "run1-min_default/", &RunManifest{
		ConfigName: "min_default", DeleteAfter: now.Add(-10 * time.Hour).Format(time.RFC3339),
		Region: testRegion, DRRegion: testDR,
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
	calls int
	fail  bool
}

func (r *teardownRecorder) teardown(context.Context, PhaseParams, bool) error {
	r.calls++
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
