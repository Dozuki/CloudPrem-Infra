package validation

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// The sentinel side of the phase-2 DR data proof: rows written to the PRIMARY before
// promotion, counted on the PROMOTED cluster afterwards by the in-VPC probe. Same marker
// pattern the harness already trusts for S3 (continuity sentinel, DR canaries), aimed at
// the database.
//
// Writes go through the running app pod exactly like the schema validator's reads: the
// image's own db_helpers.sh supplies the credentials, so the harness never handles the
// password on the write side, and the write provably uses the same DB identity and path
// the application does. The rows carry the DB server's own NOW(3) so the age math on the
// probe side compares Aurora clocks with Aurora clocks.

// WriteDRHeartbeats writes `rows` timestamped rows for runID into harness_dr.heartbeat on
// the primary, one per interval. Returns the count written. The spread matters: a single
// row proves replication happened at some point; a train of rows ending seconds before
// promotion bounds how far behind the secondary could have been.
func WriteDRHeartbeats(kubeconfig, namespace, runID string, rows int, interval time.Duration) (int, error) {
	pod, err := appPod(kubeconfig, namespace)
	if err != nil {
		return 0, fmt.Errorf("dr-sentinel: %w", err)
	}

	setup := `
. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd -e "CREATE DATABASE IF NOT EXISTS harness_dr" || exit 93
$mysqlcmd harness_dr -e "CREATE TABLE IF NOT EXISTS heartbeat (
  id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  run_id VARCHAR(64) NOT NULL,
  wrote_at DATETIME(3) NOT NULL,
  KEY run_id (run_id)
)" || exit 94
`
	if out, err := execInAppPod(kubeconfig, namespace, pod, setup); err != nil {
		return 0, fmt.Errorf("dr-sentinel: setup failed: %w (%s)", err, firstLine(out))
	}

	written := 0
	for i := 0; i < rows; i++ {
		insert := fmt.Sprintf(`
. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd harness_dr -e "INSERT INTO heartbeat (run_id, wrote_at) VALUES ('%s', NOW(3))" || exit 95
`, runID)
		if out, err := execInAppPod(kubeconfig, namespace, pod, insert); err != nil {
			// A partial train is still evidence; report what landed and let the caller
			// decide. Losing the app pod mid-train is a run problem, not a DR problem.
			return written, fmt.Errorf("dr-sentinel: heartbeat %d/%d failed: %w (%s)", i+1, rows, err, firstLine(out))
		}
		written++
		if i < rows-1 {
			time.Sleep(interval)
		}
	}
	logStep("dr-sentinel: %d heartbeats written to the primary for run %s", written, runID)
	return written, nil
}

// PrimaryHeartbeatCount reads back the count on the PRIMARY, immediately before
// promotion. Comparing the probe's post-promotion count against this (rather than against
// rows-requested) keeps the proof honest if a heartbeat insert half-failed.
func PrimaryHeartbeatCount(kubeconfig, namespace, runID string) (int, error) {
	pod, err := appPod(kubeconfig, namespace)
	if err != nil {
		return 0, err
	}
	script := fmt.Sprintf(`
. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd -sN harness_dr -e "SELECT COUNT(*) FROM heartbeat WHERE run_id='%s'"
`, runID)
	out, err := execInAppPod(kubeconfig, namespace, pod, script)
	if err != nil {
		return 0, fmt.Errorf("dr-sentinel: primary count: %w (%s)", err, firstLine(out))
	}
	n, cerr := strconv.Atoi(strings.TrimSpace(lastNonEmptyLine(out)))
	if cerr != nil {
		return 0, fmt.Errorf("dr-sentinel: primary count unparseable: %q", firstLine(out))
	}
	return n, nil
}

func execInAppPod(kubeconfig, namespace, pod, script string) (string, error) {
	out, err := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"exec", pod, "-c", appContainer, "--", "bash", "-c", script).CombinedOutput()
	return string(out), err
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "mysql:") {
			return l
		}
	}
	return ""
}
