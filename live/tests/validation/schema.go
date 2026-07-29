package validation

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Schema drift check for FRESH installs.
//
// The greenfield bootstrap pipes the schema dumps straight into the mysql client, which
// stops at the first error. A statement that the engine rejects therefore truncates
// everything after it — and the failure does not survive to be noticed: the Job retries,
// db_initialize.sh sees the `sites` database already exists, skips the whole greenfield
// block, exits 0, and the Job reports Complete. Nothing downstream can tell the
// difference between "migrated" and "migrated 2% of the way and gave up".
//
// That is exactly how the MySQL 8.4 restrict_fk_on_non_standard_key breakage shipped: a
// fresh 8.4 stack applied green with 4 of 225 guide tables and only announced itself
// hours later as a 500 on /Login (`Table 'password_requirements_revisions' does not
// exist`). Cluster health, HelmRelease readiness and Job status were all green
// throughout, because every one of them was asking a question this failure does not
// answer.
//
// So ask the direct question instead: does the database contain every table the dump it
// was built from declares? The dump is read out of the RUNNING pod rather than from this
// repo, so the comparison is always against the schema of the image actually deployed.
//
// Fresh-only on purpose. An upgraded stack's tables come from years of accumulated
// migrations, not from these dumps — drops and renames are legitimate there, so the same
// comparison would be noise. On a greenfield install the dump is the whole story.

// schemaExpectation pairs a dump in the app image with the database it seeds.
// Paths are relative to /home/ifixit/Code, matching db_initialize.sh's own `cd`.
type schemaExpectation struct {
	dump     string
	database string
}

// The databases the bootstrap loads a dump into WHOLE. Only these can be compared against
// a dump; anything seeded some other way would report a false shortfall.
//
// Derived from the bootstrap the APP IMAGE actually runs, which is not the copy of
// db_initialize.sh in the monolith repo — the repo's LocalDev overlay creates `metrics`
// and drives tenant creation through Exec/sites.php, while the shipped image runs a fixed
// sequence of mysql calls. Read the db-migrations log from a real run before adding an
// entry here; the image is authoritative and the two have already diverged.
//
// What that sequence does, observed:
//
//	create database sites        + 1 import   -> full sites.sql
//	create database dozuki_guide + 3 imports  -> NOT the guide dump (a smaller seed set)
//	create database onprem_guide + 1 import   -> full ifixit_guide.sql
//
// so `metrics` (never created) and `dozuki_guide` (loaded with something else) are both
// out. onprem_guide is the one that matters most anyway: it is the tenant the deploy FQDN
// routes to, and its truncation is what produced the 500s.
//
// dozuki_guide is left UNVERIFIED rather than asserted-correct. Its 3 tables are
// consistent with 3 small seed imports, but nothing here proves that is the intended
// content, and it is not covered by any check.
func greenfieldSchemas() []schemaExpectation {
	return []schemaExpectation{
		{dump: "Migrations/SchemaSQL/sites.sql", database: "sites"},
		{dump: "Migrations/SchemaSQL/ifixit_guide.sql", database: "onprem_guide"},
	}
}

// AssertFreshSchemaComplete fails if any table declared by a greenfield dump is missing
// from the database that dump seeds. Extra tables are ignored: migrations legitimately
// add tables the dump predates, and the failure mode being guarded against is truncation.
func AssertFreshSchemaComplete(kubeconfig, namespace string) error {
	pod, err := appPod(kubeconfig, namespace)
	if err != nil {
		return err
	}

	var problems []string
	for _, s := range greenfieldSchemas() {
		want, got, err := schemaTables(kubeconfig, namespace, pod, s)
		if err != nil {
			return fmt.Errorf("%s: %w", s.database, err)
		}
		if len(want) == 0 {
			return fmt.Errorf("%s: parsed 0 tables from %s in the image — the dump moved or the parse broke, so this check is not actually verifying anything", s.database, s.dump)
		}
		if len(got) == 0 {
			problems = append(problems, fmt.Sprintf("%s: database is empty or absent (expected %d tables from %s)", s.database, len(want), s.dump))
			continue
		}
		if missing := missingFrom(want, got); len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s: %d/%d tables missing, e.g. %s",
				s.database, len(missing), len(want), strings.Join(sample(missing, 8), ", ")))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("greenfield schema incomplete (the migration Job reports success but the schema is truncated):\n  %s",
			strings.Join(problems, "\n  "))
	}
	return nil
}

// schemaTables returns the table names the dump declares and the table names the database
// actually has. Both come out of the pod in a single exec: the dump is read from the
// image, and the credentials come from the image's own db_helpers.sh rather than being
// threaded in from Terraform, so this reads the same database the migrations wrote to by
// construction.
func schemaTables(kubeconfig, namespace, pod string, s schemaExpectation) (want, got []string, err error) {
	// db_helpers.sh opens with `set -exou pipefail`; turn xtrace and nounset back off
	// after sourcing or the traces land in stdout and unset vars abort the shell.
	script := fmt.Sprintf(`
cd /home/ifixit/Code || exit 90
[ -f %[1]q ] || exit 91
. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
echo "---WANT---"
sed -n 's/^CREATE TABLE `+"`"+`\([^`+"`"+`]*\)`+"`"+`.*/\1/p' %[1]q
echo "---GOT---"
$mysqlcmd -sN -e "SELECT TABLE_NAME FROM information_schema.tables WHERE TABLE_SCHEMA='%[2]s'" 2>/dev/null
`, s.dump, s.database)

	out, err := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"exec", pod, "-c", appContainer, "--", "bash", "-c", script).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			switch ee.ExitCode() {
			case 90:
				return nil, nil, fmt.Errorf("/home/ifixit/Code missing in the app image")
			case 91:
				return nil, nil, fmt.Errorf("schema dump %s missing in the app image (it moved — update greenfieldSchemas)", s.dump)
			case 92:
				return nil, nil, fmt.Errorf("could not source /bootstrap/helpers/db_helpers.sh in the app image")
			}
			return nil, nil, fmt.Errorf("exec in %s: %v: %s", pod, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, nil, fmt.Errorf("exec in %s: %w", pod, err)
	}

	want, got = parseSchemaOutput(string(out))
	return want, got, nil
}

// parseSchemaOutput splits the exec output on its two markers. Anything before the first
// marker is discarded: the app image's shell profile and db_helpers.sh's own tracing can
// print before our first echo, and treating that as table names would silently invent
// expectations.
func parseSchemaOutput(out string) (want, got []string) {
	section := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "---WANT---" || line == "---GOT---":
			section = line
		case line == "":
		case section == "---WANT---":
			want = append(want, line)
		case section == "---GOT---":
			got = append(got, line)
		}
	}
	return want, got
}

// The chart labels the app deployment app=app and names its container app
// (deployments.app.name); the pod is dozuki-app-deployment-*. Confirmed against a live
// cluster, not inferred from the template — the obvious guesses (app=dozuki-app,
// container dozuki-app) are both wrong and would fail at exec time, deep into a run.
const (
	appSelector  = "app=app"
	appContainer = "app"
)

// appPod returns a running app pod to exec into. The migration pods would be the
// more obvious choice but they are Completed, and exec needs Running.
func appPod(kubeconfig, namespace string) (string, error) {
	out, err := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"get", "pods", "-l", appSelector, "--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[0].metadata.name}").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("no running app pod (%s) in %s to inspect the schema from", appSelector, namespace)
	}
	return strings.TrimSpace(string(out)), nil
}

// appPods returns every running app pod, so a caller that gets killed inside one
// container can try another instead of retrying into the same one forever. appPod
// deliberately always returns items[0], which is right for a read-only inspection but
// useless as a retry target.
func appPods(kubeconfig, namespace string) ([]string, error) {
	out, err := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace,
		"get", "pods", "-l", appSelector, "--field-selector=status.phase=Running",
		"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`).Output()
	if err != nil {
		return nil, fmt.Errorf("listing app pods (%s) in %s: %w", appSelector, namespace, err)
	}
	var pods []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			pods = append(pods, l)
		}
	}
	if len(pods) == 0 {
		return nil, fmt.Errorf("no running app pod (%s) in %s", appSelector, namespace)
	}
	return pods, nil
}

func missingFrom(want, got []string) []string {
	have := make(map[string]struct{}, len(got))
	for _, g := range got {
		have[strings.ToLower(g)] = struct{}{}
	}
	var missing []string
	for _, w := range want {
		if _, ok := have[strings.ToLower(w)]; !ok {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	return missing
}

func sample(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("… (+%d more)", len(s)-n))
}
