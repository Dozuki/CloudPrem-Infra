package harness

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// recordingS3 is a PutObject-order-tracking S3API fake, distinct from manifest_s3_test.go's
// fakeS3: WS2's manifest-written-last invariant depends on call ORDER, which a plain map
// can't observe, so this fake also keeps a []string of keys in the order PutObject saw them.
type recordingS3 struct {
	objs  map[string][]byte
	order []string
	// failKeys, when non-empty, makes PutObject return an error for exactly these keys
	// (still recording nothing for them) — used to exercise the "real PutObject error"
	// skip path distinctly from the size-cap skip path.
	failKeys map[string]bool
}

func newRecordingS3() *recordingS3 {
	return &recordingS3{objs: map[string][]byte{}, failKeys: map[string]bool{}}
}

func (f *recordingS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *recordingS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.failKeys[*in.Key] {
		return nil, &smithy.GenericAPIError{Code: "InternalError", Message: "injected failure"}
	}
	b, _ := io.ReadAll(in.Body)
	f.objs[*in.Key] = b
	f.order = append(f.order, *in.Key)
	return &s3.PutObjectOutput{}, nil
}

// writeSizedFile creates path (parents included) containing n arbitrary bytes.
func writeSizedFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), n), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestUploadArtifactsDumpDirAbsentIsNoOp(t *testing.T) {
	f := newRecordingS3()
	uploadArtifacts(context.Background(), f, "bucket", filepath.Join(t.TempDir(), "does-not-exist"), "run1-min", artifactsUploadCapBytes)
	if len(f.objs) != 0 {
		t.Fatalf("expected no PutObject calls for an absent dump dir, got %v", f.order)
	}
}

func TestUploadArtifactsEmptyDirIsNoOp(t *testing.T) {
	f := newRecordingS3()
	uploadArtifacts(context.Background(), f, "bucket", t.TempDir(), "run1-min", artifactsUploadCapBytes)
	if len(f.objs) != 0 {
		t.Fatalf("expected no PutObject calls for an empty dump dir, got %v", f.order)
	}
}

func TestUploadArtifactsCapOrderingAndManifest(t *testing.T) {
	dir := t.TempDir()
	// Three files: two small ones that together fit a 15-byte cap, one large one that
	// does not. Smallest-first ordering must upload both small files and skip the large
	// one, even though the large file sorts last alphabetically too (so alpha order
	// would have given the same result by accident) — use out-of-alpha-order names to
	// rule that out.
	writeSizedFile(t, filepath.Join(dir, "z-small-a.txt"), 5)
	writeSizedFile(t, filepath.Join(dir, "a-small-b.txt"), 5)
	writeSizedFile(t, filepath.Join(dir, "m-large.txt"), 50)

	f := newRecordingS3()
	const cap = 15
	uploadArtifacts(context.Background(), f, "state-bucket", dir, "run1-min", cap)

	wantKeys := []string{
		"artifacts/run1-min/z-small-a.txt",
		"artifacts/run1-min/a-small-b.txt",
		"artifacts/run1-min/upload-manifest.txt",
	}
	for _, k := range wantKeys {
		if _, ok := f.objs[k]; !ok {
			t.Errorf("expected key %s to be uploaded, have %v", k, f.order)
		}
	}
	if _, ok := f.objs["artifacts/run1-min/m-large.txt"]; ok {
		t.Errorf("expected the 50-byte file to be skipped under a 15-byte cap, but it was uploaded")
	}

	manifest := string(f.objs["artifacts/run1-min/upload-manifest.txt"])
	if !strings.Contains(manifest, "z-small-a.txt") || !strings.Contains(manifest, "a-small-b.txt") {
		t.Errorf("manifest missing included small files:\n%s", manifest)
	}
	if !strings.Contains(manifest, "m-large.txt") {
		t.Errorf("manifest missing the skipped large file:\n%s", manifest)
	}
	if !strings.Contains(manifest, "cap reached") {
		t.Errorf("manifest missing a cap-reached note for the skipped file:\n%s", manifest)
	}
	if !strings.Contains(manifest, "SKIPPED:") || !strings.Contains(manifest, "INCLUDED:") {
		t.Errorf("manifest missing INCLUDED/SKIPPED sections:\n%s", manifest)
	}
}

func TestUploadArtifactsManifestWrittenLast(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "one.txt"), 1)
	writeSizedFile(t, filepath.Join(dir, "two.txt"), 2)
	writeSizedFile(t, filepath.Join(dir, "three.txt"), 3)

	f := newRecordingS3()
	uploadArtifacts(context.Background(), f, "state-bucket", dir, "run1-min", artifactsUploadCapBytes)

	if len(f.order) == 0 {
		t.Fatal("expected at least one PutObject call")
	}
	last := f.order[len(f.order)-1]
	if last != "artifacts/run1-min/upload-manifest.txt" {
		t.Errorf("upload-manifest.txt must be the LAST object written (its presence signals completion), got order %v", f.order)
	}
}

func TestUploadArtifactsRealErrorIsSkippedAndNamed(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "ok.txt"), 1)
	writeSizedFile(t, filepath.Join(dir, "bad.txt"), 2)

	f := newRecordingS3()
	f.failKeys["artifacts/run1-min/bad.txt"] = true
	uploadArtifacts(context.Background(), f, "state-bucket", dir, "run1-min", artifactsUploadCapBytes)

	if _, ok := f.objs["artifacts/run1-min/ok.txt"]; !ok {
		t.Error("expected ok.txt to upload despite bad.txt's injected failure")
	}
	if _, ok := f.objs["artifacts/run1-min/bad.txt"]; ok {
		t.Error("bad.txt should not have been recorded as uploaded")
	}
	manifest := string(f.objs["artifacts/run1-min/upload-manifest.txt"])
	if !strings.Contains(manifest, "bad.txt") || !strings.Contains(manifest, "upload failed") {
		t.Errorf("manifest must name the PutObject failure, not silently drop it:\n%s", manifest)
	}
}

func TestUploadArtifactsKeysMirrorRelativePaths(t *testing.T) {
	dir := t.TempDir()
	writeSizedFile(t, filepath.Join(dir, "cluster-atfailure", "pods.txt"), 4)
	writeSizedFile(t, filepath.Join(dir, "refs.txt"), 3)

	f := newRecordingS3()
	uploadArtifacts(context.Background(), f, "state-bucket", dir, "myrun-min", artifactsUploadCapBytes)

	wantKeys := []string{
		"artifacts/myrun-min/cluster-atfailure/pods.txt",
		"artifacts/myrun-min/refs.txt",
	}
	got := make([]string, 0, len(f.objs))
	for k := range f.objs {
		got = append(got, k)
	}
	sort.Strings(got)
	for _, w := range wantKeys {
		if _, ok := f.objs[w]; !ok {
			t.Errorf("expected key %s, got keys %v", w, got)
		}
	}
}

func TestCaptureFailureDumpSkipsWhenNoIdentifier(t *testing.T) {
	repoDir := t.TempDir()
	runID := "run1-min"
	captureFailureDump(repoDir, runID, "", "us-east-1", "profile", "dozuki")

	dumpDir := filepath.Join(ArtifactsDir(repoDir, runID), "cluster-atfailure")
	if _, err := os.Stat(dumpDir); !os.IsNotExist(err) {
		t.Errorf("expected no cluster-atfailure dir when identifier is empty, stat err=%v", err)
	}
}

// TestTeardownNeedsFailureCapture pins the codex P1 fix's decision table: a teardown
// error must trigger its own capture+upload exactly when no prior phase already did
// (failed==false), and must NOT re-trigger one when a prior phase already ran the full
// capture (failed==true) — see teardownNeedsFailureCapture's doc comment in phases.go.
func TestTeardownNeedsFailureCapture(t *testing.T) {
	cases := []struct {
		name   string
		failed bool
		want   bool
	}{
		{"green run, teardown itself fails -> needs its own capture", false, true},
		{"already-failed run, teardown also fails -> pre-destroy call already covered it", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := teardownNeedsFailureCapture(tc.failed); got != tc.want {
				t.Errorf("teardownNeedsFailureCapture(%v) = %v, want %v", tc.failed, got, tc.want)
			}
		})
	}
}

// TestCaptureDiagnosticsUploadsOnlyWhenFull pins the other half of the codex P1
// invariant at the captureDiagnostics level: full=false (a fully-green run, or the
// pre-destroy call on a run with nothing failed yet) must call the upload function
// ZERO times, and full=true (what Teardown's new error-path calls use per
// teardownNeedsFailureCapture) must call it. Spies on uploadArtifactsOnFailureFn rather
// than calling the real uploadArtifactsOnFailure, which would make a live AWS
// PutObject call — see that var's doc comment in diagnostics.go.
//
// cluster is left empty and toTG.WorkingDir points at an empty temp dir (no physical/
// logical subdirs) so this never shells out to capture-cluster.sh or terragrunt —
// exactly the "dump-dir/inputs absent" no-op paths captureTG and the cluster-dump
// guard already take on their own.
func TestCaptureDiagnosticsUploadsOnlyWhenFull(t *testing.T) {
	orig := uploadArtifactsOnFailureFn
	t.Cleanup(func() { uploadArtifactsOnFailureFn = orig })

	var calls int
	uploadArtifactsOnFailureFn = func(repoDir, runID, accountID, profile, region string) {
		calls++
	}

	repoDir := t.TempDir()
	rp := RunParams{RepoDir: repoDir, RunID: "run1-min", ConfigName: "min", AccountID: "123456789012", Profile: "test-profile"}
	tg := TGOptions{WorkingDir: t.TempDir()}

	calls = 0
	captureDiagnostics(rp, "us-east-1", "" /* cluster */, false /* full */, tg, "", "")
	if calls != 0 {
		t.Errorf("full=false (green run) must upload nothing, got %d upload call(s)", calls)
	}

	calls = 0
	captureDiagnostics(rp, "us-east-1", "" /* cluster */, true /* full */, tg, "", "")
	if calls != 1 {
		t.Errorf("full=true (failed run) must attempt exactly one upload, got %d", calls)
	}
}

// TestCaptureDiagnosticsSkipsLiveClusterDumpWhenClusterEmpty pins the mechanism the
// codex MAJOR fix (Teardown's final residual gate, phases.go) relies on: by the time
// that gate runs, tg.Destroy() already succeeded and the cluster is gone, so it calls
// captureDiagnostics with cluster="" — the function's existing "skip the kubectl
// portion" signal (see captureDiagnostics' `full && cluster != ""` guard) — while
// everything else (refs.txt, TF inventory, and the S3 upload) still runs.
func TestCaptureDiagnosticsSkipsLiveClusterDumpWhenClusterEmpty(t *testing.T) {
	orig := uploadArtifactsOnFailureFn
	t.Cleanup(func() { uploadArtifactsOnFailureFn = orig })
	var calls int
	uploadArtifactsOnFailureFn = func(repoDir, runID, accountID, profile, region string) { calls++ }

	repoDir := t.TempDir()
	rp := RunParams{RepoDir: repoDir, RunID: "run1-min", ConfigName: "min", AccountID: "123456789012", Profile: "test-profile"}
	tg := TGOptions{WorkingDir: t.TempDir()}

	captureDiagnostics(rp, "us-east-1", "" /* cluster already destroyed by Destroy() */, true, tg, "", "")

	dumpDir := filepath.Join(ArtifactsDir(repoDir, rp.RunID), "cluster")
	if _, err := os.Stat(dumpDir); !os.IsNotExist(err) {
		t.Errorf("expected no cluster/ dump dir when cluster is empty (cluster already destroyed), stat err=%v", err)
	}
	refsPath := filepath.Join(ArtifactsDir(repoDir, rp.RunID), "refs.txt")
	if _, err := os.Stat(refsPath); err != nil {
		t.Errorf("expected refs.txt to still be written even with no cluster dump: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected the upload to still be attempted (full=true) even with cluster empty, got %d call(s)", calls)
	}
}

// TestTeardownResidualGateCapturesOnlyWhenPriorPhasesGreen exercises the same
// teardownNeedsFailureCapture decision the residual-gate branch now uses (phases.go,
// after `destroy reported success but left ... resource(s) behind`), pinned at the
// pure-function level for the same reason as TestTeardownNeedsFailureCapture: Teardown
// itself needs a real terragrunt/AWS/k8s environment to exercise end-to-end.
func TestTeardownResidualGateCapturesOnlyWhenPriorPhasesGreen(t *testing.T) {
	if !teardownNeedsFailureCapture(false) {
		t.Error("a residual-gate failure after a green provision/validate must trigger its own capture+upload")
	}
	if teardownNeedsFailureCapture(true) {
		t.Error("a residual-gate failure after an already-failed run must NOT re-trigger capture+upload (the pre-destroy call already ran full)")
	}
}

func TestArtifactsRunIDMatchesAcrossPhases(t *testing.T) {
	// artifactsRunID must produce the same value Teardown's captureDiagnostics call used
	// to compute inline before WS2 (strings.TrimSuffix(statePrefix, "/")), so the
	// failure-time dump (Provision/Validate) and the teardown dump land under the exact
	// same run-id prefix — see phases.go's Teardown comment.
	p := PhaseParams{RunID: "local-1719"}
	cfg := Config{Name: "min"}
	got := p.artifactsRunID(cfg)
	want := "local-1719-min"
	if got != want {
		t.Errorf("artifactsRunID = %q, want %q", got, want)
	}
}
