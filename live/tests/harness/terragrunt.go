package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// kubeAuthRaceRE matches the EKS access-entry propagation race: when physical
// creates the cluster + creator access entry, the logical layer's kubernetes/helm
// providers can call the API before EKS makes the entry effective (~30s-2min),
// yielding "credentials configured in the provider block are not accepted". A
// re-apply after a short wait succeeds — the entry is just slow to propagate.
var kubeAuthRaceRE = regexp.MustCompile(`credentials configured in the provider block are not accepted by the API server`)

type TGOptions struct {
	WorkingDir   string
	AccountID    string
	Region       string
	Profile      string
	BucketPrefix string
	StatePrefix  string
	NLBName      string // "<customer>-<env>" (e.g. smoke-min); for pre-destroy protection clear
}

func (o TGOptions) env() []string {
	return append(os.Environ(),
		"TG_AWS_ACCT_ID="+o.AccountID,
		"TG_AWS_REGION="+o.Region,
		"TG_AWS_PROFILE="+o.Profile,
		"TG_BUCKET_PREFIX="+o.BucketPrefix,
		"TG_STATE_PREFIX="+o.StatePrefix,
		"TG_NON_INTERACTIVE=true",
	)
}

func (o TGOptions) exec(args ...string) error {
	cmd := exec.Command("terragrunt", args...)
	cmd.Dir = o.WorkingDir
	cmd.Env = o.env()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// execCapture runs terragrunt while both streaming output (so the run stays
// visible) and capturing it for error-pattern inspection.
func (o TGOptions) execCapture(args ...string) (string, error) {
	cmd := exec.Command("terragrunt", args...)
	cmd.Dir = o.WorkingDir
	cmd.Env = o.env()
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stderr, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	err := cmd.Run()
	return buf.String(), err
}

func (o TGOptions) Apply() error {
	// Retry only the EKS access-entry propagation race (see kubeAuthRaceRE);
	// terraform apply is idempotent, so a re-apply just finishes the remaining
	// resources once the access entry is effective.
	const maxAttempts = 4
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var out string
		out, err = o.execCapture("run-all", "apply", "--terragrunt-non-interactive", "-auto-approve")
		if err == nil {
			return nil
		}
		if attempt < maxAttempts && kubeAuthRaceRE.MatchString(out) {
			wait := time.Duration(attempt*30) * time.Second
			fmt.Fprintf(os.Stderr, "\n>> harness: EKS access-entry propagation race; retrying apply in %s (attempt %d/%d)\n\n", wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		}
		return err
	}
	return err
}

// destroyModule destroys a single layer in its own directory (not run-all), so
// one layer's failure doesn't abort the others.
func (o TGOptions) destroyModule(module string) error {
	cmd := exec.Command("terragrunt", "destroy", "--terragrunt-non-interactive", "-auto-approve")
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Destroy tears the stack down resiliently. `run-all destroy` aborts the whole
// stack if any module fails — so a broken in-cluster helm release (common after a
// failed upgrade apply) would strand the expensive physical infra (VPC/EKS/RDS),
// forcing a manual teardown. Instead: destroy logical best-effort, then ALWAYS
// destroy physical. Deleting the EKS cluster disposes of any k8s/helm resources
// the logical destroy couldn't, and physical is the layer that actually costs
// money and collides with the next run.
func (o TGOptions) Destroy() error {
	refreshVaultToken()
	if err := o.destroyModule("logical"); err != nil {
		fmt.Fprintf(os.Stderr, "\n>> teardown: logical destroy failed (continuing to physical so infra isn't stranded): %v\n", err)
	}
	o.clearNLBProtection()
	return o.destroyModule("physical")
}

// refreshVaultToken best-effort re-logs-in to Vault before the teardown and updates
// VAULT_TOKEN in the process env. A long run can outlast the AWS-auth token's TTL,
// so the token run.sh logged in with is stale by teardown — the logical destroy then
// 403s on every vault data source (lookup-self against VAULT_ADDR). run.sh's
// port-forward is still up, so we re-login the same way it did. Best-effort: if the
// vault/aws CLIs or VAULT_ADDR are absent, keep the inherited token — the logical
// destroy is best-effort anyway and the cleanup-orphans backstop re-auths too.
func refreshVaultToken() {
	if os.Getenv("VAULT_ADDR") == "" {
		return // no Vault tunnel in this run (e.g. azure / SKIP_VAULT_TUNNEL)
	}
	if _, err := exec.LookPath("vault"); err != nil {
		return
	}
	profile := os.Getenv("VAULT_AWS_PROFILE")
	if profile == "" {
		profile = "dozuki"
	}
	role := os.Getenv("VAULT_AWS_ROLE")
	if role == "" {
		role = "admin"
	}
	// Export the profile's AWS creds, then run `vault login` with those creds in its
	// env. No shell (`sh -c`): args are passed directly, so profile/role can't be
	// interpreted as shell metacharacters (avoids command injection).
	credsOut, err := exec.Command("aws", "--profile", profile, "configure",
		"export-credentials", "--format", "env-no-export").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, ">> teardown: Vault re-login skipped (aws creds for %q: %v) — using inherited token\n", profile, err)
		return
	}
	loginEnv := os.Environ()
	for _, line := range strings.Split(strings.TrimSpace(string(credsOut)), "\n") {
		if strings.HasPrefix(line, "AWS_") {
			loginEnv = append(loginEnv, line)
		}
	}
	login := exec.Command("vault", "login", "-method=aws", "role="+role, "-format=json")
	login.Env = loginEnv
	out, err := login.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, ">> teardown: Vault re-login skipped (%v) — using inherited token; backstop re-auths if needed\n", err)
		return
	}
	var resp struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Auth.ClientToken == "" {
		return
	}
	_ = os.Setenv("VAULT_TOKEN", resp.Auth.ClientToken)
	fmt.Fprintf(os.Stderr, ">> teardown: refreshed Vault token (re-login via aws role=%s) so the logical destroy uses a live token\n", role)
}

// clearNLBProtection disables deletion protection on the stack's NLB before the
// physical destroy. v6.0.x baselines create the NLB protected (no protect_resources
// wiring on the alb module's default), so a baseline-failure teardown would otherwise
// stall ~15-20min on the internet gateway — the protected NLB's public addresses pin
// it (DependencyViolation) — before the cleanup-orphans backstop disables it. This
// makes the harness's own teardown self-sufficient. Best-effort + idempotent: a
// missing NLB or already-cleared protection is a silent no-op.
func (o TGOptions) clearNLBProtection() {
	if o.NLBName == "" {
		return
	}
	out, err := exec.Command("aws", "elbv2", "describe-load-balancers",
		"--region", o.Region, "--profile", o.Profile,
		"--query", fmt.Sprintf("LoadBalancers[?LoadBalancerName=='%s'].LoadBalancerArn|[0]", o.NLBName),
		"--output", "text").Output()
	arn := strings.TrimSpace(string(out))
	if err != nil || arn == "" || arn == "None" {
		return
	}
	if e := exec.Command("aws", "elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", arn,
		"--attributes", "Key=deletion_protection.enabled,Value=false",
		"--region", o.Region, "--profile", o.Profile).Run(); e == nil {
		fmt.Fprintf(os.Stderr, "\n>> teardown: cleared NLB deletion-protection on %s (avoids IGW stall)\n", o.NLBName)
	}
}

func (o TGOptions) Output(module, name string) (string, error) {
	cmd := exec.Command("terragrunt", "output", "-raw", name)
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("terragrunt output %s/%s: %w", module, name, err)
	}
	return string(out), nil
}

func (o TGOptions) OutputJSON(module string) (map[string]interface{}, error) {
	cmd := exec.Command("terragrunt", "output", "-json")
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terragrunt output -json %s: %w", module, err)
	}
	var raw map[string]struct {
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse terragrunt output-json %s: %w", module, err)
	}
	m := map[string]interface{}{}
	for k, v := range raw {
		m[k] = v.Value
	}
	return m, nil
}
