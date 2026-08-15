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
	"math"
	"math/big"
	"net/http"
	"net/http/cookiejar"
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
	// PublicDelivery declares whether this harness config's site is supposed to serve
	// logged-out traffic. It drives stage 6's public-delivery assertions and is
	// deliberately a declared expectation rather than something stage 6 infers from the
	// app: inferring it lets a site that regressed into requiring auth look "private"
	// and pass. False (the zero value, and min_default's state) means stage 6 asserts
	// that anonymous reads are refused and then skips the delivery checks.
	PublicDelivery bool
}

// appPassTimeout is the ceiling for an entire AppPass run (all 8 stages, including the
// stage-4 video-encoding poll). A zero AppPassOptions.Timeout normalizes to this inside
// RunAppPass — see normalizeAppPassTimeout.
const appPassTimeout = 15 * time.Minute

// appPassTeardownTimeout bounds stage 8's own drain. Deliberately independent of
// opts.Timeout / stageCtx (see runAppPass): teardown must still get a real chance to
// run — and to actually succeed, on a still-live context — after the pass ceiling
// itself has already fired, not be cut off by the same expired deadline that stopped
// stages 0-7.
const appPassTeardownTimeout = 3 * time.Minute

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
	// scanner accumulates every secret minted over the run — the stage-0/1 admin
	// password plus any other per-stage generated secret (stage 2's registration
	// password) — so finalizeAppPass's self-scan below covers all of them, not just
	// the first one.
	scanner := &appPassSecretScanner{}
	scanner.add(secret)
	deps := appPassDeps{
		exec:       kubectlExecRunner(),
		client:     newAppPassClient(),
		anonClient: newAppPassAnonClient(),
		clock:      realAppPassClock{},
		secrets:    scanner,
	}
	runErr := runAppPass(ctx, opts, deps, log, secret, runsalt)
	return finalizeAppPass(log, runErr, scanner.list()...)
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
// log line AppPass emitted (plus the final error's own text, if there is one) against
// EVERY secret generated over the run (variadic — the stage-0/1 admin password, stage
// 2's registration password, and any future per-stage secret). A hit converts the
// result into a distinct, secret-free failure rather than ever letting the raw error
// (which might carry the leak) escape — layers (a)/(c) are the unit tests asserting
// this and that nothing else is needed.
func finalizeAppPass(log *appPassLogger, runErr error, secrets ...string) error {
	lines := log.Lines()
	if runErr != nil {
		lines = append(lines, runErr.Error())
	}
	for _, l := range lines {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(l, secret) {
				return fmt.Errorf("app-pass: SECRET LEAKED TO LOGS")
			}
		}
	}
	return runErr
}

// appPassSecretScanner accumulates every value that must never appear in a log line or
// error string over one AppPass run, so finalizeAppPass's self-scan can check all of
// them, not just the one secret generated at the top level. Safe to use with a nil
// receiver (add/list are both no-ops) so stage-level unit tests that build an
// appPassDeps without a scanner don't have to construct one just to compile.
type appPassSecretScanner struct {
	mu      sync.Mutex
	secrets []string
}

func (s *appPassSecretScanner) add(secret string) {
	if s == nil || secret == "" {
		return
	}
	s.mu.Lock()
	s.secrets = append(s.secrets, secret)
	s.mu.Unlock()
}

func (s *appPassSecretScanner) list() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.secrets...)
}

// ---------------------------------------------------------------------------------
// dependency injection: exec, HTTP, clock
// ---------------------------------------------------------------------------------

type appPassDeps struct {
	exec   execRunner
	client *http.Client
	// anonClient is client's cookie-free twin, and stage 6's anonymous assertions are
	// worthless without it. client carries a cookie jar, and stage 1 logs in, so the
	// jar holds a live session cookie for the rest of the run. Omitting the
	// Authorization header therefore does NOT make a request anonymous: the jar
	// re-attaches the session automatically, and a "public delivery" check would be
	// quietly proving that an authenticated user can read the guide. Anything meant to
	// be anonymous must go through this client instead.
	anonClient *http.Client
	clock      appPassClock
	secrets    *appPassSecretScanner // nil-safe; see appPassSecretScanner
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
// The cookie jar is load-bearing for stage 7, not incidental. The Next.js /bff-api
// surface is COOKIE-authenticated and ignores the "Authorization: api <token>" header
// every other stage uses — VERIFIED live: /bff-api/courses/_shared/getCourse with the
// api token alone returns 401 "Invalid login", and with the session cookie alone returns
// 200. The cookie is set by the stage-1 token mint (POST /api/2.0/user/token) on the same
// response that returns the authToken, so a jar on this client is all that is needed to
// carry it forward; nothing has to parse or store it by hand. The cookie's name is
// site-scoped (session_<siteid>, e.g. session_2 on min_default), which is the other
// reason to let the jar handle it rather than matching on a hardcoded name.
//
// cookiejar.New(nil) never returns an error (documented), so the error is discarded
// rather than propagated through every caller of this constructor.
func newAppPassClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout:   30 * time.Second,
		Jar:       jar,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

// newAppPassAnonClient is newAppPassClient with NO cookie jar, for requests that have
// to be genuinely anonymous. Identical in every other respect (timeout, TLS, redirect
// behavior) so a difference in outcome can only be the missing session, not a
// difference in client configuration.
//
// A nil Jar makes net/http send no cookies and store none, which is the whole point:
// dropping the Authorization header alone leaves the stage-1 session cookie attached
// and turns an "anonymous" assertion into an authenticated one.
func newAppPassAnonClient() *http.Client {
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

// appPassUsernamePattern is the site's required shape for both `username` and
// `unique_username` on POST /api/2.0/users — VERIFIED against a live app: a hyphenated
// value ("qa-harness-user-<hex>") 422'd, while this underscore-only charset/length
// window is accepted (201). Generated usernames are checked against it before the
// request is sent, matching this file's other pre-flight validation (appPassHostPattern,
// appPassSitePattern) rather than discovering a mismatch only from the server's
// response. Also reused by app_pass_test.go's mock to assert the request body itself,
// so a future regression back to hyphens fails the test, not just a live run.
var appPassUsernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{2,29}$`)

// appPassRegistrationPasswordLength is the length of the password stage 2 generates for
// its registered user. Deliberately different from generateAppPassSecret's plain 32-hex
// stage-0/1 admin password (which is hashed straight into the database via a shell
// pipeline and never faces a password validator, so hex keeps that transport simple):
// this password travels only as a JSON body field over HTTPS to POST /api/2.0/users,
// which VERIFIED-rejects the old all-hex-lowercase shape (no uppercase, no special
// character) on any site with a stricter password policy.
const appPassRegistrationPasswordLength = 20

const (
	appPassPasswordUpper   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	appPassPasswordLower   = "abcdefghijklmnopqrstuvwxyz"
	appPassPasswordDigits  = "0123456789"
	appPassPasswordSpecial = "!@#$%^&*()-_=+"
)

// appPassRandIndex returns a crypto/rand index in [0, n).
func appPassRandIndex(n int) (int, error) {
	v, err := crand.Int(crand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

// generateAppPassRegistrationPassword produces a random appPassRegistrationPasswordLength-
// character password for stage 2's registered user, GUARANTEED (by construction, then
// double-checked via appPassPasswordMeetsPolicy before it is ever returned) to contain
// at least one uppercase letter, one lowercase letter, one digit, and one special
// character — a shape VERIFIED to satisfy the site's password policy, unlike the
// 32-hex-char password this replaced. See appPassRegistrationPasswordLength's comment
// for why this is intentionally NOT the same generator as the stage-0/1 admin secret.
func generateAppPassRegistrationPassword() (string, error) {
	classes := []string{appPassPasswordUpper, appPassPasswordLower, appPassPasswordDigits, appPassPasswordSpecial}
	all := appPassPasswordUpper + appPassPasswordLower + appPassPasswordDigits + appPassPasswordSpecial

	buf := make([]byte, appPassRegistrationPasswordLength)
	for i, charset := range classes {
		idx, err := appPassRandIndex(len(charset))
		if err != nil {
			return "", fmt.Errorf("generate registration password: %w", err)
		}
		buf[i] = charset[idx]
	}
	for i := len(classes); i < len(buf); i++ {
		idx, err := appPassRandIndex(len(all))
		if err != nil {
			return "", fmt.Errorf("generate registration password: %w", err)
		}
		buf[i] = all[idx]
	}
	// Fisher-Yates shuffle (crypto/rand-backed) so the four guaranteed-class characters
	// aren't always sitting in positions 0-3.
	for i := len(buf) - 1; i > 0; i-- {
		j, err := appPassRandIndex(i + 1)
		if err != nil {
			return "", fmt.Errorf("generate registration password: %w", err)
		}
		buf[i], buf[j] = buf[j], buf[i]
	}
	password := string(buf)
	if err := appPassPasswordMeetsPolicy(password); err != nil {
		return "", fmt.Errorf("generated password failed its own policy self-check: %w", err)
	}
	return password, nil
}

// appPassPasswordMeetsPolicy checks pw against the shape
// generateAppPassRegistrationPassword guarantees by construction: length >= 12, and at
// least one uppercase letter, one lowercase letter, one digit, and one special
// character. Used both as generateAppPassRegistrationPassword's own self-check and by
// app_pass_test.go's mock, so a future regression to a weaker generated password fails
// the test rather than only a live run.
func appPassPasswordMeetsPolicy(pw string) error {
	if len(pw) < 12 {
		return fmt.Errorf("password length %d, want >= 12", len(pw))
	}
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range pw {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	if !hasUpper {
		return fmt.Errorf("password has no uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password has no lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password has no digit")
	}
	if !hasSpecial {
		return fmt.Errorf("password has no special character")
	}
	return nil
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
	return d.do(ctx, d.client, method, rawURL, authToken, body, contentType)
}

// doAnon issues a genuinely anonymous GET: no Authorization header AND no cookie jar,
// so no stage-1 session rides along. Every "can a logged-out visitor read this" check
// must use this rather than doRaw with an empty token — see appPassDeps.anonClient.
func (d appPassDeps) doAnon(ctx context.Context, rawURL string) (*apiResult, error) {
	return d.do(ctx, d.anonClient, http.MethodGet, rawURL, "", nil, "")
}

func (d appPassDeps) do(ctx context.Context, client *http.Client, method, rawURL, authToken string, body []byte, contentType string) (*apiResult, error) {
	// A deps built without one of the two clients would otherwise nil-panic deep inside
	// a stage, which reads as a harness crash rather than the wiring mistake it is.
	if client == nil {
		return nil, fmt.Errorf("appPassDeps is missing an http client (both client and anonClient must be set)")
	}
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
	resp, err := client.Do(req)
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

// idField extracts an ID field (userid, wikiid, guideid, stepid, id, documentid, ...).
// Every one of these is a positive integer by construction on the real API, so this
// requires an integral value and rejects zero/negative — a fractional value like 12.9
// silently truncating to 12, or a bare 0, would otherwise pass extraction as if it were
// a real, valid ID and only surface as confusion several calls later.
// numField reads an integral JSON number that is NOT an identifier, so unlike idField it
// accepts 0 and negatives. Used for orderby, where 0 is a legal position and idField's
// "identifiers are positive" rule would silently reject the first step.
func numField(m map[string]interface{}, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	switch n := m[key].(type) {
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int64(n), true
	case string:
		v, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	default:
		return 0, false
	}
}

func idField(m map[string]interface{}, key string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		id := int64(n)
		if id <= 0 {
			return 0, false
		}
		return id, true
	case string:
		id, err := strconv.ParseInt(n, 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	default:
		return 0, false
	}
}

// appPassValuePreview renders an already-parsed JSON value for a diagnostic message.
// The body-based preview is unavailable in helpers that receive a decoded map rather
// than a response, and "missing field X" with no sight of the structure is what made the
// 2026-08-15 stage-5 failure unfixable without re-running the whole hour-long proof.
func appPassValuePreview(v interface{}, n int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unrenderable %T>", v)
	}
	return appPassBodyPreview(b, n)
}

// appPassBodyPreview truncates body to at most n bytes for inclusion in a diagnostic
// error message. A bounded preview of the ACTUAL response — not just its status code —
// is what would have made the original stage-2 failure (a 422 with no visible reason)
// cheap to diagnose instead of expensive. This wraps a RESPONSE body, and a 422
// validation failure can legitimately echo the submitted payload back, including a
// generated secret — so the preview itself is not what keeps a secret out of an error
// string. What does: every generated secret is registered with the scanner
// (deps.secrets.add) BEFORE the request carrying it is ever sent, so by the time a
// response could echo it back, finalizeAppPass's self-scan — which checks the final
// error's own text, not just log lines — already has it on the list. The trade-off is
// deliberate: a response that really does echo a registered secret gets reported as
// "SECRET LEAKED TO LOGS" instead of the underlying HTTP reason, trading that one
// diagnostic detail for never letting the secret text itself escape.
func appPassBodyPreview(body []byte, n int) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "<empty body>"
	}
	if len(s) <= n {
		return s
	}
	// s[:n] slices bytes and can split a multi-byte UTF-8 rune mid-sequence;
	// ToValidUTF8 drops any resulting partial rune at the cut point instead of
	// emitting invalid UTF-8 into diagnostics.
	return strings.ToValidUTF8(s[:n], "") + "…"
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

// appPassPHPFailureMarkers are checked case-insensitively against sites.php's combined
// output, BEFORE either path signature is checked. A PHP process that fataled cannot
// be trusted to describe its own capabilities: a fatal-error dump (e.g. a stack trace
// through the file that also happens to print the word "Usage" from an unrelated
// context, or a truncated script that echoes part of its own source) can easily
// contain either signature's substring by accident. This gate exists specifically so
// that case can never be misread as a positive "B" identification — path B PERMANENTLY
// rotates the seeded admin's password, so trusting a blown-up process's incidental
// output there would be an irreversible mutation performed on bad information.
var appPassPHPFailureMarkers = []string{"fatal error", "parse error", "uncaught"}

func appPassLooksLikePHPFailure(s string) bool {
	lower := strings.ToLower(s)
	for _, marker := range appPassPHPFailureMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// detectAppPassAdminPath runs `php .../Exec/sites.php` with no arguments and inspects
// its usage text to decide path A vs path B.
//
// Path B PERMANENTLY rotates the seeded admin's password — an irreversible mutation —
// so a misdetection here is not a cosmetic bug, it is stage 0 silently performing the
// wrong destructive action on a stack that may not be slim-lineage at all. Evaluation
// order matters and is deliberate:
//  1. A PHP failure signature anywhere in the output fails the stage outright, before
//     either path signature is even checked — see appPassLooksLikePHPFailure.
//  2. "create-site-admin" in the output → "A". Checked before the usage signature on
//     purpose: a real slim image's usage text legitimately contains BOTH
//     "create-site-admin" and the generic usage words, and "A" is the correct answer
//     in that case, so this is not treated as an ambiguity.
//  3. The tool's own stable usage banner ("Usage" and "sites.php" both present) → "B".
//  4. Anything else — unrecognized output, empty output, with or without an exec
//     error — fails the stage, naming the exec error when there was one.
//
// A non-zero exit is expected and NOT itself disqualifying on its own (a bare
// usage/help invocation normally exits non-zero) as long as the output is
// recognizable and not a failure per (1). Never defaults to a path.
func detectAppPassAdminPath(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod string) (string, error) {
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, false,
		[]string{"php", "/home/ifixit/Code/Exec/sites.php"})
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	combined := string(stdout) + string(stderr)
	switch {
	case appPassLooksLikePHPFailure(combined):
		return "", fmt.Errorf("path detection: sites.php failed (php error in output — cannot trust it to describe its own capabilities): %q", appPassTruncateForError(combined, 300))
	case strings.Contains(combined, "create-site-admin"):
		return "A", nil
	case strings.Contains(combined, "Usage") && strings.Contains(combined, "sites.php"):
		return "B", nil
	case err != nil:
		return "", fmt.Errorf("path detection: exec error and output did not positively match either path signature (exec error: %v; output seen: %q)", err, appPassTruncateForError(combined, 300))
	default:
		return "", fmt.Errorf("path detection: output did not positively match either path signature (output seen: %q)", appPassTruncateForError(combined, 300))
	}
}

// appPassTruncateForError bounds how much raw exec output an error message can quote,
// so a pathological usage dump doesn't blow up an error string. Path-detection output
// never carries the secret (it happens before any password is generated into the
// script/stdin path), so quoting it here is safe.
func appPassTruncateForError(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// createAppPassAdminPathA creates a fresh site admin via the documented CLI subcommand.
// The password travels ONLY via stdin (a `read -r PW` at the top of the exec'd script);
// the script text itself never contains it.
// createAppPassAdminPathA's `--password="$PW"` line is deliberate, ACCEPTED residual
// exposure, reviewed and left as-is: `$PW` still never appears in kubectl argv, in the
// exec'd script's literal text, or in any log/error string (the harness-side rule this
// file is held to everywhere else). But once bash expands "$PW" into the php process's
// argv INSIDE the pod, the cleartext password is visible there for that process's
// lifetime — to `ps`, to `/proc/<pid>/cmdline`, to anything else running in that
// container. This is accepted rather than fixed because the verified
// `create-site-admin` CLI has no stdin/env form (only a positional --password flag),
// and the exposure is confined to a single-tenant, ephemeral pod on a throwaway smoke
// stack this same run tears down in stage 8 — nothing outside that pod's lifetime can
// observe it. Do not change this transport without re-verifying the CLI signature.
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
		return "", 0, fmt.Errorf("login: want status 201, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	tok, ok := stringField(res.json, "authToken")
	if !ok || tok == "" {
		return "", 0, fmt.Errorf("login response missing authToken")
	}
	uid, ok := idField(res.json, "userid")
	if !ok {
		return "", 0, fmt.Errorf("login response missing userid")
	}
	return tok, uid, nil
}

// ---------------------------------------------------------------------------------
// stage 2: register user (no teardown route exists — documented absence)
// ---------------------------------------------------------------------------------

func stage2RegisterUser(ctx context.Context, deps appPassDeps, base, authToken, runsalt string) (int64, error) {
	password, err := generateAppPassRegistrationPassword()
	if err != nil {
		return 0, fmt.Errorf("generate stage-2 password: %w", err)
	}
	// Registered with the scanner so finalizeAppPass's leak self-scan covers this
	// generated secret too, not just the stage-0/1 admin password.
	deps.secrets.add(password)
	// unique_username VERIFIED against a live app: must match appPassUsernamePattern —
	// hyphens are illegal ("qa-harness-user-<hex>" 422'd). Underscores throughout, never
	// hyphens.
	username := fmt.Sprintf("qa_harness_user_%s", runsalt)
	if !appPassUsernamePattern.MatchString(username) {
		return 0, fmt.Errorf("generated username %q fails required pattern %s", username, appPassUsernamePattern.String())
	}
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/users", authToken, map[string]string{
		"username":        username,
		"unique_username": username,
		"password":        password,
		"email":           fmt.Sprintf("qa-harness-user-%s@dozuki.test", runsalt),
	})
	if err != nil {
		return 0, fmt.Errorf("register user request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("register user: want status 201, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	uid, ok := idField(res.json, "userid")
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
		// contents is REQUIRED — VERIFIED against a live app: namespace+title alone
		// 422'd with missing_field; adding contents returns 201.
		"contents": "QA harness category (created by AppPass, torn down in stage 8)",
	})
	if err != nil {
		return 0, "", fmt.Errorf("create wiki request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, "", fmt.Errorf("create wiki: want status 201, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	wikiid, ok := idField(res.json, "wikiid")
	if !ok {
		return 0, "", fmt.Errorf("create wiki response missing wikiid")
	}
	gotTitle, ok := stringField(res.json, "title")
	if !ok || gotTitle == "" {
		return 0, "", fmt.Errorf("create wiki response missing title")
	}
	// The wiki's revisionid is captured HERE, from the create response, and closed over
	// below. It cannot be re-fetched at teardown time the way the guide's can: a wiki
	// CATEGORY GET returns "revisionid": null (and "source_revisionid": null with it),
	// while the CREATE response carries the real value. VERIFIED live — create returned
	// revisionid 4, the follow-up GET returned null, DELETE without the param 422'd
	// ("field":"revisionid","code":"missing_field"), and DELETE with the create-time 4
	// returned 204. This is why appPassFetchRevisionID is NOT used for the wiki; it
	// remains correct for the GUIDE, whose GET does return a non-null revisionid.
	wikiRevisionID, ok := idField(res.json, "revisionid")
	if !ok {
		return 0, "", fmt.Errorf("create wiki response missing revisionid")
	}
	stack.push(appPassTeardownEntry{
		kind: "wiki",
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			wikiURL := base + "/api/2.0/wikis/CATEGORY/" + url.PathEscape(gotTitle)
			res, err := deps.doJSON(ctx, http.MethodDelete, fmt.Sprintf("%s?revisionid=%d", wikiURL, wikiRevisionID), authToken, nil)
			if err != nil {
				return err
			}
			// Deletes return 204, not 200 — accept any 2xx.
			if res.status/100 != 2 {
				return fmt.Errorf("delete wiki: status %d: %s", res.status, appPassBodyPreview(res.body, 500))
			}
			return nil
		},
	})
	return wikiid, gotTitle, nil
}

// appPassFetchRevisionID GETs url and extracts its "revisionid" field. Used by stage 8
// to obtain the ?revisionid= query param DELETE requires on both the guide and the wiki
// category routes: VERIFIED for the guide (the same route/field stage 6 already reads
// for the publish call — see stage6Publish); applied to the wiki category route by the
// same GET-the-resource-you're-about-to-delete pattern, since it mirrors the guide route
// one level up (both are revisioned Objects in this app).
func appPassFetchRevisionID(ctx context.Context, deps appPassDeps, authToken, resourceURL string) (int64, error) {
	res, err := deps.doJSON(ctx, http.MethodGet, resourceURL, authToken, nil)
	if err != nil {
		return 0, fmt.Errorf("fetch revisionid: %w", err)
	}
	if res.status != http.StatusOK {
		return 0, fmt.Errorf("fetch revisionid: want status 200, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	revisionID, ok := idField(res.json, "revisionid")
	if !ok {
		return 0, fmt.Errorf("fetch revisionid: response missing revisionid")
	}
	return revisionID, nil
}

// ---------------------------------------------------------------------------------
// stage 4: media uploads
//
// PARTLY VERIFIED — read this before changing anything below.
//
// Live-proven on a kept smoke stack, with the evidence recorded at each site:
//   - the media TYPE vocabulary the 2.0 API expects as a path segment on
//     /api/2.0/user/media/{type}/{id}, which is NOT the harness's own asset labels
//     (appPassMediaAPIType, below, including the DELETE that teardown uses);
//   - the published guide's video read type being "video" and not "object"
//     (appPassGuideVideoURL).
// Do NOT revert either of those to the raw asset labels. That re-introduces a live 400,
// "Invalid type provided in the route".
//
// Still unverified, for one specific reason: the raw-bytes upload request/response shape
// (stage4UploadOne's POST /api/2.0/user/media/uploads?file=), the encode-poll's isReady
// field (stage4WaitForVideoReady), and the image/document URL assertions
// (appPassGuideImageURL, appPassGuideDocumentURL, and their size/guid/encodings
// assumptions) have never executed against a running app, because every upload 422'd on
// an empty allowed-extension list before reaching them. seedAppPassAllowlist (below)
// removes that blocker, so the first run to get through stage 4 is what finally proves
// these shapes. Until such a run is recorded, treat them as unconfirmed, and do not
// invent corrected contracts for them from a pass that never exercised them.
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

// ---------------------------------------------------------------------------------
// FIXTURE: masks the document_extension_allowed provisioning defect
// (David-approved 2026-08-14)
// ---------------------------------------------------------------------------------
//
// A freshly provisioned CloudPrem stack has ZERO rows in document_extension_allowed for
// every site, so the app renders an empty allowed-extension list and rejects every media
// upload with a 422. Stage 4 cannot pass without this.
//
// The defect is in provisioning, not in the app: the product's only default seeder is
// SiteManagementLib::initializeAllowedDocumentTypes(), reachable only from
// createNewSite(), and the CloudPrem bootstrap imports a schema-only sites dump rather
// than creating its sites through that code path, so the rows are never written. Stacks
// whose admin has saved Manage -> Security Settings at least once have rows by accident
// of that write; fresh ones never do.
//
// THIS MASKS THAT DEFECT. With the fixture in place a harness run can no longer detect
// the missing rows, so the nightly will not catch a regression in the provisioning path.
// That trade was made deliberately and knowingly: the alternative was leaving AppPass
// blocked at stage 4 indefinitely. The chart-side repair that would have fixed it for
// real is parked on helm #292, which was deferred because its rollout guard could not be
// made to both repair the broken stacks and protect a deliberately-emptied allowlist.
//
// Remove this fixture when a real fix lands. Removal is safe but never urgent: once the
// rows are provisioned properly the INSERT matches nothing and the fixture is a no-op
// that logs "0 inserted".
//
// Idempotency is per (siteid, document_extension), not per stack. The NOT EXISTS guard,
// against document_extension_allowed's own PRIMARY KEY (siteid, document_extension),
// means an already-seeded site contributes no rows while a site that is missing them
// still gets them. A stack-wide "skip if any rows exist" test would be wrong here: a
// stack with one product-created subsite has rows for that subsite and none for
// base/onprem, and would be skipped in exactly the state the fixture exists to repair.
// DISTINCT is belt-and-braces. document_file_information is PRIMARY KEY
// (document_extension) at monolith 4ea36c94076 (Migrations/SchemaSQL/sites.sql), so one
// row per extension and the CROSS JOIN cannot yield a duplicate pair today. NOT EXISTS
// screens only against already-committed rows and could not save us if that ever changed
// (MySQL materializes the SELECT before inserting, so an in-statement duplicate would
// 1062 against the target's PRIMARY KEY and roll the whole INSERT back). DISTINCT costs
// nothing and makes the statement correct without depending on another repo's schema.
const appPassAllowlistSeedSQL = `INSERT INTO document_extension_allowed (siteid, document_extension)
SELECT DISTINCT s.siteid, dfi.document_extension
FROM site_index s
CROSS JOIN document_file_information dfi
WHERE dfi.document_group IN ('image','pdf','3d_model','video')
  AND NOT EXISTS (SELECT 1 FROM document_extension_allowed dea
                  WHERE dea.siteid = s.siteid
                    AND dea.document_extension = dfi.document_extension);`

// seedAppPassAllowlist runs the seed and every count in ONE mysql session (one heredoc,
// one $mysqlcmd invocation) because ROW_COUNT() is connection-scoped and could not
// observe the INSERT from a separate exec — the same reason rotateAppPassAdminPassword
// is structured this way.
//
// It emits five numbers, and the two that actually gate the result are the last two.
// A global "are there any rows now" check is NOT sufficient: on a stack that already has
// a product-created subsite the global count is large and positive while the site under
// test still has nothing, which is precisely the misattributed stage-4 422 this exists to
// prevent. So the gate is per-site coverage — the site behind opts.BaseURL must hold at
// least as many extensions as document_file_information lists for the four media groups.
//
// BOTH sides of that comparison are filtered to the same four groups. An unfiltered
// site count would be a proxy rather than the coverage statement: a site holding rows
// for extensions outside those groups (an admin can add them from Security Settings)
// would inflate its own count and could clear the bar while still missing media
// extensions. Filtering both sides makes siteRows a subset count of wantExts, so
// siteRows >= wantExts means exactly "this site holds every required extension".
//
// A failure here fails the pass rather than falling through to stage 4, and names which
// of the three possible causes it is, because all three produce a similar-looking result
// and only one of them is the one an operator would guess.
func seedAppPassAllowlist(ctx context.Context, deps appPassDeps, opts AppPassOptions, pod string, log *appPassLogger) error {
	u, err := url.Parse(normalizeURL(opts.BaseURL))
	if err != nil {
		return fmt.Errorf("allowlist fixture: parse base URL: %w", err)
	}
	host := u.Hostname()
	// Same validation and quoting convention as resolveAppPassSite: the pattern admits
	// only alnum/./- so no quote can reach the single-quoted SQL literal below.
	if host == "" || !appPassHostPattern.MatchString(host) {
		return fmt.Errorf("allowlist fixture: base URL host %q fails validation", host)
	}
	script := fmt.Sprintf(`. /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92
set +exu
$mysqlcmd -sN -D sites <<'SQL'
SELECT COUNT(*) FROM document_extension_allowed;
%[1]s
SELECT ROW_COUNT();
SELECT COUNT(*) FROM document_extension_allowed;
SELECT COUNT(*) FROM document_extension_allowed dea JOIN site_index s ON s.siteid = dea.siteid JOIN document_file_information dfi ON dfi.document_extension = dea.document_extension WHERE s.domain='%[2]s' AND dfi.document_group IN ('image','pdf','3d_model','video');
SELECT COUNT(DISTINCT document_extension) FROM document_file_information WHERE document_group IN ('image','pdf','3d_model','video');
SQL
`, appPassAllowlistSeedSQL, host)
	argv := kubectlExecArgv(opts.Kubeconfig, opts.Namespace, pod, false, []string{"bash", "-c", script})
	stdout, stderr, err := deps.exec(ctx, argv, nil)
	if err != nil {
		return fmt.Errorf("allowlist fixture: %v: %s", err, strings.TrimSpace(string(stderr)))
	}
	rows := appPassNonEmptyLines(string(stdout))
	if len(rows) != 5 {
		return fmt.Errorf("allowlist fixture: expected 5 result lines (before, inserted, after, site rows, expected extensions), got %d", len(rows))
	}
	labels := []string{"before-count", "inserted-count", "after-count", "site-row-count", "expected-extension-count"}
	nums := make([]int, len(rows))
	for i, r := range rows {
		n, perr := strconv.Atoi(r)
		if perr != nil {
			return fmt.Errorf("allowlist fixture: could not parse %s %q: %w", labels[i], r, perr)
		}
		nums[i] = n
	}
	before, inserted, after, siteRows, wantExts := nums[0], nums[1], nums[2], nums[3], nums[4]

	// Cause 1: the catalogue itself is empty, or its document_group vocabulary is not the
	// four MediaGroup values this seeds from. Nothing could have been inserted for anyone.
	if wantExts == 0 {
		return fmt.Errorf("allowlist fixture: document_file_information lists 0 extensions in groups image/pdf/3d_model/video (before=%d inserted=%d after=%d) — the catalogue is unpopulated or its document_group values differ; stage 4 cannot pass", before, inserted, after)
	}
	// Cause 2: the site under test is not in site_index under this domain, so the CROSS
	// JOIN never produced rows for it however healthy the global counts look.
	if siteRows == 0 {
		return fmt.Errorf("allowlist fixture: 0 allowlist rows for the site at domain %q after seeding (global before=%d inserted=%d after=%d) — site_index likely has no row for this domain; stage 4 cannot pass", host, before, inserted, after)
	}
	// Cause 3: partial coverage. Some extensions landed but not the full set, so whether
	// stage 4's png/pdf/mp4 are among them is luck.
	if siteRows < wantExts {
		return fmt.Errorf("allowlist fixture: site at domain %q has %d of %d expected extensions after seeding (before=%d inserted=%d after=%d) — coverage is partial; stage 4 may still 422", host, siteRows, wantExts, before, inserted, after)
	}
	log.Logf("app-pass: allowlist FIXTURE before=%d inserted=%d after=%d site=%d/%d (masks the provisioning defect)", before, inserted, after, siteRows, wantExts)
	return nil
}

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
		return 0, fmt.Errorf("upload %s: want status 200, got %d: %s", asset.assetType, res.status, appPassBodyPreview(res.body, 500))
	}
	id, ok := idField(res.json, "id")
	if !ok {
		return 0, fmt.Errorf("upload %s response missing id", asset.assetType)
	}
	assetType, itemID := asset.assetType, id
	stack.push(appPassTeardownEntry{
		kind: "media:" + assetType,
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			// The path segment is the API's media TYPE, not the harness's asset label —
			// see appPassMediaAPIType. Passing assetType straight through 400s.
			mediaType, terr := appPassMediaAPIType(assetType)
			if terr != nil {
				return fmt.Errorf("delete media %s/%d: %w", assetType, itemID, terr)
			}
			res, err := deps.doJSON(ctx, http.MethodDelete, fmt.Sprintf("%s/api/2.0/user/media/%s/%d", base, mediaType, itemID), authToken, nil)
			if err != nil {
				return err
			}
			// Deletes return 204, not 200 — accept any 2xx.
			if res.status/100 != 2 {
				return fmt.Errorf("delete media %s/%d: status %d: %s", assetType, itemID, res.status, appPassBodyPreview(res.body, 500))
			}
			return nil
		},
	})
	return id, nil
}

// appPassMediaAPIType maps the harness's own asset label to the media TYPE the 2.0 API
// expects as a path segment on /api/2.0/user/media/{type}/{id}. The two vocabularies are
// NOT the same, and the API rejects the harness's labels outright — VERIFIED live:
// GET /api/2.0/user/media/video/1 and DELETE /api/2.0/user/media/image/2 both return 400
// "Invalid type provided in the route. Type must one of: GuideImage, GuideVideoObject,
// GuideEmbedObject, Document.", while the mapped forms return 200 and 204 respectively.
// GuideEmbedObject is the fourth legal value and AppPass never produces one.
func appPassMediaAPIType(assetType string) (string, error) {
	switch assetType {
	case "image":
		return "GuideImage", nil
	case "document":
		return "Document", nil
	case "video":
		return "GuideVideoObject", nil
	default:
		return "", fmt.Errorf("no API media type for asset label %q", assetType)
	}
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
		videoType, terr := appPassMediaAPIType("video")
		if terr != nil {
			return fmt.Errorf("video status poll: %w", terr)
		}
		res, err := deps.doJSON(ctx, http.MethodGet, fmt.Sprintf("%s/api/2.0/user/media/%s/%d", base, videoType, videoID), authToken, nil)
		if err != nil {
			return fmt.Errorf("video status poll: %w", err)
		}
		if res.status != http.StatusOK {
			return fmt.Errorf("video status poll: want status 200, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
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
	// Defensive, not reachable via a live server response (stage4UploadOne already
	// fails closed on a missing id): guards against appPassStageAssets itself ever
	// losing one of its three entries, which would otherwise silently poll video id 0
	// below instead of failing loud.
	for _, want := range []string{"image", "document", "video"} {
		if _, ok := ids[want]; !ok {
			return 0, 0, 0, fmt.Errorf("upload: no %s asset produced an id (appPassStageAssets contract error)", want)
		}
	}
	if verr := stage4WaitForVideoReady(ctx, deps, clk, base, authToken, ids["video"]); verr != nil {
		return 0, 0, 0, verr
	}
	return ids["image"], ids["document"], ids["video"], nil
}

// ---------------------------------------------------------------------------------
// stage 5: guide + steps
// ---------------------------------------------------------------------------------

// guideTypeHowTo is the guide `type` field literal AppPass sends when creating its
// stage-5 QA guide. VERIFIED against a live app: "technique" (this file's original
// assumption, based on ServerConstants::getGuideTypes()'s / SiteSettingsLib's default
// ten-type list) was rejected — "Invalid guide type. Got technique Was expecting one
// of: how-to." — meaning this site's `guide-types` setting has narrowed the allowed set
// down to just "how-to". Use "how-to" as the literal observed to work.
const guideTypeHowTo = "how-to"

func stage5CreateGuide(ctx context.Context, deps appPassDeps, base, authToken, category, runsalt string, documentID int64, stack *appPassTeardownStack) (int64, error) {
	res, err := deps.doJSON(ctx, http.MethodPost, base+"/api/2.0/guides", authToken, map[string]interface{}{
		"category":  category,
		"type":      guideTypeHowTo,
		"title":     fmt.Sprintf("QA-Harness-Guide-%s", runsalt),
		"documents": []int64{documentID},
	})
	if err != nil {
		return 0, fmt.Errorf("create guide request: %w", err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("create guide: want status 201, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	guideID, ok := idField(res.json, "guideid")
	if !ok {
		return 0, fmt.Errorf("create guide: 201 but response has no guideid: %s", appPassBodyPreview(res.body, 500))
	}
	stack.push(appPassTeardownEntry{
		kind: "guide",
		do: func(ctx context.Context, deps appPassDeps, base, authToken string) error {
			guideURL := fmt.Sprintf("%s/api/2.0/guides/%d", base, guideID)
			// DELETE requires ?revisionid= as a query param (VERIFIED) — same
			// GET-then-act pattern as the wiki category and stage 6's publish call;
			// see appPassFetchRevisionID.
			revisionID, rerr := appPassFetchRevisionID(ctx, deps, authToken, guideURL)
			if rerr != nil {
				return fmt.Errorf("delete guide %d: %w", guideID, rerr)
			}
			res, err := deps.doJSON(ctx, http.MethodDelete, fmt.Sprintf("%s?revisionid=%d", guideURL, revisionID), authToken, nil)
			if err != nil {
				return err
			}
			// Deletes return 204, not 200 — accept any 2xx.
			if res.status/100 != 2 {
				return fmt.Errorf("delete guide %d: status %d: %s", guideID, res.status, appPassBodyPreview(res.body, 500))
			}
			return nil
		},
	})
	return guideID, nil
}

// appPassStepLine is a single entry of a step's `lines[]`. VERIFIED against a live app:
// the API requires `text` (not `text_raw`), an integer `level` (0 is valid; a first
// line with level 1 is rejected — "Invalid line indentation"), and a bullet style via
// `bullet` (a name, e.g. "black") or `bullet_styleid`.
type appPassStepLine struct {
	Text   string `json:"text"`
	Level  int    `json:"level"`
	Bullet string `json:"bullet"`
}

func stage5AddStep(ctx context.Context, deps appPassDeps, base, authToken string, guideID int64, mediaType string, data interface{}, orderby int) (int64, error) {
	res, err := deps.doJSON(ctx, http.MethodPost, fmt.Sprintf("%s/api/2.0/guides/%d/steps", base, guideID), authToken, map[string]interface{}{
		// orderby and lines[] are REQUIRED — VERIFIED against a live app: a body
		// carrying only `media` 422'd. This exact shape (text/level/bullet) returned
		// 201: {"orderby":1,"title":"QA step","lines":[{"text":"probe","level":0,
		// "bullet":"black"}]}.
		"orderby": orderby,
		"title":   fmt.Sprintf("QA step %d", orderby),
		"lines":   []appPassStepLine{{Text: "probe", Level: 0, Bullet: "black"}},
		"media":   map[string]interface{}{"type": mediaType, "data": data},
	})
	if err != nil {
		return 0, fmt.Errorf("add %s step request: %w", mediaType, err)
	}
	if res.status != http.StatusCreated {
		return 0, fmt.Errorf("add %s step: want status 201, got %d: %s", mediaType, res.status, appPassBodyPreview(res.body, 500))
	}
	// The 201 body is the WHOLE UPDATED GUIDE, not the step that was just created.
	// GuidesStepApiController::newStep (iFixit/Guide/Controllers/GuidesStepApiController.php
	// :227-229, route registered :74) ends with
	// `return new APIResult(GuideAPILib_2_0::guideTranslationToArray($guide, false), 201)`,
	// which is the same shape GET /guides/{guideid} returns. So there is no top-level
	// stepid, and the original lookup for one could never have succeeded: the 2026-08-15
	// fresh proof run failed here with exactly that.
	//
	// The new step is one entry in steps[], which also carries every PRE-EXISTING step.
	//
	// Prefer the entry whose orderby matches what we asked for, but DO NOT make that match
	// mandatory. The source establishes the response SHAPE; it does not establish that the
	// server persists the requested orderby verbatim, and it could legitimately renumber,
	// 0-base, or clamp to the actual append position. Nothing here can prove otherwise: a
	// mock written alongside this code echoes back whatever it was sent, so it can only
	// ever agree with the assumption it encodes. That is precisely how the original
	// top-level-stepid fiction survived to a live run, and repeating the pattern one level
	// down would be the same mistake.
	//
	// Making it fatal would also buy nothing. No caller reads this id - both call sites
	// discard it, and stage5VerifyRoundTrip re-fetches the guide and asserts on media
	// types and documents[], which is the real check. So a strict match could only ever
	// turn a passing run red over an unproven ordering detail.
	//
	// What IS worth failing on: a 201 whose guide carries no identifiable step at all,
	// which would mean the step was not created.
	// Cross-check the guide identity before trusting anything in the body. With the
	// fallback below, an unrelated or stale guide would otherwise hand back a plausible
	// step id and the add would look successful. Strict only when the field is present:
	// the shape is derived from source, not observed, so an absent guideid should not
	// invent a new failure mode.
	//
	// Missing and malformed are distinguished deliberately. idField reports both as "not
	// ok", so a single `ok &&` guard would silently tolerate a present-but-unusable
	// guideid (null, 0, negative, a nested object) while the comment claimed strictness -
	// a comment asserting a property the code does not have.
	if raw, present := res.json["guideid"]; present {
		gid, ok := idField(res.json, "guideid")
		if !ok {
			return 0, fmt.Errorf("add %s step: 201 but guideid is present and unusable (%v): %s", mediaType, raw, appPassBodyPreview(res.body, 500))
		}
		if gid != guideID {
			return 0, fmt.Errorf("add %s step: 201 but the returned guide is %d, not the %d we posted to: %s", mediaType, gid, guideID, appPassBodyPreview(res.body, 500))
		}
	}
	steps, _ := res.json["steps"].([]interface{})
	if len(steps) == 0 {
		return 0, fmt.Errorf("add %s step: 201 but the returned guide carries no steps[]: %s", mediaType, appPassBodyPreview(res.body, 500))
	}
	var fallback int64
	for _, s := range steps {
		step, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		stepID, ok := idField(step, "stepid")
		if !ok {
			continue
		}
		// numField, not idField: idField rejects <= 0 because it is for identifiers, and
		// orderby 0 is a legal position.
		if ob, ok := numField(step, "orderby"); ok && ob == int64(orderby) {
			return stepID, nil
		}
		if fallback == 0 {
			fallback = stepID
		}
	}
	if fallback != 0 {
		return fallback, nil
	}
	return 0, fmt.Errorf("add %s step: 201 but no steps[] entry carries a stepid: %s", mediaType, appPassBodyPreview(res.body, 500))
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
		return nil, fmt.Errorf("get guide: want status 200, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	hasImage, hasVideo := appPassGuideStepMediaTypes(res.json)
	if !hasImage {
		return nil, fmt.Errorf("get guide: no image media found in steps[]")
	}
	if !hasVideo {
		return nil, fmt.Errorf("get guide: no video media found in steps[]")
	}
	if !appPassGuideHasDocument(res.json, documentID) {
		return nil, fmt.Errorf("get guide: documents[] does not contain documentid %d", documentID)
	}
	return res.json, nil
}

// appPassStepVideoWriteType / appPassStepVideoReadType exist as a PAIR because the API is
// asymmetric about a video step's media.type: it is WRITTEN as "object" and READ BACK as
// "video". VERIFIED live — a step created with media.type "object" (201) reads back from
// GET /api/2.0/guides/{id} as {"stepid":2,"media":{"type":"video",...}}. Matching the
// write value on read silently finds no video step at all, which is exactly what the
// original single "object" constant did here and in appPassGuideVideoURL.
const (
	appPassStepVideoWriteType = "object"
	appPassStepVideoReadType  = "video"
)

func appPassGuideStepMediaTypes(guide map[string]interface{}) (hasImage, hasVideo bool) {
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
		case appPassStepVideoReadType:
			hasVideo = true
		}
	}
	return hasImage, hasVideo
}

func appPassGuideHasDocument(guide map[string]interface{}, documentID int64) bool {
	docs, _ := guide["documents"].([]interface{})
	for _, d := range docs {
		doc, ok := d.(map[string]interface{})
		if !ok {
			continue
		}
		if id, ok := idField(doc, "documentid"); ok && id == documentID {
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
			return "", "", fmt.Errorf("image step media.data is missing or empty: %s", appPassValuePreview(media, 500))
		}
		first, ok := data[0].(map[string]interface{})
		if !ok {
			return "", "", fmt.Errorf("image step media.data[0] is not an object: %s", appPassValuePreview(data[0], 500))
		}
		guid, ok = stringField(first, "guid")
		if !ok || guid == "" {
			return "", "", fmt.Errorf("image step media.data[0] missing guid: %s", appPassValuePreview(first, 500))
		}
		for _, key := range appPassImageSizeKeys {
			if url, ok := stringField(first, key); ok && url != "" {
				return url, guid, nil
			}
		}
		return "", "", fmt.Errorf("image step media.data[0] has neither %s: %s", strings.Join(appPassImageSizeKeys, " nor "), appPassValuePreview(first, 500))
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
		if t, _ := stringField(media, "type"); t != appPassStepVideoReadType {
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

// requireAppPassFetchScheme rejects any URL AppPass is about to fetch that isn't plain
// http/https. Every URL it guards is server-supplied (parsed out of the guide API's
// own JSON response), so this is a cheap backstop against a corrupted or malicious
// response handing back a file://, data:, or other scheme this stage should never
// blindly dereference.
func requireAppPassFetchScheme(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q has scheme %q, want http or https", rawURL, u.Scheme)
	}
	return nil
}

// ---------------------------------------------------------------------------------
// stage 6: publish
// ---------------------------------------------------------------------------------

func stage6Publish(ctx context.Context, deps appPassDeps, log *appPassLogger, base, authToken string, guideID int64, publicDelivery bool) error {
	// revisionid is a QUERY PARAMETER on the publish PUT, not a JSON body field —
	// VERIFIED against a live app: a body-borne revisionid 422'd missing_field. It
	// comes from the guide GET response's own revisionid field.
	//
	// apiGuideURL (the REST resource) and pageURL (the human-facing guide page the
	// response advertises) are deliberately separate variables. They used to be one,
	// reassigned halfway down, which made every reference below depend on where in the
	// function you were reading — and the privacy gate has to probe the API url
	// specifically, so the distinction is now load-bearing.
	apiGuideURL := fmt.Sprintf("%s/api/2.0/guides/%d", base, guideID)
	revisionID, rerr := appPassFetchRevisionID(ctx, deps, authToken, apiGuideURL)
	if rerr != nil {
		return fmt.Errorf("publish guide: %w", rerr)
	}

	res, err := deps.doJSON(ctx, http.MethodPut, fmt.Sprintf("%s/public?revisionid=%d", apiGuideURL, revisionID), authToken, nil)
	if err != nil {
		return fmt.Errorf("publish guide request: %w", err)
	}
	if res.status != http.StatusOK {
		return fmt.Errorf("publish guide: want status 200, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}

	res, err = deps.doJSON(ctx, http.MethodGet, apiGuideURL, authToken, nil)
	if err != nil {
		return fmt.Errorf("get guide (public check) request: %w", err)
	}
	if res.status != http.StatusOK {
		return fmt.Errorf("get guide (public check): want status 200, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	if public, ok := boolField(res.json, "public"); !ok || !public {
		return fmt.Errorf("get guide (public check): public flag not true: %s", appPassBodyPreview(res.body, 500))
	}
	// guideURL (the guide's own page link) is a top-level field, always absolute
	// (GuideAPILib_2_0.php:1708 -> GuideTranslation.php:929-946 viewLink(true)) — no
	// resolution needed.
	pageURL, ok := stringField(res.json, "url")
	if !ok || pageURL == "" {
		return fmt.Errorf("get guide (public check): response missing url: %s", appPassBodyPreview(res.body, 500))
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

	// All four of these are server-supplied (the app's own API decided them, but
	// nothing stops a corrupted or malicious response handing back a file://, data:,
	// or other non-http(s) scheme) — refuse to dereference anything but plain
	// http/https before fetching any of them.
	for _, u := range []string{pageURL, imageURL, videoURL, viewURL} {
		if serr := requireAppPassFetchScheme(u); serr != nil {
			return fmt.Errorf("get guide (public check): %w", serr)
		}
	}

	// THE PRIVACY GATE (David's Decision 2(ii), 2026-08-14).
	//
	// Everything above this point is authenticated and runs on every config. Everything
	// below is a genuinely anonymous read — the "is it actually public" question stage 6
	// exists to answer. A site that refuses logged-out traffic cannot pass those
	// assertions, and that is its configuration working rather than a defect:
	// min_default is private and stays that way.
	//
	// WHAT DECIDES: the harness config, via publicDelivery, NOT the app's response. An
	// earlier revision inferred privacy from an observed 401 and review rejected it,
	// correctly: a public site that regressed into requiring auth also answers 401, so
	// the run would classify a real outage as "private" and pass. Configuration is the
	// only trustworthy source for what the site is SUPPOSED to be, so it decides, and
	// the probe below is then an assertion about that expectation rather than the thing
	// that forms it.
	//
	// Both directions are asserted, so neither config is a soft spot:
	//   - declared private: the anonymous read MUST be 401. A 200 means the site is
	//     serving content it should not be, which fails loudly instead of skipping.
	//   - declared public: the anonymous read MUST be 200, and every delivery assertion
	//     below then runs for real.
	//
	// The request is the API url, not pageURL: the client follows redirects, so a
	// private site's page 302s to /Login and comes back 200 on the login page. The API
	// route answers a bare 401. GET /api/2.0/site would have been the tidier probe but
	// throws 405 unless the site has the mobile feature enabled
	// (Guide/UI/api/2.0/site.php:121-124), so it says nothing about privacy here.
	//
	// deps.doAnon, never doRaw with an empty token: doRaw uses the jarred client that
	// still holds stage 1's session cookie, which would make every check below an
	// authenticated read wearing an anonymous label.
	anonProbe, err := deps.doAnon(ctx, apiGuideURL)
	if err != nil {
		return fmt.Errorf("probe anonymous guide read: %w", err)
	}
	if !publicDelivery {
		if anonProbe.status != http.StatusUnauthorized {
			return fmt.Errorf("config declares this site private, so an anonymous guide read must be 401, got %d: %s",
				anonProbe.status, appPassBodyPreview(anonProbe.body, 500))
		}
		log.Logf("app-pass: stage6 public-delivery SKIPPED (site private)")
		return nil
	}
	if anonProbe.status != http.StatusOK {
		return fmt.Errorf("config declares this site public, so an anonymous guide read must be 200, got %d: %s",
			anonProbe.status, appPassBodyPreview(anonProbe.body, 500))
	}

	// SHAPES UNVERIFIED, TRANSPORT DELIBERATELY CHANGED — the two halves differ here.
	//
	// Unverified: the assertions below (the guide page fetch, the image-guid-in-HTML
	// check, and the CDN/video/document GETs) have never run against a published guide on
	// a live app, because the proof run failed earlier and never reached publish. Do not
	// invent a corrected contract for their shapes.
	//
	// Changed on purpose: every one of them now goes through deps.doAnon instead of
	// deps.doRaw with an empty auth token. Omitting the Authorization header was never
	// enough — the shared client carries a cookie jar, so stage 1's login re-attached
	// session_<siteid> to each of these and they were silently AUTHENTICATED reads
	// asserting anonymous behaviour. Do not route them back through the jarred client.
	pageRes, err := deps.doAnon(ctx, pageURL)
	if err != nil {
		return fmt.Errorf("get public guide page: %w", err)
	}
	if pageRes.status/100 != 2 {
		return fmt.Errorf("get public guide page: want 2xx, got %d: %s", pageRes.status, appPassBodyPreview(pageRes.body, 500))
	}
	// The page may render a different SIZE variant than the one appPassGuideImageURL
	// picked, so assert the stable guid rather than the size-specific CDN url.
	if !bytes.Contains(pageRes.body, []byte(imageGUID)) {
		return fmt.Errorf("get public guide page: image guid not present in HTML")
	}

	imgRes, err := deps.doAnon(ctx, imageURL)
	if err != nil {
		return fmt.Errorf("get image CDN url: %w", err)
	}
	if imgRes.status != http.StatusOK {
		return fmt.Errorf("get image CDN url: want status 200, got %d: %s", imgRes.status, appPassBodyPreview(imgRes.body, 500))
	}

	vidRes, err := deps.doAnon(ctx, videoURL)
	if err != nil {
		return fmt.Errorf("get video encoding url: %w", err)
	}
	if vidRes.status != http.StatusOK {
		return fmt.Errorf("get video encoding url: want status 200, got %d: %s", vidRes.status, appPassBodyPreview(vidRes.body, 500))
	}

	docRes, err := deps.doAnon(ctx, viewURL)
	if err != nil {
		return fmt.Errorf("get document url (anonymous): %w", err)
	}
	if docRes.status != http.StatusOK {
		return fmt.Errorf("get document url (anonymous): want status 200, got %d: %s", docRes.status, appPassBodyPreview(docRes.body, 500))
	}
	return nil
}

// ---------------------------------------------------------------------------------
// stage 7: course
//
// VERIFIED live 2026-08-13 against a kept smoke stack. This stage exercises BOTH the
// guide-CMS course API (POST /api/2.0/courses, 201, returns wikiid + a non-null
// revisionid) and the Next.js-served course route (POST /bff-api/courses/_shared/
// getCourse, 200, response carries top-level wikiid/title/namespace:"COURSE"). The
// earlier note here claiming this stage did not reach the Next.js route was wrong: it
// always called it, just at the un-routed /api prefix, which is why it 404'd.
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
		return 0, fmt.Errorf("create course: want status 201, got %d: %s", res.status, appPassBodyPreview(res.body, 500))
	}
	wikiid, ok := idField(res.json, "wikiid")
	if !ok {
		return 0, fmt.Errorf("create course: 201 but response has no wikiid: %s", appPassBodyPreview(res.body, 500))
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
			// Deletes return 204, not 200 — accept any 2xx.
			if res.status/100 != 2 {
				return fmt.Errorf("delete course %d: status %d: %s", wikiid, res.status, appPassBodyPreview(res.body, 500))
			}
			return nil
		},
	})

	// /bff-api, NOT /api. Next.js rewrites /bff-api/:path* -> /api/:path* internally
	// (next.config.js), and /bff-api is the only prefix the gateway routes to the nextjs
	// service, so the internal form 404s from outside — VERIFIED live: /api/... returned
	// 404 (an HTML error page) and /bff-api/... returned 200. Auth here rides on the
	// session cookie in the client's jar, not on authToken; see newAppPassClient.
	getRes, err := deps.doJSON(ctx, http.MethodPost, base+"/bff-api/courses/_shared/getCourse", authToken, map[string]interface{}{"wikiid": wikiid})
	if err != nil {
		return 0, fmt.Errorf("getCourse request: %w", err)
	}
	if getRes.status != http.StatusOK {
		return 0, fmt.Errorf("getCourse: want status 200, got %d: %s", getRes.status, appPassBodyPreview(getRes.body, 500))
	}
	if gotWikiID, ok := idField(getRes.json, "wikiid"); !ok || gotWikiID != wikiid {
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

	// stageCtx is what every stage 0-7 exec/HTTP call actually runs on: opts.Timeout is
	// a REAL ceiling here, not only a between-stage poll — a hung kubectl exec or HTTP
	// call is interrupted once it passes, rather than blocking indefinitely on the
	// caller's ctx while the fake-clock-driven checkDeadline below never gets a chance
	// to run again. checkDeadline still runs before each stage too, so a ceiling that
	// has already passed fails immediately with a clean, stage-attributed message
	// instead of waiting for whatever's in flight to notice cancellation.
	stageCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var stack appPassTeardownStack
	var authToken string
	defer func() {
		// Stage 8 must still run when the pass ceiling has already fired: draining on
		// stageCtx would skip every delete the instant it expires, leaking everything
		// created so far. Deriving teardownCtx from the ORIGINAL ctx (its parent, not
		// stageCtx, which is ctx's own child) is sufficient for that: stageCtx expiring
		// does not touch ctx, so a fresh child of ctx with its own bound survives the
		// pass ceiling automatically. Deliberately NOT context.WithoutCancel(ctx): stage
		// 8 is best-effort by design (the eventual stack destroy is the real cleanup),
		// so a genuine upstream cancellation — an operator Ctrl-C, a harness shutdown —
		// should still be allowed to abort in-flight teardown deletes rather than
		// forcing every caller to wait out the full appPassTeardownTimeout regardless.
		teardownCtx, teardownCancel := context.WithTimeout(ctx, appPassTeardownTimeout)
		defer teardownCancel()
		drainAppPassTeardown(teardownCtx, deps, base, authToken, log, &stack)
	}()

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 0 (ephemeral admin) starting")
	pod, err := appPassResolvePod(stageCtx, deps, opts)
	if err != nil {
		return fmt.Errorf("app-pass: stage 0 (ephemeral admin): %w", err)
	}
	// FIXTURE, not a stage: seeds document_extension_allowed so stage 4's uploads are not
	// rejected by an empty allowed-extension list. See seedAppPassAllowlist — it masks a
	// real provisioning defect and is meant to be deleted once that defect is fixed.
	//
	// Placed HERE, before stage 0 and therefore before the pass makes its first HTTP
	// request, on purpose. The app caches config-shaped data (memcached is fleet-wide),
	// and this list is read on the upload path. Seeding after stages 1-3 have already
	// talked to the site risks the empty list being cached first: the fixture would then
	// insert its rows, log a healthy before/inserted/after, and stage 4 would still 422
	// against the cached empty list — a false-success fixture is worse than none, because
	// it converts a known blocker into a mysterious media-contract failure. Running
	// before any HTTP traffic removes the ordering question entirely. It still sits
	// inside the deadline-checked sequence, so a hung exec cannot outlive the ceiling.
	if serr := seedAppPassAllowlist(stageCtx, deps, opts, pod, log); serr != nil {
		return fmt.Errorf("app-pass: %w", serr)
	}

	loginEmail, path, err := stage0EphemeralAdmin(stageCtx, deps, opts, pod, secret, runsalt, log)
	if err != nil {
		return fmt.Errorf("app-pass: stage 0 (ephemeral admin): %w", err)
	}
	log.Logf("app-pass: stage 0 (ephemeral admin) ok path=%s", path)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 1 (login) starting")
	tok, userid, err := stage1Login(stageCtx, deps, base, loginEmail, secret)
	if err != nil {
		return fmt.Errorf("app-pass: stage 1 (login): %w", err)
	}
	authToken = tok
	// Registered with the scanner so finalizeAppPass's leak self-scan also covers the
	// live session token, not just the generated passwords — a response body that
	// happens to echo it back (e.g. into an error preview) must redact rather than leak.
	deps.secrets.add(authToken)
	log.Logf("app-pass: stage 1 (login) ok userid=%d", userid)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 2 (register user) starting")
	regUserID, err := stage2RegisterUser(stageCtx, deps, base, authToken, runsalt)
	if err != nil {
		return fmt.Errorf("app-pass: stage 2 (register user): %w", err)
	}
	log.Logf("app-pass: stage 2 (register user) ok userid=%d", regUserID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 3 (create wiki) starting")
	_, wikiTitle, err := stage3CreateWiki(stageCtx, deps, base, authToken, runsalt, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 3 (create wiki): %w", err)
	}
	log.Logf("app-pass: stage 3 (create wiki) ok title=%s", wikiTitle)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 4 (media uploads) starting")
	imageID, documentID, videoID, err := stage4Uploads(stageCtx, deps, deps.clock, base, authToken, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 4 (media uploads): %w", err)
	}
	log.Logf("app-pass: stage 4 (media uploads) ok image=%d document=%d video=%d", imageID, documentID, videoID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 5 (guide + steps) starting")
	guideID, err := stage5CreateGuide(stageCtx, deps, base, authToken, wikiTitle, runsalt, documentID, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	if _, err := stage5AddStep(stageCtx, deps, base, authToken, guideID, "image", []int64{imageID}, 1); err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	if _, err := stage5AddStep(stageCtx, deps, base, authToken, guideID, appPassStepVideoWriteType, videoID, 2); err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	if _, err := stage5VerifyRoundTrip(stageCtx, deps, base, authToken, guideID, documentID); err != nil {
		return fmt.Errorf("app-pass: stage 5 (guide + steps): %w", err)
	}
	log.Logf("app-pass: stage 5 (guide + steps) ok guideid=%d", guideID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 6 (publish) starting")
	if err := stage6Publish(stageCtx, deps, log, base, authToken, guideID, opts.PublicDelivery); err != nil {
		return fmt.Errorf("app-pass: stage 6 (publish): %w", err)
	}
	log.Logf("app-pass: stage 6 (publish) ok guideid=%d", guideID)

	if derr := checkDeadline(deps.clock, deadline, opts.Timeout, "app-pass"); derr != nil {
		return derr
	}
	log.Logf("app-pass: stage 7 (course) starting")
	courseWikiID, err := stage7Course(stageCtx, deps, base, authToken, guideID, runsalt, &stack)
	if err != nil {
		return fmt.Errorf("app-pass: stage 7 (course): %w", err)
	}
	log.Logf("app-pass: stage 7 (course) ok wikiid=%d", courseWikiID)

	return nil
}
