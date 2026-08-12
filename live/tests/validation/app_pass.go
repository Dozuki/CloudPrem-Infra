package validation

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------------
// AppPass: the 8-stage in-application API exerciser.
//
// Cluster/HelmRelease health (validateStack) and the schema-completeness check
// (AssertFreshSchemaComplete) both ask "did the deploy converge". Neither asks "can a
// real user actually log in, upload media, publish a guide, and read it back". AppPass
// answers that question by driving the deployed app's own public API through a full
// write/publish/read cycle: mint a throwaway site admin, log in, register a second
// user, create a guide category, upload an image/PDF/video, build a two-step guide
// referencing all three, publish it, and prove the public (anonymous) read path works
// end to end — then create a course wrapping the guide and prove the courses API sees
// it too.
//
// Fail-closed, stages 0-7: any non-2xx status, missing/unparseable response field, or
// a video that never finishes encoding fails the WHOLE pass — no mocked defaults, no
// "continue on error". Stage 8 (teardown) is the sole exception: every entity created
// gets a best-effort delete, failures there are logged and non-fatal, and stage 8 never
// changes the pass's verdict.
// ---------------------------------------------------------------------------------

// AppPassOptions configures a single AppPass run.
type AppPassOptions struct {
	BaseURL    string
	Kubeconfig string
	Namespace  string
	Timeout    time.Duration
}

// appPassTimeout is the ceiling for an entire AppPass run (all 8 stages, including the
// stage-4 video-encoding poll). A zero AppPassOptions.Timeout normalizes to this inside
// RunAppPass — see normalizeAppPassTimeout.
const appPassTimeout = 15 * time.Minute

// execRunner is the injectable boundary for every kubectl invocation AppPass makes.
// argv is everything after the "kubectl" binary name; stdin, when non-nil, is piped to
// the process. The real implementation (kubectlExecRunner) is a thin adapter over
// os/exec; tests inject a fake that records exact argv/stdin so a contract regression —
// including the password leaking into argv instead of staying in stdin — fails loud.
type execRunner func(ctx context.Context, argv []string, stdin []byte) (stdout, stderr []byte, err error)

// RunAppPass exercises the 8-stage pass against the stack described by opts. See the
// package-level doc comment above for what it proves and its fail-closed contract.
func RunAppPass(ctx context.Context, opts AppPassOptions) error {
	opts.Timeout = normalizeAppPassTimeout(opts.Timeout)

	secret, err := generateAppPassSecret()
	if err != nil {
		return fmt.Errorf("app-pass: %w", err)
	}
	runsalt, err := generateAppPassRunSalt()
	if err != nil {
		return fmt.Errorf("app-pass: %w", err)
	}

	log := newAppPassLogger()
	deps := appPassDeps{
		exec:   kubectlExecRunner(),
		client: newAppPassClient(),
		clock:  realAppPassClock{},
	}
	runErr := runAppPass(ctx, opts, deps, log, secret, runsalt)
	return finalizeAppPass(log, runErr, secret)
}

// normalizeAppPassTimeout maps a zero (or negative) timeout to appPassTimeout and
// passes any positive value through unchanged.
func normalizeAppPassTimeout(t time.Duration) time.Duration {
	if t <= 0 {
		return appPassTimeout
	}
	return t
}

// finalizeAppPass is redaction layer (b): before returning ANY result, it scans every
// log line AppPass emitted (plus the final error's own text, if there is one) for the
// exact generated secret. A hit converts the result into a distinct, secret-free
// failure rather than ever letting the raw error (which might carry the leak) escape —
// layers (a)/(c) are the unit tests asserting this and that nothing else is needed.
func finalizeAppPass(log *appPassLogger, runErr error, secret string) error {
	lines := log.Lines()
	if runErr != nil {
		lines = append(lines, runErr.Error())
	}
	for _, l := range lines {
		if strings.Contains(l, secret) {
			return fmt.Errorf("app-pass: SECRET LEAKED TO LOGS")
		}
	}
	return runErr
}

// ---------------------------------------------------------------------------------
// dependency injection: exec, HTTP, clock
// ---------------------------------------------------------------------------------

type appPassDeps struct {
	exec   execRunner
	client *http.Client
	clock  appPassClock
}

// appPassClock abstracts time.Now/time.Sleep so the stage-4 video-encoding poll (5m
// ceiling) and the whole-pass ceiling (appPassTimeout) can be driven instantly in
// tests via a fake that advances a virtual clock on Sleep instead of blocking for real.
type appPassClock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realAppPassClock struct{}

func (realAppPassClock) Now() time.Time        { return time.Now() }
func (realAppPassClock) Sleep(d time.Duration) { time.Sleep(d) }

// execError carries a kubectl exec's exit code and stderr, for callers that need to
// distinguish specific exit codes (mirroring schema.go's exit-code-mapping discipline).
type execError struct {
	ExitCode int
	Stderr   string
}

func (e *execError) Error() string {
	return fmt.Sprintf("kubectl exec exit %d: %s", e.ExitCode, strings.TrimSpace(e.Stderr))
}

// kubectlExecRunner is the real execRunner: a thin adapter over os/exec. kubeconfig,
// namespace, pod etc. are already baked into argv by the callers below (kubectlExecArgv
// / appPassResolvePod), so this needs no parameters of its own.
func kubectlExecRunner() execRunner {
	return func(ctx context.Context, argv []string, stdin []byte) ([]byte, []byte, error) {
		cmd := exec.CommandContext(ctx, "kubectl", argv...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return stdout.Bytes(), stderr.Bytes(), &execError{ExitCode: ee.ExitCode(), Stderr: stderr.String()}
			}
			return stdout.Bytes(), stderr.Bytes(), err
		}
		return stdout.Bytes(), stderr.Bytes(), nil
	}
}

// newAppPassClient builds the HTTP client AppPass uses for every stage-1..7 API call.
//
// InsecureSkipVerify is accepted here ONLY because the credential this client carries
// (the stage-0 generated admin password / stage-1 authToken) is per-run random, valid
// only on a throwaway smoke stack that this same run tears down in stage 8, and grants
// nothing outside it — the same posture endpoints.go documents for CheckEndpoint. If
// AppPass is ever pointed at a static or shared credential, or a stack that outlives
// the run, this must be revisited.
func newAppPassClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

// ---------------------------------------------------------------------------------
// logging
// ---------------------------------------------------------------------------------

// appPassLogger captures every line AppPass emits (for the redaction self-scan in
// finalizeAppPass) in addition to writing it to stderr for a human operator, matching
// the rest of the harness's step()/logStep() convention.
type appPassLogger struct {
	mu    sync.Mutex
	lines []string
}

func newAppPassLogger() *appPassLogger { return &appPassLogger{} }

func (l *appPassLogger) Logf(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	l.mu.Lock()
	l.lines = append(l.lines, line)
	l.mu.Unlock()
	fmt.Fprintf(os.Stderr, ">> [app-pass %s] %s\n", time.Now().Format("15:04:05"), line)
}

// Lines returns a snapshot of every line logged so far.
func (l *appPassLogger) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

// ---------------------------------------------------------------------------------
// secret / identity generation
// ---------------------------------------------------------------------------------

// generateAppPassSecret produces the ONE password AppPass generates and protects: 32
// hex characters (16 random bytes) from crypto/rand. It travels ONLY as stdin to
// `kubectl exec -i` (stage 0) and in the stage-1 login request body — never in argv,
// never in an exec'd script's literal text, never in a log line or error string.
func generateAppPassSecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// generateAppPassRunSalt produces a short random suffix used to namespace this run's
// throwaway identities (emails, the wiki/guide/course titles) so concurrent runs never
// collide. It is not a secret and is safe to log.
func generateAppPassRunSalt() (string, error) {
	buf := make([]byte, 4)
	if _, err := crand.Read(buf); err != nil {
		return "", fmt.Errorf("generate run salt: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// ---------------------------------------------------------------------------------
// small HTTP helpers
// ---------------------------------------------------------------------------------

type apiResult struct {
	status int
	body   []byte
	json   map[string]interface{}
}

// doJSON issues a JSON request (payload may be nil for a bodyless GET/DELETE/PUT) with
// the "Authorization: api <token>" header when authToken is non-empty. Response bodies
// are parsed into a map best-effort; a non-JSON or empty body just leaves json nil,
// which every field extractor below treats as "field absent" (fail-closed).
func (d appPassDeps) doJSON(ctx context.Context, method, rawURL, authToken string, payload interface{}) (*apiResult, error) {
	var body []byte
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		body = b
	}
	contentType := ""
	if payload != nil {
		contentType = "application/json"
	}
	return d.doRaw(ctx, method, rawURL, authToken, body, contentType)
}

// doRaw issues a request with a raw byte body (used for JSON via doJSON and for the
// stage-4 raw-file uploads). Never logs or echoes body/response content — callers must
// only surface status codes and parsed, non-secret field values in errors, never a raw
// payload (see the secret-transport rule in RunAppPass's doc comment).
func (d appPassDeps) doRaw(ctx context.Context, method, rawURL, authToken string, body []byte, contentType string) (*apiResult, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "api "+authToken)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	res := &apiResult{status: resp.StatusCode, body: b}
	if len(b) > 0 {
		var m map[string]interface{}
		if json.Unmarshal(b, &m) == nil {
			res.json = m
		}
	}
	return res, nil
}

func stringField(m map[string]interface{}, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func numberField(m map[string]interface{}, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func boolField(m map[string]interface{}, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// checkDeadline fails with a message naming label once clk.Now() passes deadline —
// the whole-pass ceiling check called before each stage below. timeout is threaded
// through only to make the error message legible; the comparison itself is against
// deadline.
func checkDeadline(clk appPassClock, deadline time.Time, timeout time.Duration, label string) error {
	if clk.Now().After(deadline) {
		return fmt.Errorf("%s: exceeded %s ceiling", label, timeout)
	}
	return nil
}

// ---------------------------------------------------------------------------------
// stage 0: ephemeral admin
// ---------------------------------------------------------------------------------

var appPassHostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
var appPassSitePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// appPassHashCharPattern bounds the characters CryptLib's generated salt/hash may
// contain before either is interpolated into a SQL statement (path B). Standard
// crypt/bcrypt-family output uses only this charset; anything else is refused rather
// than interpolated blind.
var appPassHashCharPattern = regexp.MustCompile(`^[A-Za-z0-9./$]+$`)

// appPassResolvePod finds a running app pod, mirroring schema.go's appPod (same
// selector, same field-selector) but routed through the injectable execRunner so
// AppPass's kubectl calls are hermetically testable like every other stage-0 exec.
// appSelector/appContainer are schema.go's unexported constants, reused here rather
// than redefined.
func appPassResolvePod(ctx context.Context, deps appPassDeps, opts AppPassOptions) (string, error) {
	argv := []string{
		"--kubeconfig", opts.Kubeconfig, "-n", opts.Namespace,
		"get", "pods", "-l", appSelector, "--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[0].metadata.name}",
	}
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	pod := strings.TrimSpace(string(stdout))
	if err != nil || pod == "" {
		return "", fmt.Errorf("no running app pod (%s) in %s: %s", appSelector, opts.Namespace, strings.TrimSpace(string(stderr)))
	}
	return pod, nil
}

// kubectlExecArgv builds the common kubectl-exec argv prefix. withStdin adds -i so the
// process reads from the pipe RunAppPass wires up; callers that need no stdin omit it.
func kubectlExecArgv(kubeconfig, namespace, pod string, withStdin bool, cmd []string) []string {
	argv := []string{"--kubeconfig", kubeconfig, "-n", namespace, "exec"}
	if withStdin {
		argv = append(argv, "-i")
	}
	argv = append(argv, pod, "-c", appContainer, "--")
	return append(argv, cmd...)
}

func appPassNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// resolveAppPassSite parses opts.BaseURL's hostname, validates it, and resolves the
// site name via `SELECT name FROM site_index WHERE domain=...` against the `sites` DB
// — sourcing db_helpers.sh and using $mysqlcmd exactly as schema.go does. Requires
// exactly one row and validates the returned name before it is used in a DB name or
// CLI argument anywhere downstream.
func resolveAppPassSite(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod string) (string, error) {
	u, err := url.Parse(normalizeURL(opts.BaseURL))
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	host := u.Hostname()
	if host == "" || !appPassHostPattern.MatchString(host) {
		return "", fmt.Errorf("base URL host %q fails validation", host)
	}
	// host is already validated against appPassHostPattern above (alnum/./- only, no
	// quotes possible), so a plain SQL single-quoted literal is safe here — matching
	// the convention resolveAppPassAdminUserID/rotateAppPassAdminPassword use below.
	// (%q would be wrong: it would nest a second, Go-escaped double-quoted string
	// inside the shell's already-double-quoted -e argument and break the quoting.)
	script := fmt.Sprintf(`. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd -sN -D sites -e "SELECT name FROM site_index WHERE domain='%s'"
`, host)
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, false, []string{"bash", "-c", script})
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	if err != nil {
		return "", fmt.Errorf("site resolution query for domain %q: %v: %s", host, err, strings.TrimSpace(string(stderr)))
	}
	rows := appPassNonEmptyLines(string(stdout))
	if len(rows) != 1 {
		return "", fmt.Errorf("site resolution for domain %q: expected exactly 1 row, got %d", host, len(rows))
	}
	name := rows[0]
	if !appPassSitePattern.MatchString(name) {
		return "", fmt.Errorf("site resolution returned name %q which fails validation", name)
	}
	return name, nil
}

// detectAppPassAdminPath runs `php .../Exec/sites.php` with no arguments and inspects
// its usage text for "create-site-admin" to decide path A vs path B. A non-zero exit is
// expected (it's a usage/help invocation) and is not itself a failure.
func detectAppPassAdminPath(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod string) (string, error) {
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, false,
		[]string{"php", "/home/ifixit/Code/Exec/sites.php"})
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	combined := string(stdout) + string(stderr)
	if combined == "" && err != nil {
		return "", fmt.Errorf("could not invoke sites.php for path detection: %v", err)
	}
	if strings.Contains(combined, "create-site-admin") {
		return "A", nil
	}
	return "B", nil
}

// createAppPassAdminPathA creates a fresh site admin via the documented CLI subcommand.
// The password travels ONLY via stdin (a `read -r PW` at the top of the exec'd script);
// the script text itself never contains it.
func createAppPassAdminPathA(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod, site, email string, secret []byte) error {
	script := fmt.Sprintf("read -r PW\nphp /home/ifixit/Code/Exec/sites.php create-site-admin %q --email=%q --password=\"$PW\"\n", site, email)
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, true, []string{"bash", "-c", script})
	stdin := append(append([]byte{}, secret...), '\n')
	_, stderr, err := deps.exec(ctx, argv, stdin)
	if err != nil {
		return fmt.Errorf("create-site-admin failed: %v: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// generateAppPassHashPathB runs SiteSpecific::setup (mandatory: it is what makes
// CryptLib hash at the server's configured bcrypt cost) then reads the password from
// STDIN and prints "<salt>\t<hash>\n". The password travels ONLY via stdin.
func generateAppPassHashPathB(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod, site string, secret []byte) (salt, hash string, err error) {
	script := fmt.Sprintf(`php -r 'require "/home/ifixit/Code/Exec/essentials.php"; SiteSpecific::setup(%q); $p=trim(fgets(STDIN)); $s=CryptLib::generatePasswordSalt(); $h=CryptLib::hashPassword($p,$s); echo $s."\t".$h."\n";'`, site)
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, true, []string{"bash", "-c", script})
	stdin := append(append([]byte{}, secret...), '\n')
	stdout, stderr, err := deps.exec(ctx, argv, stdin)
	if err != nil {
		return "", "", fmt.Errorf("hash generation failed: %v: %s", err, strings.TrimSpace(string(stderr)))
	}
	parts := strings.SplitN(strings.TrimSpace(string(stdout)), "\t", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("hash generation: could not parse salt/hash from output")
	}
	return parts[0], parts[1], nil
}

// resolveAppPassAdminUserID looks up the seeded admin's userid. Requires exactly one
// row; zero or more than one both fail (and neither attempts the UPDATE that follows).
func resolveAppPassAdminUserID(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod, site string) (int64, error) {
	script := fmt.Sprintf(`. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd -sN -D %[1]s_guide -e "SELECT userid FROM %[1]s_guide.global_users WHERE login='admin@dozuki.com'"
`, site)
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, false, []string{"bash", "-c", script})
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	if err != nil {
		return 0, fmt.Errorf("admin userid lookup: %v: %s", err, strings.TrimSpace(string(stderr)))
	}
	rows := appPassNonEmptyLines(string(stdout))
	if len(rows) != 1 {
		return 0, fmt.Errorf("admin userid lookup: expected exactly 1 row, got %d", len(rows))
	}
	id, err := strconv.ParseInt(rows[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("admin userid lookup: could not parse userid: %w", err)
	}
	return id, nil
}

// rotateAppPassAdminPassword runs the UPDATE and SELECT ROW_COUNT() in the SAME mysql
// session (one heredoc, one $mysqlcmd invocation) — ROW_COUNT() is connection-scoped,
// so two separate execs could not observe the UPDATE's own result. Requires exactly one
// affected row; anything else fails.
func rotateAppPassAdminPassword(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod, site string, userID int64, salt, hash string) error {
	if !appPassHashCharPattern.MatchString(salt) || !appPassHashCharPattern.MatchString(hash) {
		return fmt.Errorf("generated salt/hash contain unexpected characters — refusing to interpolate into SQL")
	}
	script := fmt.Sprintf(`. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd -sN -D %[1]s_guide <<'SQL'
UPDATE %[1]s_guide.global_users SET password_salt='%[2]s', password_hash='%[3]s' WHERE userid=%[4]d;
SELECT ROW_COUNT();
SQL
`, site, salt, hash, userID)
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, false, []string{"bash", "-c", script})
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	if err != nil {
		return fmt.Errorf("password rotation: %v: %s", err, strings.TrimSpace(string(stderr)))
	}
	rows := appPassNonEmptyLines(string(stdout))
	if len(rows) != 1 {
		return fmt.Errorf("password rotation: could not read ROW_COUNT() (got %d lines of output)", len(rows))
	}
	n, err := strconv.Atoi(rows[0])
	if err != nil || n != 1 {
		return fmt.Errorf("password rotation: UPDATE affected %s row(s), want exactly 1", rows[0])
	}
	return nil
}

// stage0EphemeralAdmin mints a throwaway site admin and returns the email stage 1
// should log in with, plus which path was taken ("A" or "B") for the stage-0 completion
// log line.
func stage0EphemeralAdmin(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod string, secret []byte, runsalt string, log *appPassLogger) (loginEmail, path string, err error) {
	site, err := resolveAppPassSite(ctx, deps, opts, pod)
	if err != nil {
		return "", "", fmt.Errorf("site resolution: %w", err)
	}
	p, err := detectAppPassAdminPath(ctx, deps, opts, pod)
	if err != nil {
		return "", "", fmt.Errorf("path detection: %w", err)
	}
	switch p {
	case "A":
		email := fmt.Sprintf("qa-harness-%s@dozuki.test", runsalt)
		if aerr := createAppPassAdminPathA(ctx, deps, opts, pod, site, email, secret); aerr != nil {
			return "", "", fmt.Errorf("path A create-site-admin: %w", aerr)
		}
		return email, "A", nil
	case "B":
		salt, hash, herr := generateAppPassHashPathB(ctx, deps, opts, pod, site, secret)
		if herr != nil {
			return "", "", fmt.Errorf("path B hash generation: %w", herr)
		}
		userID, uerr := resolveAppPassAdminUserID(ctx, deps, opts, pod, site)
		if uerr != nil {
			return "", "", fmt.Errorf("path B userid resolve: %w", uerr)
		}
		if rerr := rotateAppPassAdminPassword(ctx, deps, opts, pod, site, userID, salt, hash); rerr != nil {
			return "", "", fmt.Errorf("path B password rotation: %w", rerr)
		}
		log.Logf("app-pass: stage 0 rotated seeded admin password (kept stacks: admin login is gone by design)")
		return "admin@dozuki.com", "B", nil
	default:
		return "", "", fmt.Errorf("unknown path %q", p)
	}
}

// ---------------------------------------------------------------------------------
// stage 1: login
// ---------------------------------------------------------------------------------

func stage1Login(ctx context.Context, deps appPassDeps, base, email string, secret []byte) (authToken string, userid int64, err error) {
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/user/token", "", map[string]string{
		"email":    email,
		"password": string(secret),
	})
	if err != nil {
		return "", 0, fmt.Errorf("login request: %w", err)
	}
	if res.status != http.StatusCreated {
		return "", 0, fmt.Errorf("login: want status 201, got %d", res.status)
	}
	tok, ok := stringField(res.json, "authToken")
	if !ok || tok == "" {
		return "", 0, fmt.Errorf("login response missing authToken")
	}
	uid, ok := numberField(res.json, "userid")
	if !ok {
		return "", 0, fmt.Errorf("login response missing userid")
	}
	return tok, uid, nil
}

// ---------------------------------------------------------------------------------
// stage 2: register user (no teardown route exists — documented absence)
// ---------------------------------------------------------------------------------

func stage2RegisterUser(ctx context.Context, deps appPassDeps, base, authToken, runsalt string) (int64, error) {
	pwBuf := make([]byte, 16)
	if _, err := crand.Read(pwBuf); err != nil {
		return 0, fmt.Errorf("generate stage-2 password: %w", err)
	}
	username := fmt.Sprintf("qa-harness-user-%s", runsalt)
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/users", authToken, map[string]string{
		"username":        username,
		"unique_username": username,
		"password":        hex.EncodeToString(pwBuf),
		"email":           fmt.Sprintf("qa-harness-user-%s@dozuki.test", runsalt),
	})
	if err != nil {
		return 0, fmt.Errorf("register user request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("register user: want status 201, got %d", res.status)
	}
	uid, ok := numberField(res.json, "userid")
	if !ok {
		return 0, fmt.Errorf("register user response missing userid")
	}
	return uid, nil
}

// ---------------------------------------------------------------------------------
// stage 3: wiki (category)
// ---------------------------------------------------------------------------------

func stage3CreateWiki(ctx context.Context, deps appPassDeps, base, authToken, runsalt string, stack *appPassTeardownStack) (wikiid int64, title string, err error) {
	title = fmt.Sprintf("QA-Harness-%s", runsalt)
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/wikis", authToken, map[string]string{
		"namespace": "CATEGORY",
		"title":     title,
	})
	if err != nil {
		return 0, "", fmt.Errorf("create wiki request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, "", fmt.Errorf("create wiki: want status 201, got %d", res.status)
	}
	wikiid, ok := numberField(res.json, "wikiid")
	if !ok {
		return 0, "", fmt.Errorf("create wiki response missing wikiid")
	}
	gotTitle, ok := stringField(res.json, "title")
	if !ok || gotTitle == "" {
		return 0, "", fmt.Errorf("create wiki response missing title")
	}
	stack.push(appPassTeardownEntry{
		kind: "wiki",
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			res, err := deps.doJSON(ctx, http.MethodDelete, base+"/api/2.0/wikis/CATEGORY/"+url.PathEscape(gotTitle), authToken, nil)
			if err != nil {
				return err
			}
			if res.status/100 != 2 {
				return fmt.Errorf("delete wiki: status %d", res.status)
			}
			return nil
		},
	})
	return wikiid, gotTitle, nil
}

// ---------------------------------------------------------------------------------
// stage 4: media uploads
// ---------------------------------------------------------------------------------

type appPassAsset struct {
	fileName  string
	assetType string // "image" | "document" | "video" — the {type} segment in the media routes
	embedPath string
}

var appPassStageAssets = []appPassAsset{
	{"app_pass_probe.png", "image", "app_pass_assets/app_pass_probe.png"},
	{"app_pass_probe.pdf", "document", "app_pass_assets/app_pass_probe.pdf"},
	{"app_pass_probe.mp4", "video", "app_pass_assets/app_pass_probe.mp4"},
}

const (
	appPassVideoPollInterval = 10 * time.Second
	appPassVideoPollCeiling  = 5 * time.Minute
)

func stage4UploadOne(ctx context.Context, deps appPassDeps, base, authToken string, asset appPassAsset, stack *appPassTeardownStack) (int64, error) {
	data, err := appPassAssets.ReadFile(asset.embedPath)
	if err != nil {
		return 0, fmt.Errorf("read embedded asset: %w", err)
	}
	endpoint := fmt.Sprintf("%s/api/2.0/user/media/uploads?file=%s", base, url.QueryEscape(asset.fileName))
	res, err := deps.doRaw(ctx, http.MethodPost, endpoint, authToken, data, "application/octet-stream")
	if err != nil {
		return 0, fmt.Errorf("upload request: %w", err)
	}
	if res.status != http.StatusOK {
		return 0, fmt.Errorf("upload %s: want status 200, got %d", asset.assetType, res.status)
	}
	id, ok := numberField(res.json, "id")
	if !ok {
		return 0, fmt.Errorf("upload %s response missing id", asset.assetType)
	}
	assetType, itemID := asset.assetType, id
	stack.push(appPassTeardownEntry{
		kind: "media:" + assetType,
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			res, err := deps.doJSON(ctx, http.MethodDelete, fmt.Sprintf("%s/api/2.0/user/media/%s/%d", base, assetType, itemID), authToken, nil)
			if err != nil {
				return err
			}
			if res.status/100 != 2 {
				return fmt.Errorf("delete media %s/%d: status %d", assetType, itemID, res.status)
			}
			return nil
		},
	})
	return id, nil
}

// stage4WaitForVideoReady polls the video status route every appPassVideoPollInterval
// until isReady is true or appPassVideoPollCeiling elapses (measured via clk, so tests
// can drive this instantly with a fake clock whose Sleep just advances a virtual
// clock). The ceiling error's text always contains "stage 4 (video encoding)".
func stage4WaitForVideoReady(ctx context.Context, deps appPassDeps, clk appPassClock, base, authToken string, videoID int64) error {
	deadline := clk.Now().Add(appPassVideoPollCeiling)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		res, err := deps.doJSON(ctx, http.MethodGet, fmt.Sprintf("%s/api/2.0/user/media/video/%d", base, videoID), authToken, nil)
		if err != nil {
			return fmt.Errorf("video status poll: %w", err)
		}
		if res.status != http.StatusOK {
			return fmt.Errorf("video status poll: want status 200, got %d", res.status)
		}
		if ready, ok := boolField(res.json, "isReady"); ok && ready {
			return nil
		}
		if clk.Now().After(deadline) {
			return fmt.Errorf("stage 4 (video encoding): video %d not ready after %s", videoID, appPassVideoPollCeiling)
		}
		clk.Sleep(appPassVideoPollInterval)
	}
}

func stage4Uploads(ctx context.Context, deps appPassDeps, clk appPassClock, base, authToken string, stack *appPassTeardownStack) (imageID, documentID, videoID int64, err error) {
	ids := make(map[string]int64, len(appPassStageAssets))
	for _, asset := range appPassStageAssets {
		id, uerr := stage4UploadOne(ctx, deps, base, authToken, asset, stack)
		if uerr != nil {
			return 0, 0, 0, fmt.Errorf("upload %s: %w", asset.assetType, uerr)
		}
		ids[asset.assetType] = id
	}
	if verr := stage4WaitForVideoReady(ctx, deps, clk, base, authToken, ids["video"]); verr != nil {
		return 0, 0, 0, verr
	}
	return ids["image"], ids["document"], ids["video"], nil
}

// ---------------------------------------------------------------------------------
// stage 5: guide + steps
// ---------------------------------------------------------------------------------

// guideTypeTechnique is a valid literal for the guide `type` field. guides.php's PUT
// handler (Guide/UI/api/2.0/guides.php, case 'type') validates against
// ServerConstants::getGuideTypes() (Libs/ServerConstants.php:702), which falls back to
// SiteSettingsLib::getDefaultGuideTypes() (Libs/SiteSettingsLib.php:205) whenever a
// site's `guide-types` setting doesn't override a given key. "technique" is one of the
// ten defaults there (replacement, installation, repair, disassembly, maintenance,
// troubleshooting, project, how-to, teardown, technique), present on every site unless
// a customer explicitly disabled it — a safe literal for a throwaway QA guide.
const guideTypeTechnique = "technique"

func stage5CreateGuide(ctx context.Context, deps appPassDeps, base, authToken, category, runsalt string, documentID int64, stack *appPassTeardownStack) (int64, error) {
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/guides", authToken, map[string]interface{}{
		"category":  category,
		"type":      guideTypeTechnique,
		"title":     fmt.Sprintf("QA-Harness-Guide-%s", runsalt),
		"documents": []int64{documentID},
	})
	if err != nil {
		return 0, fmt.Errorf("create guide request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("create guide: want status 201, got %d", res.status)
	}
	guideID, ok := numberField(res.json, "guideid")
	if !ok {
		return 0, fmt.Errorf("create guide response missing guideid")
	}
	stack.push(appPassTeardownEntry{
		kind: "guide",
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			res, err := deps.doJSON(ctx, http.MethodDelete, fmt.Sprintf("%s/api/2.0/guides/%d", base, guideID), authToken, nil)
			if err != nil {
				return err
			}
			if res.status/100 != 2 {
				return fmt.Errorf("delete guide %d: status %d", guideID, res.status)
			}
			return nil
		},
	})
	return guideID, nil
}

func stage5AddStep(ctx context.Context, deps appPassDeps, base, authToken string, guideID int64, mediaType string, data interface{}) (int64, error) {
	res, err := deps.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/api/2.0/guides/%d/steps", base, guideID), authToken, map[string]interface{}{
		"media": map[string]interface{}{"type": mediaType, "data": data},
	})
	if err != nil {
		return 0, fmt.Errorf("add %s step request: %w", mediaType, err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("add %s step: want status 201, got %d", mediaType, res.status)
	}
	stepID, ok := numberField(res.json, "stepid")
	if !ok {
		return 0, fmt.Errorf("add %s step response missing stepid", mediaType)
	}
	return stepID, nil
}

// stage5VerifyRoundTrip re-fetches the guide and requires all three of: an "image"
// media step, an "object" media step, and documents[] carrying documentID. Missing any
// one fails.
func stage5VerifyRoundTrip(ctx context.Context, deps appPassDeps, base, authToken string, guideID, documentID int64) (map[string]interface{}, error) {
	res, err := deps.doJSON(ctx, http.MethodGet, fmt.Sprintf("%s/api/2.0/guides/%d", base, guideID), authToken, nil)
	if err != nil {
		return nil, fmt.Errorf("get guide request: %w", err)
	}
	if res.status != http.StatusOK {
		return nil, fmt.Errorf("get guide: want status 200, got %d", res.status)
	}
	hasImage, hasObject := appPassGuideStepMediaTypes(res.json)
	if !hasImage {
		return nil, fmt.Errorf("get guide: no image media found in steps[]")
	}
	if !hasObject {
		return nil, fmt.Errorf("get guide: no object media found in steps[]")
	}
	if !appPassGuideHasDocument(res.json, documentID) {
		return nil, fmt.Errorf("get guide: documents[] does not contain documentid %d", documentID)
	}
	return res.json, nil
}

func appPassGuideStepMediaTypes(guide map[string]interface{}) (hasImage, hasObject bool) {
	steps, _ := guide["steps"].([]interface{})
	for _, s := range steps {
		step, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		media, ok := step["media"].(map[string]interface{})
		if !ok {
			continue
		}
		switch t, _ := stringField(media, "type"); t {
		case "image":
			hasImage = true
		case "object":
			hasObject = true
		}
	}
	return hasImage, hasObject
}

func appPassGuideHasDocument(guide map[string]interface{}, documentID int64) bool {
	docs, _ := guide["documents"].([]interface{})
	for _, d := range docs {
		doc, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := numberField(doc, "documentid"); ok && id == documentID {
			return true
		}
	}
	return false
}

// appPassGuideDocumentURL returns the first documents[] entry's "url" field — a path
// relative to the app's base URL (e.g. "/Document/{id}/{name}.pdf"; Objects/
// Document.php:261-267, GuideURI.php:533-539). There is no "view_url" key on this
// response shape (that belongs to DocumentLib::prepareDocument, a different ajax path
// this flow never calls) — resolveAppPassURL must be used against it before fetching.
func appPassGuideDocumentURL(guide map[string]interface{}) (string, error) {
	docs, _ := guide["documents"].([]interface{})
	for _, d := range docs {
		doc, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		if v, ok := stringField(doc, "url"); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("documents[] has no entry with a non-empty url")
}

// appPassImageSizeKeys is checked in preference order against an image step's
// media.data[0]. "standard" is preferred (a reasonably-sized rendition); "original" is
// the one size GuideImageLib always emits and so is the guaranteed fallback.
var appPassImageSizeKeys = []string{"standard", "original"}

// appPassGuideImageURL returns the CDN url and the stable guid for the first "image"
// step's first media.data[] element. For an image step, media.data is an ARRAY of
// image-variant objects, each carrying "id", "guid" (stable across every size
// rendition of the same image — GuideTranslationLightweight_2_0.php:1093-1106) and one
// URL per available size (GuideAPILib_2_0.php:1264-1285; GuideStepImage.php:36-45;
// GuideImageLib.php:35-45). Fails closed: no size key present, or no guid, fails
// rather than guessing a URL.
func appPassGuideImageURL(guide map[string]interface{}) (cdnURL, guid string, err error) {
	steps, _ := guide["steps"].([]interface{})
	for _, s := range steps {
		step, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		media, ok := step["media"].(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := stringField(media, "type"); t != "image" {
			continue
		}
		data, ok := media["data"].([]interface{})
		if !ok || len(data) == 0 {
			return "", "", fmt.Errorf("image step media.data is missing or empty")
		}
		first, ok := data[0].(map[string]interface{})
		if !ok {
			return "", "", fmt.Errorf("image step media.data[0] is not an object")
		}
		guid, ok = stringField(first, "guid")
		if !ok || guid == "" {
			return "", "", fmt.Errorf("image step media.data[0] missing guid")
		}
		for _, key := range appPassImageSizeKeys {
			if url, ok := stringField(first, key); ok && url != "" {
				return url, guid, nil
			}
		}
		return "", "", fmt.Errorf("image step media.data[0] has neither %s", strings.Join(appPassImageSizeKeys, " nor "))
	}
	return "", "", fmt.Errorf("no image step found in steps[]")
}

// appPassGuideVideoURL returns the first non-empty encodings[].url for the first
// "object" step. Unlike the image case, an object step's media.data is a single
// OBJECT (not an array): GuideTranslationLightweight_2_0.php:1081-1083 and
// GuideVideoObject.php:34-37,106-119 put the encodings array one level down, at
// media.data.encodings[]. Fails closed: no encodings, or none with a url, fails.
func appPassGuideVideoURL(guide map[string]interface{}) (string, error) {
	steps, _ := guide["steps"].([]interface{})
	for _, s := range steps {
		step, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		media, ok := step["media"].(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := stringField(media, "type"); t != "object" {
			continue
		}
		data, ok := media["data"].(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("object step media.data is missing or not an object")
		}
		encodings, ok := data["encodings"].([]interface{})
		if !ok || len(encodings) == 0 {
			return "", fmt.Errorf("object step media.data.encodings is missing or empty")
		}
		for _, e := range encodings {
			enc, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			if url, ok := stringField(enc, "url"); ok && url != "" {
				return url, nil
			}
		}
		return "", fmt.Errorf("object step media.data.encodings has no entry with a non-empty url")
	}
	return "", fmt.Errorf("no object step found in steps[]")
}

// resolveAppPassURL resolves ref (which may be relative, like the document url, or
// already absolute) against base, per net/url's own RFC 3986 reference-resolution
// rules — never plain string concatenation, which breaks the moment base does or
// doesn't carry a trailing slash.
func resolveAppPassURL(base, ref string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	refURL, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", ref, err)
	}
	return baseURL.ResolveReference(refURL).String(), nil
}

// ---------------------------------------------------------------------------------
// stage 6: publish
// ---------------------------------------------------------------------------------

func stage6Publish(ctx context.Context, deps appPassDeps, base, authToken string, guideID int64) error {
	res, err := deps.doJSON(ctx, http.MethodPut, fmt.Sprintf("%s/api/2.0/guides/%d/public", base, guideID), authToken, nil)
	if err != nil {
		return fmt.Errorf("publish guide request: %w", err)
	}
	if res.status != http.StatusOK {
		return fmt.Errorf("publish guide: want status 200, got %d", res.status)
	}

	res, err = deps.doJSON(ctx, http.MethodGet, fmt.Sprintf("%s/api/2.0/guides/%d", base, guideID), authToken, nil)
	if err != nil {
		return fmt.Errorf("get guide (public check) request: %w", err)
	}
	if res.status != http.StatusOK {
		return fmt.Errorf("get guide (public check): want status 200, got %d", res.status)
	}
	if public, ok := boolField(res.json, "public"); !ok || !public {
		return fmt.Errorf("get guide (public check): public flag not true")
	}
	// guideURL (the guide's own page link) is a top-level field, always absolute
	// (GuideAPILib_2_0.php:1708 -> GuideTranslation.php:929-946 viewLink(true)) — no
	// resolution needed.
	guideURL, ok := stringField(res.json, "url")
	if !ok || guideURL == "" {
		return fmt.Errorf("get guide (public check): response missing url")
	}
	imageURL, imageGUID, ierr := appPassGuideImageURL(res.json)
	if ierr != nil {
		return fmt.Errorf("get guide (public check): image url: %w", ierr)
	}
	videoURL, verr := appPassGuideVideoURL(res.json)
	if verr != nil {
		return fmt.Errorf("get guide (public check): video url: %w", verr)
	}
	docRef, derr := appPassGuideDocumentURL(res.json)
	if derr != nil {
		return fmt.Errorf("get guide (public check): document url: %w", derr)
	}
	viewURL, rerr := resolveAppPassURL(base, docRef)
	if rerr != nil {
		return fmt.Errorf("get guide (public check): resolve document url: %w", rerr)
	}

	// Everything below is a plain anonymous read (no Authorization header) — this is
	// exactly the "is it actually public" question stage 6 exists to answer.
	pageRes, err := deps.doRaw(ctx, http.MethodGet, guideURL, "", nil, "")
	if err != nil {
		return fmt.Errorf("get public guide page: %w", err)
	}
	if pageRes.status/100 != 2 {
		return fmt.Errorf("get public guide page: want 2xx, got %d", pageRes.status)
	}
	// The page may render a different SIZE variant than the one appPassGuideImageURL
	// picked, so assert the stable guid rather than the size-specific CDN url.
	if !bytes.Contains(pageRes.body, []byte(imageGUID)) {
		return fmt.Errorf("get public guide page: image guid not present in HTML")
	}

	imgRes, err := deps.doRaw(ctx, http.MethodGet, imageURL, "", nil, "")
	if err != nil {
		return fmt.Errorf("get image CDN url: %w", err)
	}
	if imgRes.status != http.StatusOK {
		return fmt.Errorf("get image CDN url: want status 200, got %d", imgRes.status)
	}

	vidRes, err := deps.doRaw(ctx, http.MethodGet, videoURL, "", nil, "")
	if err != nil {
		return fmt.Errorf("get video encoding url: %w", err)
	}
	if vidRes.status != http.StatusOK {
		return fmt.Errorf("get video encoding url: want status 200, got %d", vidRes.status)
	}

	docRes, err := deps.doRaw(ctx, http.MethodGet, viewURL, "", nil, "")
	if err != nil {
		return fmt.Errorf("get document url (anonymous): %w", err)
	}
	if docRes.status != http.StatusOK {
		return fmt.Errorf("get document url (anonymous): want status 200, got %d", docRes.status)
	}
	return nil
}

// ---------------------------------------------------------------------------------
// stage 7: course
// ---------------------------------------------------------------------------------

func stage7Course(ctx context.Context, deps appPassDeps, base, authToken string, guideID int64, runsalt string, stack *appPassTeardownStack) (int64, error) {
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/courses", authToken, map[string]interface{}{
		"title":    fmt.Sprintf("QA-Harness-Course-%s", runsalt),
		"contents": "QA harness course (created by AppPass, torn down in stage 8)",
		"stages":   []map[string]interface{}{{"doctype": "guide", "docid": guideID}},
	})
	if err != nil {
		return 0, fmt.Errorf("create course request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("create course: want status 201, got %d", res.status)
	}
	wikiid, ok := numberField(res.json, "wikiid")
	if !ok {
		return 0, fmt.Errorf("create course response missing wikiid")
	}
	// Pushed for teardown immediately once the create+ID-extraction succeeds — same
	// pattern as stage3/stage5 — so a getCourse verification failure right below still
	// leaves this course draining in stage 8 instead of orphaned.
	stack.push(appPassTeardownEntry{
		kind: "course",
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			res, err := deps.doJSON(ctx, http.MethodDelete, fmt.Sprintf("%s/api/2.0/courses/%d", base, wikiid), authToken, nil)
			if err != nil {
				return err
			}
			if res.status/100 != 2 {
				return fmt.Errorf("delete course %d: status %d", wikiid, res.status)
			}
			return nil
		},
	})

	getRes, err := deps.doJSON(ctx, http.MethodPost, base+"/api/courses/_shared/getCourse", authToken, map[string]interface{}{"wikiid": wikiid})
	if err != nil {
		return 0, fmt.Errorf("getCourse request: %w", err)
	}
	if getRes.status != http.StatusOK {
		return 0, fmt.Errorf("getCourse: want status 200, got %d", getRes.status)
	}
	if gotWikiID, ok := numberField(getRes.json, "wikiid"); !ok || gotWikiID != wikiid {
		return 0, fmt.Errorf("getCourse response does not carry wikiid %d", wikiid)
	}
	return wikiid, nil
}

// ---------------------------------------------------------------------------------
// stage 8: teardown
// ---------------------------------------------------------------------------------

type appPassTeardownEntry struct {
	kind string
	do   func(ctx context.Context, deps appPassDeps, base, authToken string) error
}

// appPassTeardownStack is a genuine LIFO: entries are pushed in creation order (wiki,
// then the 3 media uploads, then the guide, then the course) and drained in reverse.
// That reversal is what makes stage 8 safe rather than merely tidy: the guide
// references the wiki (its `category`) and the media (its steps/documents), and the
// course references the guide, so popping newest-first deletes every referencer before
// the thing it referenced — course, then guide, then the 3 media items, then the wiki.
type appPassTeardownStack struct {
	entries []appPassTeardownEntry
}

func (s *appPassTeardownStack) push(e appPassTeardownEntry) { s.entries = append(s.entries, e) }

// drainAppPassTeardown pops every entry LIFO. Each failure is logged as non-fatal and
// draining continues — stage 8 is the one stage where a failure never changes the
// pass's verdict (see RunAppPass's doc comment).
func drainAppPassTeardown(ctx context.Context, deps appPassDeps, base, authToken string, log *appPassLogger, stack *appPassTeardownStack) {
	if len(stack.entries) == 0 {
		return
	}
	log.Logf("app-pass: stage 8 (teardown) draining %d entries", len(stack.entries))
	for i := len(stack.entries) - 1; i >= 0; i-- {
		e := stack.entries[i]
		if err := e.do(ctx, deps, base, authToken); err != nil {
			log.Logf("app-pass: stage 8 cleanup %s FAILED (non-fatal): %v", e.kind, err)
			continue
		}
		log.Logf("app-pass: stage 8 (teardown) %s ok", e.kind)
	}
}

// ---------------------------------------------------------------------------------
// orchestration
// ---------------------------------------------------------------------------------

// runAppPass is RunAppPass's internal, fully-injected core: every external effect
// (kubectl, HTTP, time) comes in through deps, and the generated secret/run-salt are
// passed in rather than generated here, so tests can drive it directly and assert exact
// behavior without touching a real cluster or a real clock.
func runAppPass(ctx context.Context, opts AppPassOptions, deps appPassDeps, log *appPassLogger, secretHex, runsalt string) error {
	base := normalizeURL(opts.BaseURL)
	secret := []byte(secretHex)
	deadline := deps.clock.Now().Add(opts.Timeout)

	var stack appPassTeardownStack
	var authToken string
	defer func() {
		drainAppPassTeardown(ctx, deps, base, authToken, log, &stack)
	}()

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 0 (ephemeral admin) starting")
	pod, err := appPassResolvePod(ctx, deps, opts)
	if err != nil {
		return fmt.Errorf("app-pass: stage 0 (ephemeral admin): %w", err)
	}
	loginEmail, path, err := stage0EphemeralAdmin(ctx, deps, opts, pod, secret, runsalt, log)
	if err != nil {
		return fmt.Errorf("app-pass: stage 0 (ephemeral admin): %w", err)
	}
	log.Logf("app-pass: stage 0 (ephemeral admin) ok path=%s", path)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 1 (login) starting")
	tok, userid, err := stage1Login(ctx, deps, base, loginEmail, secret)
	if err != nil {
		return fmt.Errorf("app-pass: stage 1 (login): %w", err)
	}
	authToken = tok
	log.Logf("app-pass: stage 1 (login) ok userid=%d", userid)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 2 (register user) starting")
	regUserID, err := stage2RegisterUser(ctx, deps, base, authToken, runsalt)
	if err != nil {
		return fmt.Errorf("app-pass: stage 2 (register user): %w", err)
	}
	log.Logf("app-pass: stage 2 (register user) ok userid=%d", regUserID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 3 (create wiki) starting")
	_, wikiTitle, err := stage3CreateWiki(ctx, deps, base, authToken, runsalt, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 3 (create wiki): %w", err)
	}
	log.Logf("app-pass: stage 3 (create wiki) ok title=%s", wikiTitle)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 4 (media uploads) starting")
	imageID, documentID, videoID, err := stage4Uploads(ctx, deps, deps.clock, base, authToken, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 4 (media uploads): %w", err)
	}
	log.Logf("app-pass: stage 4 (media uploads) ok image=%d document=%d video=%d", imageID, documentID, videoID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 5 (guide + steps) starting")
	guideID, err := stage5CreateGuide(ctx, deps, base, authToken, wikiTitle, runsalt, documentID, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	if _, err := stage5AddStep(ctx, deps, base, authToken, guideID, "image", []int64{imageID}); err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	if _, err := stage5AddStep(ctx, deps, base, authToken, guideID, "object", videoID); err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	if _, err := stage5VerifyRoundTrip(ctx, deps, base, authToken, guideID, documentID); err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	log.Logf("app-pass: stage 5 (guide + steps) ok guideid=%d", guideID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 6 (publish) starting")
	if err := stage6Publish(ctx, deps, base, authToken, guideID); err != nil {
		return fmt.Errorf("app-pass: stage 6 (publish): %w", err)
	}
	log.Logf("app-pass: stage 6 (publish) ok guideid=%d", guideID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 7 (course) starting")
	courseWikiID, err := stage7Course(ctx, deps, base, authToken, guideID, runsalt, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 7 (course): %w", err)
	}
	log.Logf("app-pass: stage 7 (course) ok wikiid=%d", courseWikiID)

	return nil
}
