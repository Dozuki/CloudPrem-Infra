package harness

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTGEnv(t *testing.T) {
	opt := TGOptions{
		AccountID:    "076000000000",
		Region:       "us-east-1",
		Profile:      "ddvtest",
		BucketPrefix: "run123-",
		StatePrefix:  "run123-min/",
	}
	env := opt.env()
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"TG_AWS_ACCT_ID=076000000000",
		"TG_AWS_REGION=us-east-1",
		"TG_AWS_PROFILE=ddvtest",
		"TG_BUCKET_PREFIX=run123-",
		"TG_STATE_PREFIX=run123-min/",
		"TG_NON_INTERACTIVE=true",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("env missing %q", want)
		}
	}
}

// writeFakeTerragrunt drops an executable named "terragrunt" on the front of PATH
// that prints script (a tofu-shaped error block, none of which matches any of the
// retry regexes above) and exits 1. Tests use this instead of the real binary so
// Apply/destroyModule fail on the first attempt, deterministically and fast.
func writeFakeTerragrunt(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	body := "#!/bin/sh\n" + script + "\nexit 1\n"
	path := filepath.Join(bin, "terragrunt")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake terragrunt: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fakeTofuErrorScript = `echo 'module.foo: Still destroying... [id=x, 1s elapsed]'
echo 'Error: stopping widget (arn:aws:widget:us-east-1:000000000000:thing/abc): cannot proceed'
echo 'exit status 1'`

func TestApplyErrorCarriesOutputTail(t *testing.T) {
	writeFakeTerragrunt(t, fakeTofuErrorScript)
	o := TGOptions{WorkingDir: t.TempDir(), Region: "us-east-1"}

	err := o.Apply()
	if err == nil {
		t.Fatal("Apply() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "stopping widget") || !strings.Contains(err.Error(), "cannot proceed") {
		t.Errorf("Apply() error = %q, want it to carry the tofu error text", err.Error())
	}
	if len(err.Error()) > 500 {
		t.Errorf("Apply() error is %d chars, want it capped near 400", len(err.Error()))
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("Apply() error does not unwrap to *exec.ExitError: %v", err)
	}
}

func TestDestroyModuleErrorCarriesOutputTail(t *testing.T) {
	writeFakeTerragrunt(t, fakeTofuErrorScript)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logical"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := TGOptions{WorkingDir: dir, Region: "us-east-1"}

	err := o.destroyModule("logical")
	if err == nil {
		t.Fatal("destroyModule() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "stopping widget") || !strings.Contains(err.Error(), "cannot proceed") {
		t.Errorf("destroyModule() error = %q, want it to carry the tofu error text", err.Error())
	}
	if len(err.Error()) > 500 {
		t.Errorf("destroyModule() error is %d chars, want it capped near 400", len(err.Error()))
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("destroyModule() error does not unwrap to *exec.ExitError: %v", err)
	}
}
