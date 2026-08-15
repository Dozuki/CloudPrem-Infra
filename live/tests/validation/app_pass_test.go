package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------
// test doubles: clock, exec
// ---------------------------------------------------------------------------------

// fakeAppPassClock never sleeps for real: Sleep just advances an in-memory clock, so
// the stage-4 5-minute video poll and the whole-pass 15-minute ceiling can be exercised
// in a fraction of a second.
type fakeAppPassClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeAppPassClock() *fakeAppPassClock {
	return &fakeAppPassClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeAppPassClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeAppPassClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type recordedExecCall struct {
	argv  []string
	stdin []byte
}

// fakeExec records every call (argv + stdin, both copied so later mutation by the
// caller can't retroactively change what was recorded) and answers via fn.
type fakeExec struct {
	mu    sync.Mutex
	calls []recordedExecCall
	fn    func(argv []string, stdin []byte) (stdout, stderr []byte, err error)
}

func (f *fakeExec) runner() execRunner {
	return func(ctx context.Context, argv []string, stdin []byte) ([]byte, []byte, error) {
		argvCopy := append([]string{}, argv...)
		var stdinCopy []byte
		if stdin != nil {
			stdinCopy = append([]byte{}, stdin...)
		}
		f.mu.Lock()
		f.calls = append(f.calls, recordedExecCall{argv: argvCopy, stdin: stdinCopy})
		f.mu.Unlock()
		if f.fn == nil {
			return nil, nil, fmt.Errorf("fakeExec: no fn configured for argv %v", argv)
		}
		return f.fn(argv, stdin)
	}
}

func (f *fakeExec) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func argvString(argv []string) string {
	return strings.Join(argv, " ")
}

func argvEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// testAppPassOpts declares PublicDelivery, so the default test subject is a site that
// is SUPPOSED to serve anonymous traffic and every stage-6 delivery assertion runs for
// real. The private-config path gets its own test rather than becoming the default,
// because a default that skips those assertions would quietly stop testing them.
func testAppPassOpts(baseURL string) AppPassOptions {
	return AppPassOptions{BaseURL: baseURL, Kubeconfig: "kc", Namespace: "ns", Timeout: time.Hour, PublicDelivery: true}
}

// ---------------------------------------------------------------------------------
// timeout normalization / ceiling
// ---------------------------------------------------------------------------------

func TestNormalizeAppPassTimeout(t *testing.T) {
	if got := normalizeAppPassTimeout(0); got != appPassTimeout {
		t.Errorf("normalizeAppPassTimeout(0) = %v, want %v", got, appPassTimeout)
	}
	if got := normalizeAppPassTimeout(-5 * time.Second); got != appPassTimeout {
		t.Errorf("normalizeAppPassTimeout(negative) = %v, want %v", got, appPassTimeout)
	}
	if got := normalizeAppPassTimeout(5 * time.Minute); got != 5*time.Minute {
		t.Errorf("normalizeAppPassTimeout(5m) = %v, want 5m unchanged", got)
	}
}

// TestRunAppPass_CeilingFailsInstantly proves opts.Timeout is enforced as a real
// ceiling (a negative-relative-to-now timeout trips on the very first deadline check,
// before any exec/HTTP call happens) and that the check costs no real wall-clock time.
func TestRunAppPass_CeilingFailsInstantly(t *testing.T) {
	clk := newFakeAppPassClock()
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		t.Fatalf("no exec call expected once the ceiling has already passed, got argv %v", argv)
		return nil, nil, nil
	}}
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	start := time.Now()
	err := runAppPass(context.Background(), AppPassOptions{
		BaseURL: "https://127.0.0.1:9", Kubeconfig: "kc", Namespace: "ns", Timeout: -1 * time.Second,
	}, deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt0001")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want an error when the ceiling has already passed, got nil")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Errorf("error = %q, want it to mention the ceiling", err.Error())
	}
	if elapsed > 2*time.Second {
		t.Errorf("ceiling check took %v of real time, want near-instant", elapsed)
	}
	if fe.callCount() != 0 {
		t.Errorf("exec was called %d times, want 0 (should fail before stage 0)", fe.callCount())
	}
}

// ---------------------------------------------------------------------------------
// stage 4: video-encoding poll (fake clock, no real sleeping)
// ---------------------------------------------------------------------------------

func TestStage4WaitForVideoReady_CeilingInstant(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		json.NewEncoder(w).Encode(map[string]interface{}{"isReady": false})
	}))
	defer srv.Close()

	clk := newFakeAppPassClock()
	deps := appPassDeps{client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}

	start := time.Now()
	err := stage4WaitForVideoReady(context.Background(), deps, clk, srv.URL, "tok", 103)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("want error, video never becomes ready")
	}
	if !strings.Contains(err.Error(), "stage 4 (video encoding)") {
		t.Errorf("error = %q, want it to contain \"stage 4 (video encoding)\"", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Errorf("poll-to-ceiling took %v of real time, want near-instant (fake clock)", elapsed)
	}
	wantPolls := int(appPassVideoPollCeiling/appPassVideoPollInterval) + 1
	if polls < wantPolls-1 || polls > wantPolls+1 {
		t.Errorf("polls = %d, want approximately %d", polls, wantPolls)
	}
}

func TestStage4WaitForVideoReady_ReadyEventually(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		json.NewEncoder(w).Encode(map[string]interface{}{"isReady": polls >= 3})
	}))
	defer srv.Close()

	clk := newFakeAppPassClock()
	deps := appPassDeps{client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	if err := stage4WaitForVideoReady(context.Background(), deps, clk, srv.URL, "tok", 103); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if polls != 3 {
		t.Errorf("polls = %d, want exactly 3", polls)
	}
}

// ---------------------------------------------------------------------------------
// stage 0: exec-boundary tests — exact argv + stdin, secret only ever in stdin
// ---------------------------------------------------------------------------------

func TestResolveAppPassSite_ExactArgvAndSingleRow(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("acme\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	opts := testAppPassOpts("https://127.0.0.1:9999")

	site, err := resolveAppPassSite(context.Background(), deps, opts, "app-pod-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if site != "acme" {
		t.Errorf("site = %q, want acme", site)
	}

	wantScript := ". /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92\n" +
		"set +exu\n" +
		"$mysqlcmd -sN -D sites -e \"SELECT name FROM site_index WHERE domain='127.0.0.1'\"\n"
	wantArgv := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "app-pod-1", "-c", "app", "--", "bash", "-c", wantScript}

	if len(fe.calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(fe.calls))
	}
	if !argvEqual(fe.calls[0].argv, wantArgv) {
		t.Errorf("argv mismatch:\n got:  %v\n want: %v", fe.calls[0].argv, wantArgv)
	}
	if fe.calls[0].stdin != nil {
		t.Errorf("stdin = %q, want nil (no secret in the site-resolution call)", fe.calls[0].stdin)
	}
}

func TestResolveAppPassSite_ZeroOrManyRowsFails(t *testing.T) {
	for name, out := range map[string]string{"zero rows": "", "two rows": "acme\nother\n"} {
		t.Run(name, func(t *testing.T) {
			fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
				return []byte(out), nil, nil
			}}
			deps := appPassDeps{exec: fe.runner()}
			_, err := resolveAppPassSite(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod")
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestResolveAppPassSite_BadNameFails(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("Bad Name!\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	if _, err := resolveAppPassSite(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod"); err == nil {
		t.Fatal("want error for a site name that fails validation")
	}
}

func TestDetectAppPassAdminPath(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{"path A available", "Usage: sites.php <command>\n  create-site-admin <site>\n  list-sites\n", "A"},
		{"path A unavailable", "Usage: sites.php <command>\n  list-sites\n  rebuild-cache\n", "B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
				return []byte(tc.output), nil, nil
			}}
			deps := appPassDeps{exec: fe.runner()}
			got, err := detectAppPassAdminPath(context.Background(), deps, testAppPassOpts("https://x"), "pod")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
			wantArgv := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "pod", "-c", "app", "--", "php", "/home/ifixit/Code/Exec/sites.php"}
			if !argvEqual(fe.calls[0].argv, wantArgv) {
				t.Errorf("argv mismatch:\n got:  %v\n want: %v", fe.calls[0].argv, wantArgv)
			}
			if fe.calls[0].stdin != nil {
				t.Errorf("stdin = %q, want nil", fe.calls[0].stdin)
			}
		})
	}
}

// TestDetectAppPassAdminPath_FailsClosed proves detectAppPassAdminPath never defaults
// to a path (path B PERMANENTLY rotates the seeded admin's password, so a
// misdetection here is an irreversible mutation on the wrong path). Positive content
// match wins regardless of exec error; anything else — an exec error paired with
// unrecognized output, or unrecognized output with no error at all — must fail instead
// of silently falling through to "B".
func TestDetectAppPassAdminPath_FailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		execErr error
	}{
		{"exec error with unrecognized output", "permission denied\n", fmt.Errorf("exit status 1")},
		{"exec error with empty output", "", fmt.Errorf("exit status 1")},
		{"unrecognized output, no exec error", "some unexpected diagnostic banner\n", nil},
		{"empty output, no exec error", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
				return []byte(tc.output), nil, tc.execErr
			}}
			deps := appPassDeps{exec: fe.runner()}
			path, err := detectAppPassAdminPath(context.Background(), deps, testAppPassOpts("https://x"), "pod")
			if err == nil {
				t.Fatalf("want an error, got path %q with no error", path)
			}
			if path != "" {
				t.Errorf("path = %q on failure, want empty", path)
			}
		})
	}
}

// TestDetectAppPassAdminPath_PositiveContentWinsOverExecError proves an exec error
// does NOT itself disqualify a positively-recognized output (a bare usage/help
// invocation normally exits non-zero) — only unrecognized output does.
func TestDetectAppPassAdminPath_PositiveContentWinsOverExecError(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("Usage: sites.php <command>\n  list-sites\n"), nil, fmt.Errorf("exit status 1")
	}}
	deps := appPassDeps{exec: fe.runner()}
	path, err := detectAppPassAdminPath(context.Background(), deps, testAppPassOpts("https://x"), "pod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "B" {
		t.Errorf("path = %q, want B", path)
	}
}

// TestDetectAppPassAdminPath_PHPFailureGate proves the php-failure check runs BEFORE
// either path signature: output that trips a php failure marker must fail the stage
// even when it ALSO contains the usage banner (or, in principle, "create-site-admin")
// — a fataled php process cannot be trusted to describe its own capabilities, and a
// misread here would silently take path B, which PERMANENTLY rotates the seeded
// admin's password.
func TestDetectAppPassAdminPath_PHPFailureGate(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{
			"fatal error output containing the usage words",
			"PHP Fatal error:  Uncaught Error: Class \"Foo\" not found in /home/ifixit/Code/Exec/sites.php on line 12\n" +
				"Stack trace:\n#0 {main}\n  thrown in /home/ifixit/Code/Exec/sites.php on line 12\n" +
				"Usage: sites.php <command>\n  create-site-admin <site>\n  list-sites\n",
		},
		{
			"parse error output",
			"PHP Parse error:  syntax error, unexpected token \";\" in /home/ifixit/Code/Exec/sites.php on line 40\n",
		},
		{
			"uncaught exception, lowercase",
			"uncaught exception 'RuntimeException' with message 'boom' in /home/ifixit/Code/Exec/sites.php:5\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
				return []byte(tc.output), nil, fmt.Errorf("exit status 255")
			}}
			deps := appPassDeps{exec: fe.runner()}
			path, err := detectAppPassAdminPath(context.Background(), deps, testAppPassOpts("https://x"), "pod")
			if err == nil {
				t.Fatalf("want an error, got path %q with no error (a php failure must never resolve to a path, especially not B)", path)
			}
			if path != "" {
				t.Errorf("path = %q on a php-failure output, want empty", path)
			}
		})
	}
}

// TestCreateAppPassAdminPathA_ExactArgvSecretOnlyInStdin asserts the HOST-SIDE kubectl
// argv only: the secret never appears in what THIS PROCESS passes to kubectl. It does
// NOT — and cannot — speak to what happens once bash expands "$PW" inside the pod's
// shell for the "--password=" flag: that puts the cleartext into the in-pod php
// process's own argv, visible to `ps`/`/proc/<pid>/cmdline` in that container for the
// process's lifetime. That in-pod exposure is a known, accepted residual (see the
// comment on createAppPassAdminPathA) — this test's name should not be read as
// covering it.
func TestCreateAppPassAdminPathA_ExactArgvSecretOnlyInStdin(t *testing.T) {
	secret := []byte("deadbeefdeadbeefdeadbeefdeadbeef")
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return nil, nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	opts := testAppPassOpts("https://x")

	err := createAppPassAdminPathA(context.Background(), deps, opts, "app-pod-1", "acme", "qa-harness-abc123@dozuki.test", secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantScript := "read -r PW\n" +
		`php /home/ifixit/Code/Exec/sites.php create-site-admin "acme" --email="qa-harness-abc123@dozuki.test" --password="$PW"` + "\n"
	wantArgv := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "-i", "app-pod-1", "-c", "app", "--", "bash", "-c", wantScript}

	if !argvEqual(fe.calls[0].argv, wantArgv) {
		t.Errorf("argv mismatch:\n got:  %v\n want: %v", fe.calls[0].argv, wantArgv)
	}
	// The secret must appear ONLY in stdin, never in argv (each argv element checked
	// individually rather than a joined-string Contains, so a partial/split leak
	// can't hide from the check).
	for _, a := range fe.calls[0].argv {
		if strings.Contains(a, string(secret)) {
			t.Errorf("secret leaked into argv element %q", a)
		}
	}
	wantStdin := append(append([]byte{}, secret...), '\n')
	if string(fe.calls[0].stdin) != string(wantStdin) {
		t.Errorf("stdin = %q, want %q", fe.calls[0].stdin, wantStdin)
	}
}

func TestGenerateAppPassHashPathB_ExactArgvSecretOnlyInStdin(t *testing.T) {
	secret := []byte("deadbeefdeadbeefdeadbeefdeadbeef")
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("saltXYZ\thashXYZ\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}

	salt, hash, err := generateAppPassHashPathB(context.Background(), deps, testAppPassOpts("https://x"), "app-pod-1", "acme", secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if salt != "saltXYZ" || hash != "hashXYZ" {
		t.Errorf("salt/hash = %q/%q, want saltXYZ/hashXYZ", salt, hash)
	}

	wantScript := `php -r 'require "/home/ifixit/Code/Exec/essentials.php"; SiteSpecific::setup("acme"); $p=trim(fgets(STDIN)); $s=CryptLib::generatePasswordSalt(); $h=CryptLib::hashPassword($p,$s); echo $s."\t".$h."\n";'`
	wantArgv := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "-i", "app-pod-1", "-c", "app", "--", "bash", "-c", wantScript}

	if !argvEqual(fe.calls[0].argv, wantArgv) {
		t.Errorf("argv mismatch:\n got:  %v\n want: %v", fe.calls[0].argv, wantArgv)
	}
	for _, a := range fe.calls[0].argv {
		if strings.Contains(a, string(secret)) {
			t.Errorf("secret leaked into argv element %q", a)
		}
	}
	wantStdin := append(append([]byte{}, secret...), '\n')
	if string(fe.calls[0].stdin) != string(wantStdin) {
		t.Errorf("stdin = %q, want %q", fe.calls[0].stdin, wantStdin)
	}
}

func TestGenerateAppPassHashPathB_UnparseableOutputFails(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("not-tab-separated\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	if _, _, err := generateAppPassHashPathB(context.Background(), deps, testAppPassOpts("https://x"), "pod", "acme", []byte("secret")); err == nil {
		t.Fatal("want error for unparseable salt/hash output")
	}
}

func TestResolveAppPassAdminUserID_ExactArgvAndRows(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("42\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	id, err := resolveAppPassAdminUserID(context.Background(), deps, testAppPassOpts("https://x"), "app-pod-1", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	wantScript := ". /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92\n" +
		"set +exu\n" +
		"$mysqlcmd -sN -D acme_guide -e \"SELECT userid FROM acme_guide.global_users WHERE login='admin@dozuki.com'\"\n"
	wantArgv := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "app-pod-1", "-c", "app", "--", "bash", "-c", wantScript}
	if !argvEqual(fe.calls[0].argv, wantArgv) {
		t.Errorf("argv mismatch:\n got:  %v\n want: %v", fe.calls[0].argv, wantArgv)
	}

	for name, out := range map[string]string{"zero rows": "", "two rows": "1\n2\n"} {
		t.Run(name, func(t *testing.T) {
			fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
				return []byte(out), nil, nil
			}}
			deps := appPassDeps{exec: fe.runner()}
			if _, err := resolveAppPassAdminUserID(context.Background(), deps, testAppPassOpts("https://x"), "pod", "acme"); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestRotateAppPassAdminPassword_ExactArgvAndRowCount(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("1\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	err := rotateAppPassAdminPassword(context.Background(), deps, testAppPassOpts("https://x"), "app-pod-1", "acme", 42, "salt.XYZ", "hash/ABC$")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantScript := ". /bootstrap/helpers/db_helpers.sh >/dev/null 2>&1 || exit 92\n" +
		"set +exu\n" +
		"$mysqlcmd -sN -D acme_guide <<'SQL'\n" +
		"UPDATE acme_guide.global_users SET password_salt='salt.XYZ', password_hash='hash/ABC$' WHERE userid=42;\n" +
		"SELECT ROW_COUNT();\n" +
		"SQL\n"
	wantArgv := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "app-pod-1", "-c", "app", "--", "bash", "-c", wantScript}
	if !argvEqual(fe.calls[0].argv, wantArgv) {
		t.Errorf("argv mismatch:\n got:  %v\n want: %v", fe.calls[0].argv, wantArgv)
	}
}

func TestRotateAppPassAdminPassword_WrongRowCountFails(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("0\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	if err := rotateAppPassAdminPassword(context.Background(), deps, testAppPassOpts("https://x"), "pod", "acme", 42, "salt", "hash"); err == nil {
		t.Fatal("want error when ROW_COUNT() != 1")
	}
}

func TestRotateAppPassAdminPassword_RefusesUnsafeCharset(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		t.Fatal("must not exec at all when salt/hash fail the charset check")
		return nil, nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	if err := rotateAppPassAdminPassword(context.Background(), deps, testAppPassOpts("https://x"), "pod", "acme", 42, "salt'; DROP TABLE x; --", "hash"); err == nil {
		t.Fatal("want error for a salt containing a quote")
	}
}

// TestStage0EphemeralAdmin_PathB drives the whole stage-0 sequence for path B and
// checks the "kept stacks" log line and the path=B result.
func TestStage0EphemeralAdmin_PathB(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		joined := argvString(argv)
		switch {
		case strings.Contains(joined, "site_index"):
			return []byte("acme\n"), nil, nil
		case strings.Contains(joined, "essentials.php"):
			return []byte("saltXYZ\thashXYZ\n"), nil, nil
		case strings.Contains(joined, "SELECT userid"):
			return []byte("42\n"), nil, nil
		case strings.Contains(joined, "UPDATE"):
			return []byte("1\n"), nil, nil
		case strings.Contains(joined, "Exec/sites.php") && !strings.Contains(joined, "create-site-admin"):
			return []byte("Usage: sites.php <command>\n  list-sites\n"), nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected exec call: %v", argv)
		}
	}}
	deps := appPassDeps{exec: fe.runner()}
	log := newAppPassLogger()
	secret := []byte("deadbeefdeadbeefdeadbeefdeadbeef")

	email, path, err := stage0EphemeralAdmin(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "app-pod-1", secret, "salt1", log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "B" {
		t.Errorf("path = %q, want B", path)
	}
	if email != "admin@dozuki.com" {
		t.Errorf("email = %q, want admin@dozuki.com", email)
	}
	found := false
	for _, l := range log.Lines() {
		if strings.Contains(l, "rotated seeded admin password") {
			found = true
		}
	}
	if !found {
		t.Error("expected the 'rotated seeded admin password' log line for path B")
	}
}

func TestStage0EphemeralAdmin_PathA(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		joined := argvString(argv)
		switch {
		case strings.Contains(joined, "site_index"):
			return []byte("acme\n"), nil, nil
		case strings.Contains(joined, "create-site-admin"):
			return nil, nil, nil
		case strings.Contains(joined, "Exec/sites.php"):
			return []byte("Usage: sites.php <command>\n  create-site-admin <site>\n"), nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected exec call: %v", argv)
		}
	}}
	deps := appPassDeps{exec: fe.runner()}
	log := newAppPassLogger()
	secret := []byte("deadbeefdeadbeefdeadbeefdeadbeef")

	email, path, err := stage0EphemeralAdmin(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "app-pod-1", secret, "salt1", log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "A" {
		t.Errorf("path = %q, want A", path)
	}
	if email != "qa-harness-salt1@dozuki.test" {
		t.Errorf("email = %q, want qa-harness-salt1@dozuki.test", email)
	}
}

// ---------------------------------------------------------------------------------
// full-pass HTTP mock server
// ---------------------------------------------------------------------------------

// appPassMockImageGUID / appPassMockDocumentPath fix the values the mock guide
// response and its dependent handlers agree on: the guid is what the public-page HTML
// embeds and stage 6 must find there, and the document path is RELATIVE (matching the
// real API's documents[].url shape) so resolveAppPassURL's join is actually exercised.
const (
	appPassMockImageGUID    = "guid-mock-abc123"
	appPassMockDocumentPath = "/Document/102/app_pass_probe.pdf"
	// The session cookie the mock login issues, mirroring the real app's per-site
	// session_<siteid> cookie. Named so the anonymity assertions can look for it.
	appPassMockSessionCookie = "session_1"
	// appPassMockGuideRevisionID / appPassMockWikiRevisionID are the fixed revisionid
	// values the mock's guide/wiki GET responses report, and the exact values the
	// publish PUT and the guide/wiki DELETE calls must carry as a ?revisionid= query
	// param — proving stage6Publish/stage8's teardown actually read revisionid from a
	// GET response and pass it through as a query param, not a body field.
	appPassMockGuideRevisionID = 700
	appPassMockWikiRevisionID  = 800
)

type appPassMockFlags struct {
	loginStatus              int
	registerOmitUserID       bool
	wikiStatus               int
	videoNeverReady          bool
	videoReadyOnPoll         int
	roundTripOmitObject      bool
	omitImageGUID            bool // image step media.data[0] missing guid
	omitImageSizeKeys        bool // image step media.data[0] missing BOTH standard and original
	imageOmitStandard        bool // image step media.data[0] missing standard but keeps original (fallback path)
	omitEncodingsURL         bool // object step media.data.encodings present but no entry has a url
	omitDocumentURL          bool // documents[] entry missing url
	publicPageOmitGUID       bool // public guide page HTML doesn't contain the image guid
	getCourseWrongWikiID     bool
	echoAuthTokenInWikiError bool          // non-201 create-wiki response body echoes the caller's auth token
	uploadStatus             int           // non-200 from every stage-4 upload
	guideCreateStatus        int           // non-201 from POST /api/2.0/guides
	addStepStatus            int           // non-201 from POST /api/2.0/guides/{id}/steps
	publishPutStatus         int           // non-200 from PUT /api/2.0/guides/{id}/public
	anonGuideReadStatus      int           // stage 6's privacy probe: 200 = site serves anonymous reads (default), 401 = private
	publishGetPublicFalse    bool          // GET after publish reports public=false regardless of the PUT
	publicPageStatus         int           // non-2xx from the anonymous public guide page GET
	imageCDNStatus           int           // non-200 from the anonymous image CDN GET
	videoEncStatus           int           // non-200 from the anonymous video encoding GET
	docFetchStatus           int           // non-200 from the anonymous document GET
	courseCreateStatus       int           // non-201 from POST /api/2.0/courses
	guideCreateDelay         time.Duration // artificial delay before responding to POST /api/2.0/guides, for the real-ceiling test
}

type appPassMock struct {
	t     *testing.T
	flags appPassMockFlags

	server *httptest.Server

	mu         sync.Mutex
	published  bool
	videoPolls int
	deleteLog  []string
}

func newAppPassMock(t *testing.T, flags appPassMockFlags) *appPassMock {
	m := &appPassMock{t: t, flags: flags}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/2.0/user/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			m.t.Errorf("login request must not carry an Authorization header")
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] == "" || body["password"] == "" {
			m.t.Errorf("login body missing email/password: %+v", body)
		}
		status := statusOr(m.flags.loginStatus, http.StatusCreated)
		// Hand back a session cookie exactly like the real login does. Without this the
		// mock cannot catch the bug it is here to catch: the shared client's jar would
		// silently re-attach this cookie to every later request, so any check that only
		// drops the Authorization header would still be authenticated. With the cookie
		// modeled, assertAnonymous below actually bites.
		if status == http.StatusCreated {
			http.SetCookie(w, &http.Cookie{Name: appPassMockSessionCookie, Value: "sess-abc", Path: "/"})
		}
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"authToken": "tok-1", "userid": 1})
		}
	})

	mux.HandleFunc("/api/2.0/users", func(w http.ResponseWriter, r *http.Request) {
		m.assertAuth(r)
		var reqBody map[string]string
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		// unique_username (and username) must match appPassUsernamePattern — a
		// hyphenated value must fail this assertion, proving a regression back to
		// "qa-harness-user-<hex>" would fail the test.
		if !appPassUsernamePattern.MatchString(reqBody["unique_username"]) {
			m.t.Errorf("register user unique_username = %q, fails required pattern %s", reqBody["unique_username"], appPassUsernamePattern.String())
		}
		if !appPassUsernamePattern.MatchString(reqBody["username"]) {
			m.t.Errorf("register user username = %q, fails required pattern %s", reqBody["username"], appPassUsernamePattern.String())
		}
		if perr := appPassPasswordMeetsPolicy(reqBody["password"]); perr != nil {
			m.t.Errorf("register user password fails complexity policy: %v", perr)
		}
		w.WriteHeader(http.StatusCreated)
		respBody := map[string]interface{}{}
		if !m.flags.registerOmitUserID {
			respBody["userid"] = 2
		}
		_ = json.NewEncoder(w).Encode(respBody)
	})

	mux.HandleFunc("/api/2.0/wikis", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		m.assertAuth(r)
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["namespace"] != "CATEGORY" {
			m.t.Errorf("create wiki namespace = %v, want CATEGORY", body["namespace"])
		}
		if contents, ok := body["contents"].(string); !ok || contents == "" {
			m.t.Errorf("create wiki body missing non-empty contents: %v", body["contents"])
		}
		status := statusOr(m.flags.wikiStatus, http.StatusCreated)
		w.WriteHeader(status)
		if status == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"wikiid": 10, "title": body["title"], "revisionid": appPassMockWikiRevisionID})
		} else if m.flags.echoAuthTokenInWikiError {
			// Simulates a real failure mode: a non-2xx response that echoes request
			// state (here, the caller's own Authorization header) back into its body,
			// the way a validation error can echo a submitted payload.
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error":"validation failed","authToken":%q}`, r.Header.Get("Authorization"))))
		}
	})
	mux.HandleFunc("/api/2.0/wikis/CATEGORY/", func(w http.ResponseWriter, r *http.Request) {
		m.assertAuth(r)
		switch r.Method {
		case http.MethodGet:
			// revisionid is deliberately NULL here: a live wiki CATEGORY GET returns
			// null (VERIFIED), and only the CREATE response carries the real value.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"wikiid": 10, "revisionid": nil})
		case http.MethodDelete:
			m.assertNoRevisionIDInBody(r, "delete wiki")
			if got := r.URL.Query().Get("revisionid"); got != fmt.Sprintf("%d", appPassMockWikiRevisionID) {
				m.t.Errorf("delete wiki revisionid query param = %q, want %d", got, appPassMockWikiRevisionID)
			}
			m.recordDelete("wiki")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/2.0/user/media/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/api/2.0/user/media/uploads" && r.Method == http.MethodPost:
			m.handleUpload(w, r)
		case strings.HasPrefix(path, "/api/2.0/user/media/GuideVideoObject/") && r.Method == http.MethodGet:
			m.assertAuth(r)
			m.handleVideoStatus(w)
		case r.Method == http.MethodDelete:
			m.assertAuth(r)
			m.handleMediaDelete(w, r, path)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/2.0/guides", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		m.assertAuth(r)
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Asserted against the LITERAL, not the guideTypeHowTo constant used to build
		// the request: comparing against the same constant on both sides means a
		// regression that reverts the constant (e.g. back to "technique") moves both
		// sides together and this assertion would never fail.
		if body["type"] != "how-to" {
			m.t.Errorf("create guide type = %v, want %q", body["type"], "how-to")
		}
		if m.flags.guideCreateDelay > 0 {
			// Bail out as soon as the client gives up (r.Context() is cancelled once
			// the client aborts the request, which is exactly what a real ceiling
			// firing looks like) instead of sleeping the full delay regardless —
			// otherwise httptest.Server.Close()'s "wait for outstanding requests"
			// behavior would make every test using this flag pay the full delay in
			// wall time even though the client-side assertion doesn't need it to.
			select {
			case <-time.After(m.flags.guideCreateDelay):
			case <-r.Context().Done():
				return
			}
		}
		status := statusOr(m.flags.guideCreateStatus, http.StatusCreated)
		w.WriteHeader(status)
		if status != http.StatusCreated {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"guideid": 500})
	})
	mux.HandleFunc("/api/2.0/guides/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Stage 6's privacy gate probes this same resource ANONYMOUSLY, so auth is
		// asserted for every caller except that one. Keyed on the missing header
		// rather than on a separate path because the probe deliberately reuses the
		// guide's own API url — see the gate comment in stage6Publish.
		if path == "/api/2.0/guides/500" && r.Method == http.MethodGet && r.Header.Get("Authorization") == "" {
			m.assertAnonymous(r)
			w.WriteHeader(statusOr(m.flags.anonGuideReadStatus, http.StatusOK))
			return
		}
		m.assertAuth(r)
		switch {
		case path == "/api/2.0/guides/500/steps" && r.Method == http.MethodPost:
			m.handleAddStep(w, r)
		case path == "/api/2.0/guides/500/public" && r.Method == http.MethodPut:
			m.assertNoRevisionIDInBody(r, "publish guide")
			if got := r.URL.Query().Get("revisionid"); got != fmt.Sprintf("%d", appPassMockGuideRevisionID) {
				m.t.Errorf("publish revisionid query param = %q, want %d", got, appPassMockGuideRevisionID)
			}
			status := statusOr(m.flags.publishPutStatus, http.StatusOK)
			m.mu.Lock()
			if status == http.StatusOK {
				m.published = true
			}
			m.mu.Unlock()
			w.WriteHeader(status)
		case path == "/api/2.0/guides/500" && r.Method == http.MethodGet:
			m.handleGetGuide(w)
		case path == "/api/2.0/guides/500" && r.Method == http.MethodDelete:
			m.assertNoRevisionIDInBody(r, "delete guide")
			if got := r.URL.Query().Get("revisionid"); got != fmt.Sprintf("%d", appPassMockGuideRevisionID) {
				m.t.Errorf("delete guide revisionid query param = %q, want %d", got, appPassMockGuideRevisionID)
			}
			m.recordDelete("guide")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	mux.HandleFunc("/api/2.0/courses", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		m.assertAuth(r)
		status := statusOr(m.flags.courseCreateStatus, http.StatusCreated)
		w.WriteHeader(status)
		if status != http.StatusCreated {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"wikiid": 900})
	})
	mux.HandleFunc("/api/2.0/courses/900", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		m.assertAuth(r)
		m.recordDelete("course")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/bff-api/courses/_shared/getCourse", func(w http.ResponseWriter, r *http.Request) {
		// Cookie, not api token: this surface ignores the token entirely. Asserting the
		// token here would let the whole cookie-jar mechanism be deleted without a
		// single test failing.
		m.assertSessionCookie(r)
		wikiid := int64(900)
		if m.flags.getCourseWrongWikiID {
			wikiid = 999
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"wikiid": wikiid})
	})

	mux.HandleFunc("/Guide/QA-Harness-Guide/500", func(w http.ResponseWriter, r *http.Request) {
		m.assertAnonymous(r)
		status := statusOr(m.flags.publicPageStatus, http.StatusOK)
		w.WriteHeader(status)
		if status/100 != 2 {
			return
		}
		html := "<html><body>guide page, no image here</body></html>"
		if !m.flags.publicPageOmitGUID {
			html = fmt.Sprintf("<html><body><img data-guid=%q></body></html>", appPassMockImageGUID)
		}
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/cdn/image/101-standard.png", func(w http.ResponseWriter, r *http.Request) {
		m.assertAnonymous(r)
		w.WriteHeader(statusOr(m.flags.imageCDNStatus, http.StatusOK))
	})
	mux.HandleFunc("/cdn/image/101-original.png", func(w http.ResponseWriter, r *http.Request) {
		m.assertAnonymous(r)
		w.WriteHeader(statusOr(m.flags.imageCDNStatus, http.StatusOK))
	})
	mux.HandleFunc("/cdn/video/103/encoding", func(w http.ResponseWriter, r *http.Request) {
		m.assertAnonymous(r)
		w.WriteHeader(statusOr(m.flags.videoEncStatus, http.StatusOK))
	})
	// Registered at the RELATIVE path itself (not under /cdn/) so a request only
	// reaches here if resolveAppPassURL actually joined base + the relative
	// documents[].url correctly — a naive string-concat bug would 404 here.
	mux.HandleFunc(appPassMockDocumentPath, func(w http.ResponseWriter, r *http.Request) {
		m.assertAnonymous(r)
		w.WriteHeader(statusOr(m.flags.docFetchStatus, http.StatusOK))
	})

	m.server = httptest.NewServer(mux)
	return m
}

func statusOr(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func (m *appPassMock) assertAuth(r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "api tok-1" {
		m.t.Errorf("%s %s: Authorization = %q, want \"api tok-1\"", r.Method, r.URL.Path, got)
	}
}

// assertAnonymous fails if a request that is supposed to come from a logged-out visitor
// carries ANY credential. Checking the Authorization header alone is not enough and was
// the actual defect: the shared http client keeps a cookie jar, stage 1 logs in, and the
// jar then re-attaches the session to every later request, so a "public delivery" check
// that merely omitted the header was proving that a logged-IN user could read the guide.
func (m *appPassMock) assertAnonymous(r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "" {
		m.t.Errorf("%s %s: must be anonymous, got Authorization = %q", r.Method, r.URL.Path, got)
	}
	if c, err := r.Cookie(appPassMockSessionCookie); err == nil {
		m.t.Errorf("%s %s: must be anonymous, but carried session cookie %s=%s (the client's jar leaked it)",
			r.Method, r.URL.Path, c.Name, c.Value)
	}
}

// assertSessionCookie fails unless the request carries the session cookie. The bff
// surface is cookie-authenticated and ignores the api token (verified live: 401 with the
// token alone, 200 with the cookie alone), so asserting the token there instead would
// pin the wrong contract and would keep passing even if the cookie jar were removed.
func (m *appPassMock) assertSessionCookie(r *http.Request) {
	c, err := r.Cookie(appPassMockSessionCookie)
	if err != nil || c.Value == "" {
		m.t.Errorf("%s %s: bff route requires the session cookie %q, none present",
			r.Method, r.URL.Path, appPassMockSessionCookie)
	}
}

// assertNoRevisionIDInBody fails label if the request body carries a "revisionid"
// field. revisionid is a query-parameter-only contract (publish PUT, guide/wiki
// DELETE) — an implementation that sent it in BOTH the query string and an obsolete
// JSON body field would pass a mock that only checked the query param, so this pins
// the contract in the other direction too.
func (m *appPassMock) assertNoRevisionIDInBody(r *http.Request, label string) {
	b, err := io.ReadAll(r.Body)
	if err != nil || len(b) == 0 {
		return
	}
	var body map[string]interface{}
	if json.Unmarshal(b, &body) != nil {
		return
	}
	if v, ok := body["revisionid"]; ok {
		m.t.Errorf("%s: revisionid must not appear in the request body (query param only), got %v in body %v", label, v, body)
	}
}

func (m *appPassMock) recordDelete(kind string) {
	m.mu.Lock()
	m.deleteLog = append(m.deleteLog, kind)
	m.mu.Unlock()
}

func (m *appPassMock) handleUpload(w http.ResponseWriter, r *http.Request) {
	m.assertAuth(r)
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		m.t.Errorf("upload Content-Type = %q, want application/octet-stream (raw bytes, no multipart)", ct)
	}
	file := r.URL.Query().Get("file")
	body, _ := io.ReadAll(r.Body)
	if len(body) == 0 {
		m.t.Errorf("upload body is empty for file=%s", file)
	}
	var id int
	switch {
	case strings.HasSuffix(file, ".png"):
		id = 101
	case strings.HasSuffix(file, ".pdf"):
		id = 102
	case strings.HasSuffix(file, ".mp4"):
		id = 103
	default:
		m.t.Fatalf("unexpected upload filename %q", file)
	}
	status := statusOr(m.flags.uploadStatus, http.StatusOK)
	w.WriteHeader(status)
	if status != http.StatusOK {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": id})
}

func (m *appPassMock) handleVideoStatus(w http.ResponseWriter) {
	m.mu.Lock()
	m.videoPolls++
	polls := m.videoPolls
	m.mu.Unlock()

	ready := false
	if !m.flags.videoNeverReady {
		readyOn := m.flags.videoReadyOnPoll
		if readyOn == 0 {
			readyOn = 1
		}
		ready = polls >= readyOn
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"isReady": ready})
}

func (m *appPassMock) handleMediaDelete(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, "/api/2.0/user/media/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	// The live API rejects the harness's own asset labels on this segment with a 400,
	// so the mock only accepts the mapped API media types and translates back for the
	// teardown-order assertions.
	label, ok := map[string]string{"GuideImage": "image", "Document": "document", "GuideVideoObject": "video"}[parts[0]]
	if !ok {
		m.t.Errorf("media delete path segment = %q, want an API media type (GuideImage/Document/GuideVideoObject)", parts[0])
		http.Error(w, `{"message":"Invalid type provided in the route."}`, http.StatusBadRequest)
		return
	}
	m.recordDelete("media:" + label)
	w.WriteHeader(http.StatusNoContent)
}

func (m *appPassMock) handleAddStep(w http.ResponseWriter, r *http.Request) {
	var body map[string]interface{}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// orderby (int) and lines[] (with text/level/bullet) are REQUIRED alongside media —
	// a body carrying only `media` must fail these assertions, proving a regression
	// back to the old media-only shape would fail the test.
	if _, ok := body["orderby"].(float64); !ok {
		m.t.Errorf("add step body missing numeric orderby: %v", body["orderby"])
	}
	if title, ok := body["title"].(string); !ok || title == "" {
		m.t.Errorf("add step body missing non-empty title: %v", body["title"])
	}
	lines, ok := body["lines"].([]interface{})
	if !ok || len(lines) == 0 {
		m.t.Errorf("add step body missing non-empty lines[]: %v", body["lines"])
	}
	for _, l := range lines {
		line, ok := l.(map[string]interface{})
		if !ok {
			m.t.Errorf("add step lines[] entry is not an object: %v", l)
			continue
		}
		if text, ok := line["text"].(string); !ok || text == "" {
			m.t.Errorf("add step lines[] entry missing non-empty text (not text_raw): %v", line)
		}
		if _, ok := line["level"].(float64); !ok {
			m.t.Errorf("add step lines[] entry missing numeric level: %v", line)
		}
		_, hasBulletStyle := line["bullet_styleid"]
		if bullet, ok := line["bullet"].(string); (!ok || bullet == "") && !hasBulletStyle {
			m.t.Errorf("add step lines[] entry missing bullet or bullet_styleid: %v", line)
		}
	}

	media, _ := body["media"].(map[string]interface{})
	mtype, _ := media["type"].(string)
	var stepid int
	switch mtype {
	case "image":
		stepid = 601
		if data, ok := media["data"].([]interface{}); !ok || len(data) != 1 {
			m.t.Errorf("image step media.data = %v, want a single-element array", media["data"])
		}
	case "object":
		stepid = 602
		if _, ok := media["data"].(float64); !ok {
			m.t.Errorf("object step media.data = %v, want a scalar", media["data"])
		}
	default:
		m.t.Fatalf("unexpected step media type %q", mtype)
	}
	status := statusOr(m.flags.addStepStatus, http.StatusCreated)
	w.WriteHeader(status)
	if status != http.StatusCreated {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"stepid": stepid})
}

// handleGetGuide mirrors the REAL guide response shape (per the WS3 integration
// review, citing GuideTranslationLightweight_2_0.php / GuideAPILib_2_0.php /
// GuideStepImage.php / GuideImageLib.php / GuideVideoObject.php / Objects/Document.php
// / GuideURI.php): an image step's media.data is an ARRAY of variant objects (each
// carrying "guid" plus one key per size); an object (video) step's media.data is a
// single OBJECT whose "encodings" array holds the playable urls; a document's url is a
// path RELATIVE to the app's base URL, not an absolute "view_url".
func (m *appPassMock) handleGetGuide(w http.ResponseWriter) {
	m.mu.Lock()
	published := m.published
	m.mu.Unlock()

	imageVariant := map[string]interface{}{"id": 101}
	if !m.flags.omitImageGUID {
		imageVariant["guid"] = appPassMockImageGUID
	}
	if !m.flags.omitImageSizeKeys {
		if !m.flags.imageOmitStandard {
			imageVariant["standard"] = m.server.URL + "/cdn/image/101-standard.png"
		}
		imageVariant["original"] = m.server.URL + "/cdn/image/101-original.png"
	}
	steps := []interface{}{
		map[string]interface{}{
			"stepid": 601,
			"media": map[string]interface{}{
				"type": "image",
				"data": []interface{}{imageVariant},
			},
		},
	}
	if !m.flags.roundTripOmitObject {
		videoData := map[string]interface{}{"id": 103}
		encodings := []interface{}{}
		if !m.flags.omitEncodingsURL {
			encodings = append(encodings, map[string]interface{}{
				"url": m.server.URL + "/cdn/video/103/encoding", "format": "mp4", "width": 640, "height": 480,
			})
		}
		videoData["encodings"] = encodings
		steps = append(steps, map[string]interface{}{
			"stepid": 602,
			"media": map[string]interface{}{
				// READ type, deliberately not the "object" write type — the API is
				// asymmetric here (VERIFIED live) and matching the write value on
				// read-back finds no video step at all.
				"type": "video",
				"data": videoData,
			},
		})
	}
	doc := map[string]interface{}{"documentid": 102.0}
	if !m.flags.omitDocumentURL {
		doc["url"] = appPassMockDocumentPath
	}
	guide := map[string]interface{}{
		"guideid":    500,
		"revisionid": appPassMockGuideRevisionID,
		"public":     published && !m.flags.publishGetPublicFalse,
		"url":        m.server.URL + "/Guide/QA-Harness-Guide/500",
		"steps":      steps,
		"documents":  []interface{}{doc},
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(guide)
}

// ---------------------------------------------------------------------------------
// full-pass integration: happy path, per-stage negatives, teardown ordering
// ---------------------------------------------------------------------------------

// pathBExecFn returns a fakeExec function that answers the whole stage-0 sequence for
// path B, used by every full-pass integration test below (path A is covered
// separately by the stage-0-specific tests above).
func pathBExecFn() func(argv []string, stdin []byte) (stdout, stderr []byte, err error) {
	return func(argv []string, stdin []byte) ([]byte, []byte, error) {
		joined := argvString(argv)
		switch {
		case strings.Contains(joined, "get pods"):
			return []byte("app-pod-1\n"), nil, nil
		// MUST precede the site_index case: the allowlist fixture's INSERT also selects
		// FROM site_index, so matching on site_index first would answer the fixture with
		// a one-line site name and fail it on line count.
		case strings.Contains(joined, "document_extension_allowed"):
			return []byte("0\n80\n80\n"), nil, nil
		case strings.Contains(joined, "site_index"):
			return []byte("acme\n"), nil, nil
		case strings.Contains(joined, "essentials.php"):
			return []byte("saltXYZ\thashXYZ\n"), nil, nil
		case strings.Contains(joined, "SELECT userid"):
			return []byte("42\n"), nil, nil
		case strings.Contains(joined, "UPDATE"):
			return []byte("1\n"), nil, nil
		case strings.Contains(joined, "Exec/sites.php") && !strings.Contains(joined, "create-site-admin"):
			return []byte("Usage: sites.php <command>\n  list-sites\n"), nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected exec call: %v", argv)
		}
	}
}

// TestGuideTypeHowTo_IsLiteralHowTo pins the guide `type` contract independently of
// the mock: the create-guide mock above asserts against the literal "how-to" (not the
// guideTypeHowTo constant it was built from), and this test independently pins the
// constant itself, so a regression on either side is caught.
func TestGuideTypeHowTo_IsLiteralHowTo(t *testing.T) {
	if guideTypeHowTo != "how-to" {
		t.Fatalf("guideTypeHowTo = %q, want the literal \"how-to\"", guideTypeHowTo)
	}
}

func TestRunAppPass_HappyPath(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{})
	defer mock.server.Close()

	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	err := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantDeleteOrder := []string{"course", "guide", "media:video", "media:document", "media:image", "wiki"}
	if !stringSliceEqual(mock.deleteLog, wantDeleteOrder) {
		t.Errorf("teardown order = %v, want %v", mock.deleteLog, wantDeleteOrder)
	}

	wantMarkers := []string{
		"stage 0 (ephemeral admin) ok path=B",
		"stage 1 (login) ok",
		"stage 2 (register user) ok",
		"stage 3 (create wiki) ok",
		"stage 4 (media uploads) ok",
		"stage 5 (guide + steps) ok",
		"stage 6 (publish) ok",
		"stage 7 (course) ok",
	}
	lines := log.Lines()
	for _, marker := range wantMarkers {
		found := false
		for _, l := range lines {
			if strings.Contains(l, marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected log line containing %q; got lines: %v", marker, lines)
		}
	}
}

// TestRunAppPass_PrivateSiteSkipsPublicDelivery covers David's Decision 2(ii): on a site
// that refuses anonymous reads, stage 6 must log the skip and PASS rather than fail.
//
// Every anonymous endpoint is wired to 500 here on purpose. A gate that logged the skip
// line but still issued the requests would fail the pass on those 500s, so this asserts
// the skip actually skipped rather than merely announcing itself.
func TestRunAppPass_PrivateSiteSkipsPublicDelivery(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{
		anonGuideReadStatus: http.StatusUnauthorized,
		publicPageStatus:    http.StatusInternalServerError,
		imageCDNStatus:      http.StatusInternalServerError,
		videoEncStatus:      http.StatusInternalServerError,
		docFetchStatus:      http.StatusInternalServerError,
	})
	defer mock.server.Close()

	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	opts := testAppPassOpts(mock.server.URL)
	opts.PublicDelivery = false // the config declares this site private
	err := runAppPass(context.Background(), opts, deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt001")
	if err != nil {
		t.Fatalf("private site must not fail the pass, got: %v", err)
	}

	// The skip must be visible in the log. WS5's proof definition counts this exact
	// line as stage 6's pass on a private config, so the wording is load-bearing.
	wantSkip := "app-pass: stage6 public-delivery SKIPPED (site private)"
	found := false
	for _, l := range log.Lines() {
		if strings.Contains(l, wantSkip) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("missing the skip log line %q; got lines: %v", wantSkip, log.Lines())
	}

	// A skip is not an abort: the stage still has to report ok and the run has to go on
	// to stage 7 and tear down.
	for _, marker := range []string{"stage 6 (publish) ok", "stage 7 (course) ok"} {
		hit := false
		for _, l := range log.Lines() {
			if strings.Contains(l, marker) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("missing expected log line containing %q; got lines: %v", marker, log.Lines())
		}
	}
}

// TestRunAppPass_PrivacyGateAssertsBothDirections is the anti-laundering test. The
// danger of any skip-on-condition gate is that it converts a real failure into a pass,
// so neither declared config may be a soft spot:
//
//   - declared private but the site answers anonymously: it is serving content it
//     should not be, and that must fail rather than skip
//   - declared public but the site demands auth: that is exactly the regression an
//     earlier revision of this gate would have misread as "private" and passed, which
//     is why config declares and the probe only verifies
func TestRunAppPass_PrivacyGateAssertsBothDirections(t *testing.T) {
	cases := []struct {
		name           string
		publicDelivery bool
		probeStatus    int
		wantErr        string
	}{
		{"private config but anonymous read succeeds", false, http.StatusOK, "declares this site private"},
		{"private config and guide simply missing", false, http.StatusNotFound, "declares this site private"},
		{"public config but anonymous read is refused", true, http.StatusUnauthorized, "declares this site public"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newAppPassMock(t, appPassMockFlags{anonGuideReadStatus: tc.probeStatus})
			defer mock.server.Close()

			fe := &fakeExec{fn: pathBExecFn()}
			clk := newFakeAppPassClock()
			deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
			log := newAppPassLogger()

			opts := testAppPassOpts(mock.server.URL)
			opts.PublicDelivery = tc.publicDelivery
			err := runAppPass(context.Background(), opts, deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt001")
			if err == nil {
				t.Fatal("expected stage 6 to fail, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should contain %q, got: %v", tc.wantErr, err)
			}
			for _, l := range log.Lines() {
				if strings.Contains(l, "SKIPPED (site private)") {
					t.Errorf("a failing gate must not also log the skip line; got: %v", log.Lines())
				}
			}
		})
	}
}

// TestRunAppPass_HappyPath_ImageFallsBackToOriginalSize proves appPassGuideImageURL's
// size-key preference actually has a working fallback: with "standard" absent and only
// "original" present, the pass must still succeed (and the relative documents[].url
// must still resolve and be fetched — the mock only serves it at its real relative
// path, so a broken join would 404 here too).
func TestRunAppPass_HappyPath_ImageFallsBackToOriginalSize(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{imageOmitStandard: true})
	defer mock.server.Close()

	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	if err := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt005"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRunAppPass_NegativeStages exercises one failure mode per stage 1-7 (stage 0's
// failure modes are covered by the exec-boundary tests above) and asserts each one
// fails the WHOLE pass.
func TestRunAppPass_NegativeStages(t *testing.T) {
	cases := []struct {
		name  string
		flags appPassMockFlags
		want  string
	}{
		{"stage1 login not 201", appPassMockFlags{loginStatus: http.StatusOK}, "stage 1 (login)"},
		{"stage2 register missing userid", appPassMockFlags{registerOmitUserID: true}, "stage 2 (register user)"},
		{"stage3 create wiki fails", appPassMockFlags{wikiStatus: http.StatusInternalServerError}, "stage 3 (create wiki)"},
		{"stage4 video never ready", appPassMockFlags{videoNeverReady: true}, "stage 4 (media uploads)"},
		{"stage4 upload not 200", appPassMockFlags{uploadStatus: http.StatusInternalServerError}, "stage 4 (media uploads)"},
		{"stage5 create guide not 201", appPassMockFlags{guideCreateStatus: http.StatusInternalServerError}, "stage 5 (guide + steps)"},
		{"stage5 add step not 201", appPassMockFlags{addStepStatus: http.StatusInternalServerError}, "stage 5 (guide + steps)"},
		{"stage5 round trip missing object media", appPassMockFlags{roundTripOmitObject: true}, "stage 5 (guide + steps)"},
		{"stage6 publish PUT not 200", appPassMockFlags{publishPutStatus: http.StatusInternalServerError}, "stage 6 (publish)"},
		{"stage6 public flag false after publish", appPassMockFlags{publishGetPublicFalse: true}, "stage 6 (publish)"},
		{"stage6 image missing guid", appPassMockFlags{omitImageGUID: true}, "stage 6 (publish)"},
		{"stage6 image missing standard and original size keys", appPassMockFlags{omitImageSizeKeys: true}, "stage 6 (publish)"},
		{"stage6 video encodings has no url", appPassMockFlags{omitEncodingsURL: true}, "stage 6 (publish)"},
		{"stage6 document missing url", appPassMockFlags{omitDocumentURL: true}, "stage 6 (publish)"},
		{"stage6 public page missing image guid", appPassMockFlags{publicPageOmitGUID: true}, "stage 6 (publish)"},
		{"stage6 public page non-2xx", appPassMockFlags{publicPageStatus: http.StatusNotFound}, "stage 6 (publish)"},
		{"stage6 image CDN GET non-200", appPassMockFlags{imageCDNStatus: http.StatusInternalServerError}, "stage 6 (publish)"},
		{"stage6 video encoding GET non-200", appPassMockFlags{videoEncStatus: http.StatusInternalServerError}, "stage 6 (publish)"},
		{"stage6 document GET non-200", appPassMockFlags{docFetchStatus: http.StatusInternalServerError}, "stage 6 (publish)"},
		{"stage7 create course not 201", appPassMockFlags{courseCreateStatus: http.StatusInternalServerError}, "stage 7 (course)"},
		{"stage7 getCourse wrong wikiid", appPassMockFlags{getCourseWrongWikiID: true}, "stage 7 (course)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newAppPassMock(t, tc.flags)
			defer mock.server.Close()

			fe := &fakeExec{fn: pathBExecFn()}
			clk := newFakeAppPassClock()
			deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
			log := newAppPassLogger()

			err := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt002")
			if err == nil {
				t.Fatal("want the whole pass to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestRunAppPass_TeardownDrainsOnMidPassFailure proves stage 8 still runs and drains
// everything created so far even when an earlier/later stage fails the pass, and that
// it drains in reverse-creation (LIFO) order.
func TestRunAppPass_TeardownDrainsOnMidPassFailure(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{getCourseWrongWikiID: true})
	defer mock.server.Close()

	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	err := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt003")
	if err == nil {
		t.Fatal("want stage 7 to fail the pass")
	}

	// Stage 7 fails AFTER the course is created and its teardown entry pushed, so the
	// full LIFO drain still includes it: course, guide, video, document, image, wiki.
	wantDeleteOrder := []string{"course", "guide", "media:video", "media:document", "media:image", "wiki"}
	if !stringSliceEqual(mock.deleteLog, wantDeleteOrder) {
		t.Errorf("teardown order = %v, want %v", mock.deleteLog, wantDeleteOrder)
	}

	sawFailure := false
	for _, l := range log.Lines() {
		if strings.Contains(l, "stage 8 cleanup") {
			t.Errorf("did not expect any stage 8 cleanup failures in this scenario, got: %s", l)
		}
		if strings.Contains(l, "stage 8 (teardown) draining 6 entries") {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Error("expected a 'draining 6 entries' log line")
	}
}

// TestRunAppPass_TeardownDrainsAfterRealCeilingExpires proves opts.Timeout is a REAL
// ceiling (stageCtx = context.WithTimeout, not just the fake-clock-driven checkDeadline
// poll between stages) AND that stage 8 still runs — and actually SUCCEEDS — on an
// independent context once that ceiling has fired mid-flight.
//
// Deterministic by construction, not by timing luck: the mock's guide-create handler
// (stage 5) sleeps far longer (2s) than the pass's own ceiling (300ms), so stage 5's
// in-flight HTTP call is guaranteed to be interrupted by stageCtx's expiry long before
// the mock would ever respond — stages 0-4 (in-memory exec + a handful of fast
// loopback calls) have ample headroom to finish inside 300ms either way. If teardown
// drained on the SAME (now-expired) stageCtx instead of its own independent context,
// every one of these deletes would fail immediately with a context error; asserting
// they all succeed is what proves the independent-context fix, not just that stage 8
// was invoked.
func TestRunAppPass_TeardownDrainsAfterRealCeilingExpires(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{guideCreateDelay: 2 * time.Second})
	defer mock.server.Close()

	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	opts := testAppPassOpts(mock.server.URL)
	opts.Timeout = 300 * time.Millisecond

	err := runAppPass(context.Background(), opts, deps, log, "deadbeefdeadbeefdeadbeefdeadbeef", "salt009")
	if err == nil {
		t.Fatal("want the pass to fail once the real ceiling trips mid-flight")
	}
	if !strings.Contains(err.Error(), "stage 5") {
		t.Errorf("error = %q, want it to name stage 5 (where the ceiling should trip)", err.Error())
	}

	wantDeleteOrder := []string{"media:video", "media:document", "media:image", "wiki"}
	if !stringSliceEqual(mock.deleteLog, wantDeleteOrder) {
		t.Errorf("teardown deletes after ceiling expiry = %v, want %v (stage 8 must drain, and SUCCEED, on an independent context)", mock.deleteLog, wantDeleteOrder)
	}
}

// ---------------------------------------------------------------------------------
// ID field extraction
// ---------------------------------------------------------------------------------

func TestIdField_RejectsFractionalAndNonPositive(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"fractional", `{"userid": 12.9}`},
		{"zero", `{"userid": 0}`},
		{"negative", `{"userid": -1}`},
		{"zero string", `{"userid": "0"}`},
		{"non-numeric string", `{"userid": "abc"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(tc.json), &m); err != nil {
				t.Fatalf("bad test JSON: %v", err)
			}
			if id, ok := idField(m, "userid"); ok {
				t.Errorf("idField(%s) = (%d, true), want ok=false", tc.json, id)
			}
		})
	}
}

func TestIdField_AcceptsPositiveIntegral(t *testing.T) {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(`{"userid": 42, "wikiid": "17"}`), &m); err != nil {
		t.Fatalf("bad test JSON: %v", err)
	}
	if id, ok := idField(m, "userid"); !ok || id != 42 {
		t.Errorf("idField(userid) = (%d, %v), want (42, true)", id, ok)
	}
	if id, ok := idField(m, "wikiid"); !ok || id != 17 {
		t.Errorf("idField(wikiid) = (%d, %v), want (17, true)", id, ok)
	}
}

// ---------------------------------------------------------------------------------
// redaction
// ---------------------------------------------------------------------------------

func TestFinalizeAppPass_RedactsLeakedSecret(t *testing.T) {
	secret := "deadbeefdeadbeefdeadbeefdeadbeef"
	log := newAppPassLogger()
	log.Logf("app-pass: stage 1 (login) starting")
	// Simulate an accidental leak (e.g. a future bug) to prove the self-scan catches
	// it regardless of where in the pass it happened.
	log.Logf("app-pass: BUG this line accidentally contains %s", secret)

	err := finalizeAppPass(log, nil, secret)
	if err == nil {
		t.Fatal("want an error when the secret leaked into the logs")
	}
	if !strings.Contains(err.Error(), "SECRET LEAKED TO LOGS") {
		t.Errorf("error = %q, want it to contain SECRET LEAKED TO LOGS", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the leak-detection error itself must not repeat the secret: %q", err.Error())
	}
}

func TestFinalizeAppPass_ErrorTextScanned(t *testing.T) {
	secret := "deadbeefdeadbeefdeadbeefdeadbeef"
	log := newAppPassLogger()
	runErr := fmt.Errorf("app-pass: stage 1 (login): oops leaked %s here", secret)

	err := finalizeAppPass(log, runErr, secret)
	if err == nil || !strings.Contains(err.Error(), "SECRET LEAKED TO LOGS") {
		t.Errorf("error = %v, want SECRET LEAKED TO LOGS (the run error's own text must be scanned too)", err)
	}
}

func TestFinalizeAppPass_NoLeakPassesThrough(t *testing.T) {
	secret := "deadbeefdeadbeefdeadbeefdeadbeef"
	log := newAppPassLogger()
	log.Logf("app-pass: stage 1 (login) ok userid=1")

	if err := finalizeAppPass(log, nil, secret); err != nil {
		t.Errorf("unexpected error on a clean success: %v", err)
	}

	runErr := fmt.Errorf("app-pass: stage 3 (create wiki): want status 201, got 500")
	got := finalizeAppPass(log, runErr, secret)
	if got != runErr {
		t.Errorf("finalizeAppPass altered a clean error: got %v, want the original %v", got, runErr)
	}
	for _, l := range log.Lines() {
		if strings.Contains(l, secret) {
			t.Errorf("captured log line unexpectedly contains the secret: %q", l)
		}
	}
}

// TestRunAppPass_HappyPath_NoSecretInLogs is redaction layer (a): after a full,
// successful run, no captured log line contains the generated password.
func TestRunAppPass_HappyPath_NoSecretInLogs(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{})
	defer mock.server.Close()

	secret := "deadbeefdeadbeefdeadbeefdeadbeef"
	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk}
	log := newAppPassLogger()

	if err := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, secret, "salt004"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, l := range log.Lines() {
		if strings.Contains(l, secret) {
			t.Errorf("log line contains the secret: %q", l)
		}
	}
	// And no exec call put it anywhere but stdin.
	for _, c := range fe.calls {
		for _, a := range c.argv {
			if strings.Contains(a, secret) {
				t.Errorf("argv element contains the secret: %q", a)
			}
		}
	}
}

// TestRunAppPass_Stage2PasswordIncludedInLeakScan proves finalizeAppPass's redaction
// self-scan covers stage 2's generated registration password too, not just the
// stage-0/1 admin secret: the scanner captures all three values over a real run (the
// stage-0/1 admin password, the stage-1 auth token, and the stage-2 registration
// password), and a deliberate leak of the stage-2 password (a future bug, same as
// TestFinalizeAppPass_RedactsLeakedSecret's stage-0/1 case) is still caught.
func TestRunAppPass_Stage2PasswordIncludedInLeakScan(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{})
	defer mock.server.Close()

	mainSecret := "deadbeefdeadbeefdeadbeefdeadbeef"
	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	scanner := &appPassSecretScanner{}
	scanner.add(mainSecret)
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk, secrets: scanner}
	log := newAppPassLogger()

	if err := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, mainSecret, "salt010"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	secrets := scanner.list()
	if len(secrets) != 3 {
		t.Fatalf("scanner captured %d secret(s), want 3 (stage 0/1 admin password + stage 1 auth token + stage 2 registration password): %v", len(secrets), secrets)
	}
	if secrets[1] != "tok-1" {
		t.Errorf("secrets[1] = %q, want the stage-1 auth token \"tok-1\"", secrets[1])
	}
	stage2Password := secrets[2]
	if stage2Password == "" || stage2Password == mainSecret || len(stage2Password) != appPassRegistrationPasswordLength {
		t.Fatalf("unexpected stage-2 password captured: %q", stage2Password)
	}
	if err := appPassPasswordMeetsPolicy(stage2Password); err != nil {
		t.Errorf("stage-2 password fails its own generator's complexity policy: %v", err)
	}

	// The real run above never leaked it (finalizeAppPass would have converted the
	// result already) — now simulate a future bug leaking it and confirm the scan
	// still catches it when both secrets are passed through.
	log.Logf("app-pass: BUG this line accidentally contains %s", stage2Password)
	if err := finalizeAppPass(log, nil, secrets...); err == nil || !strings.Contains(err.Error(), "SECRET LEAKED TO LOGS") {
		t.Errorf("finalizeAppPass did not catch a leaked stage-2 password: %v", err)
	}
}

// TestRunAppPass_AuthTokenNotLeakedOnErrorEcho proves the stage-1 auth token is
// registered with the secret scanner (like the stage-0/1 and stage-2 passwords), so a
// later failure whose response body echoes that live token back — a 4xx that echoes
// request state, e.g. a validation error — cannot leak it in the error AppPass returns.
func TestRunAppPass_AuthTokenNotLeakedOnErrorEcho(t *testing.T) {
	mock := newAppPassMock(t, appPassMockFlags{
		wikiStatus:               http.StatusUnprocessableEntity,
		echoAuthTokenInWikiError: true,
	})
	defer mock.server.Close()

	mainSecret := "deadbeefdeadbeefdeadbeefdeadbeef"
	fe := &fakeExec{fn: pathBExecFn()}
	clk := newFakeAppPassClock()
	scanner := &appPassSecretScanner{}
	scanner.add(mainSecret)
	deps := appPassDeps{exec: fe.runner(), client: newAppPassClient(), anonClient: newAppPassAnonClient(), clock: clk, secrets: scanner}
	log := newAppPassLogger()

	runErr := runAppPass(context.Background(), testAppPassOpts(mock.server.URL), deps, log, mainSecret, "salt011")
	if runErr == nil {
		t.Fatal("want stage 3 (create wiki) to fail")
	}

	secrets := scanner.list()
	found := false
	for _, s := range secrets {
		if s == "tok-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("scanner did not capture the stage-1 auth token; captured: %v", secrets)
	}

	// Confirm the test actually exercises the risk: the raw stage error legitimately
	// contains the echoed token, proving the mock's error body really does carry it.
	if !strings.Contains(runErr.Error(), "tok-1") {
		t.Fatalf("test setup didn't actually echo the token into the raw error: %v", runErr)
	}

	finalErr := finalizeAppPass(log, runErr, secrets...)
	if finalErr == nil || !strings.Contains(finalErr.Error(), "SECRET LEAKED TO LOGS") {
		t.Errorf("finalizeAppPass did not catch the leaked auth token: %v", finalErr)
	}
	if strings.Contains(finalErr.Error(), "tok-1") {
		t.Errorf("final error must not repeat the leaked auth token: %q", finalErr.Error())
	}
}

// ---------------------------------------------------------------------------------
// secret / salt generation
// ---------------------------------------------------------------------------------

func TestGenerateAppPassSecret_Format(t *testing.T) {
	s, err := generateAppPassSecret()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s) != 32 {
		t.Errorf("secret length = %d, want 32 hex chars", len(s))
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("secret contains non-hex character %q", c)
		}
	}
	s2, _ := generateAppPassSecret()
	if s == s2 {
		t.Error("two generated secrets were identical — crypto/rand not being used?")
	}
}

func TestGenerateAppPassRegistrationPassword_MeetsPolicy(t *testing.T) {
	pw, err := generateAppPassRegistrationPassword()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pw) != appPassRegistrationPasswordLength {
		t.Errorf("password length = %d, want %d", len(pw), appPassRegistrationPasswordLength)
	}
	if err := appPassPasswordMeetsPolicy(pw); err != nil {
		t.Errorf("generated password fails its own policy: %v", err)
	}
	pw2, _ := generateAppPassRegistrationPassword()
	if pw == pw2 {
		t.Error("two generated passwords were identical — crypto/rand not being used?")
	}
}

func TestAppPassPasswordMeetsPolicy_RejectsWeakShapes(t *testing.T) {
	cases := []struct {
		name string
		pw   string
	}{
		{"too short", "Ab1!Ab1!Ab1"},
		{"no upper", "abcdefgh1234!@#$"},
		{"no lower", "ABCDEFGH1234!@#$"},
		{"no digit", "ABCDefgh!@#$WXYZ"},
		{"no special", "ABCDefgh12345678"},
		// The exact shape this fix replaces: 32 lowercase hex characters.
		{"old all-hex shape", "deadbeefdeadbeefdeadbeefdeadbeef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := appPassPasswordMeetsPolicy(tc.pw); err == nil {
				t.Errorf("appPassPasswordMeetsPolicy(%q) = nil, want an error", tc.pw)
			}
		})
	}
}

func TestAppPassUsernamePattern_RejectsHyphensAcceptsUnderscores(t *testing.T) {
	if appPassUsernamePattern.MatchString("qa-harness-user-deadbeef") {
		t.Error("pattern accepted a hyphenated username, want rejection")
	}
	if !appPassUsernamePattern.MatchString("qa_harness_user_deadbeef") {
		t.Error("pattern rejected a valid underscored username")
	}
}

// ---------------------------------------------------------------------------------
// allowlist FIXTURE (masks the document_extension_allowed provisioning defect)
//
// These guard the two properties that make the fixture safe to carry: it must seed
// per-site rather than per-stack (a stack with one seeded subsite must still repair
// base/onprem), and it must fail loudly rather than let stage 4 report the miss as a
// media-contract defect.
// ---------------------------------------------------------------------------------

func TestSeedAppPassAllowlist_SeedsAndReportsCounts(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("0\n80\n80\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	log := newAppPassLogger()

	if err := seedAppPassAllowlist(context.Background(), deps, testAppPassOpts("https://127.0.0.1:9999"), "app-pod-1", log); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fe.calls) != 1 {
		t.Fatalf("exec calls = %d, want exactly 1 (the seed must be ONE mysql session: ROW_COUNT() is connection-scoped)", len(fe.calls))
	}
	if fe.calls[0].stdin != nil {
		t.Errorf("stdin = %q, want nil", fe.calls[0].stdin)
	}

	argv := fe.calls[0].argv
	wantPrefix := []string{"--kubeconfig", "kc", "-n", "ns", "exec", "app-pod-1", "-c", "app", "--", "bash", "-c"}
	if len(argv) != len(wantPrefix)+1 {
		t.Fatalf("argv = %v, want the standard exec prefix plus one script argument", argv)
	}
	if !argvEqual(argv[:len(wantPrefix)], wantPrefix) {
		t.Errorf("argv prefix mismatch:\n got:  %v\n want: %v", argv[:len(wantPrefix)], wantPrefix)
	}

	var line string
	for _, l := range log.Lines() {
		if strings.Contains(l, "allowlist FIXTURE") {
			line = l
		}
	}
	if line == "" {
		t.Fatal("no allowlist FIXTURE log line emitted")
	}
	for _, want := range []string{"before=0", "inserted=80", "after=80"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q missing %q", line, want)
		}
	}
}

// The script is asserted property by property rather than as one golden string: each
// check below names a specific way the fixture could silently stop doing its job.
func TestSeedAppPassAllowlist_ScriptCarriesItsGuarantees(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("0\n80\n80\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	if err := seedAppPassAllowlist(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod", newAppPassLogger()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	script := fe.calls[0].argv[len(fe.calls[0].argv)-1]

	checks := []struct{ want, why string }{
		{". /bootstrap/helpers/db_helpers.sh", "must source db_helpers.sh for $mysqlcmd"},
		{"$mysqlcmd -sN -D sites", "the three tables all live in the sites DB"},
		{"NOT EXISTS", "without the guard a re-run duplicate-keys against PRIMARY KEY (siteid, document_extension)"},
		// Both correlation predicates, asserted separately. A NOT EXISTS that is present
		// but under-correlated is the dangerous shape: correlating on siteid alone makes
		// the subquery true as soon as a site has any row, so a site holding a partial
		// allowlist never receives its missing extensions and the seed looks like it
		// worked. Testing only for the words "NOT EXISTS" cannot see that.
		{"dea.siteid = s.siteid", "the guard must correlate on the site"},
		{"dea.document_extension = dfi.document_extension", "the guard must ALSO correlate on the extension, or a partially seeded site is skipped"},
		{"CROSS JOIN", "every site must be crossed with every allowed extension"},
		{"'image','pdf','3d_model','video'", "the four MediaGroup values initializeAllowedDocumentTypes seeds"},
		{"SELECT ROW_COUNT();", "the inserted count is read from the same session"},
	}
	for _, c := range checks {
		if !strings.Contains(script, c.want) {
			t.Errorf("script missing %q: %s", c.want, c.why)
		}
	}
	// Per-site, not per-stack. A stack-wide short circuit would skip base/onprem on any
	// stack that already has a product-created subsite — the exact shape this repairs.
	if strings.Contains(script, "COUNT(*) FROM document_extension_allowed WHERE") {
		t.Error("script appears to gate on a per-stack count; the guard must be the per-row NOT EXISTS")
	}
}

func TestSeedAppPassAllowlist_AlreadySeededInsertsNothing(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("80\n0\n80\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	log := newAppPassLogger()
	if err := seedAppPassAllowlist(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod", log); err != nil {
		t.Fatalf("a fully seeded stack must be a clean no-op, got: %v", err)
	}
	var found bool
	for _, l := range log.Lines() {
		if strings.Contains(l, "inserted=0") && strings.Contains(l, "after=80") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a no-op line reporting inserted=0 after=80, got %v", log.Lines())
	}
}

func TestSeedAppPassAllowlist_ZeroRowsAfterSeedingFails(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return []byte("0\n0\n0\n"), nil, nil
	}}
	deps := appPassDeps{exec: fe.runner()}
	err := seedAppPassAllowlist(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod", newAppPassLogger())
	if err == nil {
		t.Fatal("want an error: 0 rows after seeding guarantees stage 4 will 422")
	}
	if !strings.Contains(err.Error(), "document_file_information") {
		t.Errorf("error should name the likely cause, got: %v", err)
	}
}

func TestSeedAppPassAllowlist_MalformedOutputFails(t *testing.T) {
	for name, out := range map[string]string{
		"too few lines":  "0\n80\n",
		"too many lines": "0\n80\n80\n80\n",
		"empty":          "",
		"non-numeric":    "0\nlots\n80\n",
	} {
		t.Run(name, func(t *testing.T) {
			fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
				return []byte(out), nil, nil
			}}
			deps := appPassDeps{exec: fe.runner()}
			if err := seedAppPassAllowlist(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod", newAppPassLogger()); err == nil {
				t.Fatalf("want error for %s output %q", name, out)
			}
		})
	}
}

func TestSeedAppPassAllowlist_ExecErrorFails(t *testing.T) {
	fe := &fakeExec{fn: func(argv []string, stdin []byte) ([]byte, []byte, error) {
		return nil, []byte("exit 92"), fmt.Errorf("db_helpers.sh missing")
	}}
	deps := appPassDeps{exec: fe.runner()}
	err := seedAppPassAllowlist(context.Background(), deps, testAppPassOpts("https://127.0.0.1:1"), "pod", newAppPassLogger())
	if err == nil {
		t.Fatal("want error when the exec itself fails")
	}
	if !strings.Contains(err.Error(), "allowlist fixture") {
		t.Errorf("error must attribute itself to the fixture, not read as a stage-4 defect: %v", err)
	}
}
