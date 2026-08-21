package harness

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ArtifactsDir is the per-run artifacts directory that run.sh bundles + uploads to S3.
func ArtifactsDir(repoDir, runID string) string {
	return filepath.Join(repoDir, "live", "tests", ".artifacts", runID)
}

// captureDiagnostics writes run artifacts (.artifacts/<RUN_ID>/) BEFORE the deferred
// teardown destroys the cluster + removes the worktrees: always the TF inventory, the
// run inputs (env.hcl), and the refs; on failure (full) also a live-cluster dump (pod
// states, events, failed-pod logs, gateway status, rendered configmaps) — the data
// that's gone once Destroy runs. Best-effort: never blocks or fails teardown.
func captureDiagnostics(p RunParams, region, cluster string, full bool, toTG TGOptions, fromEnvHCL, toEnvHCL string) {
	dir := ArtifactsDir(p.RepoDir, p.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, ">> capture: mkdir %s: %v\n", dir, err)
		return
	}

	outcome := "passed"
	if full {
		outcome = "FAILED"
	}
	_ = os.WriteFile(filepath.Join(dir, "refs.txt"), []byte(fmt.Sprintf(
		"run_id=%s\nconfig=%s\nfrom_ref=%s\nto_ref=%s\noutcome=%s\ncaptured=%s\n",
		p.RunID, p.ConfigName, p.FromRef, p.ToRef, outcome, time.Now().Format(time.RFC3339))), 0o644)

	// Run inputs — copied before the worktrees are removed.
	copyFileBestEffort(fromEnvHCL, filepath.Join(dir, "from-env.hcl"))
	copyFileBestEffort(toEnvHCL, filepath.Join(dir, "to-env.hcl"))

	// TF inventory — `state list` has no values; `output` renders sensitive as <sensitive>.
	captureTG(toTG, "physical", dir)
	captureTG(toTG, "logical", dir)

	// Live-cluster dump — only on failure (the high-value, gone-after-teardown data).
	// Teardown's own dump: writes to cluster/, distinct from the failure-time dump
	// (captureFailureDump, cluster-atfailure/) Provision/Validate take earlier — both
	// are kept, see WS2 in run.go's phaseParamsFromRun doc.
	if full && cluster != "" {
		if derr := dumpCluster(p.RepoDir, filepath.Join(dir, "cluster"), cluster, region, p.Profile, p.Namespace); derr != nil {
			fmt.Fprintf(os.Stderr, ">> capture: cluster dump failed: %v\n", derr)
		}
	}
	fmt.Fprintf(os.Stderr, "\n>> [harness %s] diagnostics captured -> %s\n", time.Now().Format("15:04:05"), dir)

	// S3 upload — FAILURE PATHS ONLY (WS2). A passing run (full=false) uploads nothing,
	// same as before this change: only a failed run's artifacts are worth the network
	// trip and the bucket storage. This is teardown's own upload, once destroy and the
	// residual check have finished writing everything else into .artifacts/<run-id>/;
	// Provision and Validate call uploadArtifactsOnFailure directly on their own
	// validateStack failure (see captureFailureDump), before teardown ever runs — an
	// Argo run has those as separate pods, so both call sites are load-bearing, not
	// redundant. Later uploads under the same run-id prefix add/overwrite by key, so
	// this one only ever adds to what the earlier call already put there.
	if full {
		uploadArtifactsOnFailureFn(p.RepoDir, p.RunID, p.AccountID, p.Profile, region)
	}
}

// uploadArtifactsOnFailureFn is uploadArtifactsOnFailure behind a package-level
// indirection so tests can substitute a spy for it: uploadArtifactsOnFailure builds a
// real AWS client and makes a real PutObject call, which a unit test must never do
// (no credentials in CI, and it would make captureDiagnostics's full=true path
// untestable without live AWS access). Production code never reassigns this — only
// *_test.go files do, always restoring it via t.Cleanup.
var uploadArtifactsOnFailureFn = uploadArtifactsOnFailure

// captureTG dumps `terragrunt state list` + `terragrunt output` for one module (no
// secrets: state list has no values, output redacts sensitive). Best-effort.
func captureTG(o TGOptions, module, dir string) {
	md := filepath.Join(o.WorkingDir, module)
	if _, err := os.Stat(md); err != nil {
		return
	}
	for name, args := range map[string][]string{
		// `output` is one of terragrunt's shortcut commands, but `state` is not, and
		// since v0.88.0 terragrunt no longer forwards unrecognised commands to tofu -
		// it errors with "unknown command" instead. Non-shortcut subcommands have to
		// go through `run --`.
		"state-list": {"run", "--", "state", "list"},
		"output":     {"output"},
	} {
		cmd := exec.Command("terragrunt", args...)
		cmd.Dir = md
		cmd.Env = o.env()
		out, _ := cmd.CombinedOutput()
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("tf-%s-%s.txt", module, name)), out, 0o644)
	}
}

func copyFileBestEffort(src, dst string) {
	b, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.WriteFile(dst, b, 0o644)
}

// clusterDumpTimeout bounds one capture-cluster.sh invocation. The script's own steps
// (kubectl get/describe/logs against a wedged API server) have no timeout of their
// own, so without this a hung cluster turns a diagnostic capture into a stuck phase —
// exactly the failure mode the capture exists to explain, not reproduce.
const clusterDumpTimeout = 10 * time.Minute

// dumpCluster runs capture-cluster.sh unmodified against outdir, wrapped in
// clusterDumpTimeout. Shared by captureDiagnostics' teardown dump (cluster/) and
// captureFailureDump's provision/validate dump (cluster-atfailure/) — the two callers
// differ only in WHEN they run and which subdirectory they write to, never in how the
// script itself is invoked.
func dumpCluster(repoDir, outdir, cluster, region, profile, namespace string) error {
	sh := filepath.Join(repoDir, "live", "tests", "capture-cluster.sh")
	ctx, cancel := context.WithTimeout(context.Background(), clusterDumpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", sh, outdir, cluster, region, profile, namespace)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// captureFailureDump writes a live-cluster diagnostic dump to
// .artifacts/<run-id>/cluster-atfailure BEFORE a Provision or Validate failure
// propagates and teardown destroys the cluster — the same high-value dump
// captureDiagnostics takes at teardown, just early enough to survive whatever
// validateStack's check just failed on (an Argo run's provision/validate pod exits
// on this error long before the separate teardown pod ever starts).
//
// identifier is the EKS cluster name (see Teardown's captureDiagnostics call site:
// "identifier IS the EKS cluster name", not a general customer/env label). An empty
// identifier means no stack was ever identified as applied, so there is nothing to
// dump — logged and skipped rather than treated as an error.
//
// Best-effort and silent-on-success beyond one step() line: a dump failure must never
// mask or block on top of the real validateStack error that triggered the call.
func captureFailureDump(repoDir, runID, identifier, region, profile, namespace string) {
	if identifier == "" {
		step("WARNING: failure-time cluster dump skipped: no cluster identifier resolved")
		return
	}
	dir := filepath.Join(ArtifactsDir(repoDir, runID), "cluster-atfailure")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		step("WARNING: failure-time cluster dump: mkdir %s: %v", dir, err)
		return
	}
	if err := dumpCluster(repoDir, dir, identifier, region, profile, namespace); err != nil {
		step("WARNING: failure-time cluster dump failed: %v", err)
		return
	}
	step("failure-time cluster dump captured -> %s", dir)
}

// artifactsUploadCapBytes bounds one uploadArtifacts invocation. A hung run's per-pod
// log capture (capture-cluster.sh's logs/ dir, --tail=400 per container but uncapped
// in count) can run to hundreds of MB; the cap keeps one bad run from turning into an
// unbounded upload while the many small high-value text files (events, describe-pods,
// refs.txt, tf outputs, upload-manifest.txt itself) stay well inside it — see
// uploadArtifacts' smallest-first ordering for how that's guaranteed rather than
// merely likely.
const artifactsUploadCapBytes = 200 * 1024 * 1024

// artifactsUploadTimeout bounds the whole upload invocation (walk + every PutObject +
// the manifest write), same reasoning as clusterDumpTimeout: a stalled network call
// must not hang the phase that is already failing for the real reason.
const artifactsUploadTimeout = 10 * time.Minute

// uploadArtifactsOnFailure builds an S3 client for accountID/region (same wiring
// phaseParamsFromRun uses for the manifest store: awsConfigFor + s3.NewFromConfig +
// stateBucket) and uploads .artifacts/<run-id>/ to the harness state bucket under
// artifacts/<run-id>/. Called only from the two failure paths WS2 defines
// (captureFailureDump's callers, and captureDiagnostics when full=true) — never on a
// green run. Best-effort: every failure is logged via step() and swallowed.
func uploadArtifactsOnFailure(repoDir, runID, accountID, profile, region string) {
	ctx, cancel := context.WithTimeout(context.Background(), artifactsUploadTimeout)
	defer cancel()
	awsCfg, err := awsConfigFor(ctx, profile, region)
	if err != nil {
		step("WARNING: artifacts upload failed: aws config: %v", err)
		return
	}
	bucket := stateBucket(accountID, region)
	uploadArtifacts(ctx, s3.NewFromConfig(awsCfg), bucket, ArtifactsDir(repoDir, runID), runID, artifactsUploadCapBytes)
}

// artifactFile is one file discovered under a run's local .artifacts/<run-id>/ tree,
// with the byte size uploadArtifacts sorts and caps on.
type artifactFile struct {
	rel  string // path relative to localDir, forward-slash for the S3 key
	abs  string
	size int64
}

// uploadArtifacts uploads every file under localDir to bucket under
// "artifacts/<runID>/", mirroring localDir's relative paths, in smallest-first order,
// capped at capBytes total (uploadArtifactsOnFailure always passes
// artifactsUploadCapBytes; capBytes is a parameter rather than reading the constant
// directly so the cap/ordering/manifest behavior is unit-testable with tiny files
// instead of needing real 200MB fixtures). Sorting smallest-first before applying the
// cap is what makes "the many small high-value text files always survive" a guarantee
// rather than a hope: once a file doesn't fit the remaining budget, every later file in
// the (ascending) order is the same size or larger, so it can never fit either — the
// loop can and does stop there instead of checking each one individually.
//
// upload-manifest.txt is written LAST, after every other object has either uploaded or
// been recorded as skipped: its presence in the bucket is the signal an upload ran to
// completion; a listing that lacks it means the upload never finished (or never ran).
// Every skipped file — capped, or a real read/PutObject error — is named both in a
// step() line as it happens and in the manifest's SKIPPED section: no silent
// truncation, whichever way a file fails to make it up.
//
// Best-effort throughout: a single file's read/PutObject failure only skips that file,
// and the manifest's own PutObject failure is reported the same way (step() + return)
// as everything else here — this function never returns an error because the artifacts
// upload must never fail (or fail loud enough to fail) the caller's phase.
func uploadArtifacts(ctx context.Context, client S3API, bucket, localDir, runID string, capBytes int64) {
	var files []artifactFile
	err := filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		rel, rerr := filepath.Rel(localDir, path)
		if rerr != nil {
			return rerr
		}
		files = append(files, artifactFile{rel: rel, abs: path, size: info.Size()})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			// Provision/Validate can fail before captureFailureDump ever wrote anything
			// (e.g. no cluster identifier resolved) — nothing to upload is not a failure.
			return
		}
		step("WARNING: artifacts upload failed: walk %s: %v", localDir, err)
		return
	}
	if len(files) == 0 {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].size < files[j].size })

	prefix := "artifacts/" + runID + "/"
	step("uploading artifacts -> s3://%s/%s", bucket, prefix)

	var included, skipped []string
	var uploaded int64
	capHit := false
	for _, f := range files {
		if capHit || uploaded+f.size > capBytes {
			capHit = true
			note := fmt.Sprintf("%s (%d bytes): skipped, 200MB upload cap reached", f.rel, f.size)
			skipped = append(skipped, note)
			step("artifacts upload: %s", note)
			continue
		}
		b, rerr := os.ReadFile(f.abs)
		if rerr != nil {
			note := fmt.Sprintf("%s: skipped, read failed: %v", f.rel, rerr)
			skipped = append(skipped, note)
			step("artifacts upload: %s", note)
			continue
		}
		key := prefix + filepath.ToSlash(f.rel)
		if _, perr := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(b),
		}); perr != nil {
			note := fmt.Sprintf("%s: skipped, upload failed: %v", f.rel, perr)
			skipped = append(skipped, note)
			step("WARNING: artifacts upload failed: %s: %v", key, perr)
			continue
		}
		included = append(included, fmt.Sprintf("%s (%d bytes)", f.rel, f.size))
		uploaded += f.size
	}

	var manifest strings.Builder
	fmt.Fprintf(&manifest, "run_id=%s\nbucket=%s\nprefix=%s\ncap_bytes=%d\nuploaded_bytes=%d\nfiles_included=%d\nfiles_skipped=%d\ncaptured=%s\n\n",
		runID, bucket, prefix, capBytes, uploaded, len(included), len(skipped), time.Now().Format(time.RFC3339))
	manifest.WriteString("INCLUDED:\n")
	for _, s := range included {
		fmt.Fprintf(&manifest, "  %s\n", s)
	}
	manifest.WriteString("SKIPPED:\n")
	for _, s := range skipped {
		fmt.Fprintf(&manifest, "  %s\n", s)
	}

	mkey := prefix + "upload-manifest.txt"
	if _, merr := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(mkey),
		Body: strings.NewReader(manifest.String()), ContentType: aws.String("text/plain"),
	}); merr != nil {
		step("WARNING: artifacts upload failed: manifest write %s: %v", mkey, merr)
		return
	}
	step("artifacts uploaded -> s3://%s/%s (%d file(s), %d bytes; %d skipped)", bucket, prefix, len(included), uploaded, len(skipped))
}
