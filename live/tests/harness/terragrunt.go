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

// transientNetRE matches network faults that say nothing about the infrastructure being
// wrong - the request never reached AWS, or the answer never came back. A recovery run
// died on `lookup iam.amazonaws.com: no such host` 47 seconds into the rebuild apply,
// throwing away ~90 minutes of source stack, drill and snapshot. macOS's resolver
// returns "no such host" (not a timeout) when it is overwhelmed, and a terragrunt apply
// fans out enough concurrent provider processes to do that; a plain dig burst against
// the same resolvers came back clean immediately afterwards.
//
// Safe to retry because apply is idempotent - it is the same reasoning the EKS
// access-entry retry above rests on. Deliberately narrow: only failures where the
// request demonstrably did not land. An AWS API error that came back WITH a response
// (AccessDenied, InvalidParameterValue, a 4xx of any kind) is a real failure and must
// still fail the run.
var transientNetRE = regexp.MustCompile(`(?i)no such host|connection reset by peer|i/o timeout|TLS handshake timeout|request send failed|EOF$`)

// vaultTokenExpiredRE matches the vault provider failing to validate its OWN token
// (a 403 on auth/token/lookup-self). The renewer in vault.go is what should keep this
// from ever happening; this is the second net, because the cost of being wrong is an
// entire run - cycle 45 lost ~80 minutes of correct work to exactly this, with the
// rebuild apply already in flight.
//
// Narrow on purpose. It matches only the provider's own token lookup, not "permission
// denied" generally, so a genuine Vault POLICY problem still fails immediately instead
// of being retried into a confusing timeout.
var vaultTokenExpiredRE = regexp.MustCompile(`(?i)failed to lookup token`)

// eksKmsPropagationRE matches the EKS CreateCluster / KMS authorization race. EKS checks
// that the cluster role can use the secrets-encryption key at CreateCluster time. The
// upstream EKS module attaches that permission with an IAM policy attachment that is not
// in aws_eks_cluster's depends_on list, so the two can land in the same second and EKS
// evaluates against a role whose new policy has not propagated. The answer is
// "Access denied to KMS key ... due to explicit deny policy or revoked grant" when there
// is neither a deny nor a revoked grant. Two configs in the 2026-08-01 matrix ran side by
// side: one attached the policy 4s before CreateCluster and passed, the other at +0s and
// failed, with structurally identical key policies resolving the same role.
//
// The AWS provider already retries this class of race for the IAM-role wordings but its
// allowlist does not carry the KMS message. Safe to retry for the usual reason: apply is
// idempotent.
//
// A key policy that genuinely denies the role produces the same sentence, and this rule
// cannot tell the two apart. The cost of being wrong is bounded: three extra attempts,
// about three minutes, then the run fails with the same error it would have failed with
// immediately.
var eksKmsPropagationRE = regexp.MustCompile(`(?s)Access denied to KMS key.*due to explicit deny policy or revoked grant`)

type TGOptions struct {
	WorkingDir   string
	AccountID    string
	Region       string
	Profile      string
	BucketPrefix string
	StatePrefix  string
	// Identifier is "<customer>-<env>" (e.g. smoke-min), the EKS cluster name (see
	// terraform/physical/main.tf's local.identifier). Drives two separate pre-destroy
	// steps (clearNLBProtection, clearMSKClusters) - named for what it IS, not for the
	// first thing it was used for, since a field called NLBName deleting MSK clusters
	// is exactly the kind of thing that misleads the next reader.
	Identifier string
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

// wrapTGError wraps err with the last error-shaped line of out, if there is one.
// lastErrorLine returns "" when the captured output has no line that looks like a
// real error (only noise, or nothing at all) - unconditionally formatting "%w: %s"
// in that case leaves a dangling "exit status 1: " with nothing after the colon,
// which breaks thinVerdictRE's anchored-on-$ match downstream in evidence.go and
// makes ExtractError treat the bare verdict as already explained instead of falling
// through to a real Error: line elsewhere in the log window.
func wrapTGError(err error, out string) error {
	if line := lastErrorLine(out); line != "" {
		return fmt.Errorf("%w: %s", err, line)
	}
	return err
}

func (o TGOptions) Apply() error {
	// Retry only the EKS access-entry propagation race (see kubeAuthRaceRE);
	// terraform apply is idempotent, so a re-apply just finishes the remaining
	// resources once the access entry is effective.
	const maxAttempts = 4
	var err error
	var out string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// No -auto-approve: terragrunt appends it itself for `run --all apply`, and
		// passing it unforwarded is a hard error on 1.x ("flag -auto-approve is not a
		// Terragrunt flag ... use -- to forward it"). `run -- apply -auto-approve` also
		// works but buys nothing here. The destroy path below keeps its -auto-approve
		// because `destroy` is a shortcut command, which does accept it (both verified
		// against a real 1.1.1 binary).
		out, err = o.execCapture("run", "--all", "apply", "--non-interactive")
		if err == nil {
			return nil
		}
		if attempt < maxAttempts && kubeAuthRaceRE.MatchString(out) {
			wait := time.Duration(attempt*30) * time.Second
			fmt.Fprintf(os.Stderr, "\n>> harness: EKS access-entry propagation race; retrying apply in %s (attempt %d/%d)\n\n", wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		}
		if attempt < maxAttempts && eksKmsPropagationRE.MatchString(out) {
			wait := time.Duration(attempt*30) * time.Second
			fmt.Fprintf(os.Stderr, "\n>> harness: EKS/KMS IAM propagation race on CreateCluster; retrying apply in %s (attempt %d/%d)\n\n", wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		}
		// No backoff: a re-login either produced a usable token or it did not, and
		// waiting does not change that. Only retry if the refresh actually succeeded,
		// so a dead AWS session fails on this attempt instead of three more.
		if attempt < maxAttempts && vaultTokenExpiredRE.MatchString(out) {
			if refreshVaultToken() {
				fmt.Fprintf(os.Stderr, "\n>> harness: Vault token had expired; re-logged in and retrying apply (attempt %d/%d)\n\n", attempt+1, maxAttempts)
				continue
			}
			fmt.Fprintf(os.Stderr, "\n>> harness: Vault token expired and re-login failed — not retrying\n\n")
			return wrapTGError(err, out)
		}
		if attempt < maxAttempts && transientNetRE.MatchString(out) {
			wait := time.Duration(attempt*30) * time.Second
			fmt.Fprintf(os.Stderr, "\n>> harness: transient network failure (request never reached AWS); retrying apply in %s (attempt %d/%d)\n\n", wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		}
		return wrapTGError(err, out)
	}
	return wrapTGError(err, out)
}

// destroyModule destroys a single layer in its own directory (not run --all), so
// one layer's failure doesn't abort the others.
//
// Retries the same transient faults Apply does, for the same reason: destroy is
// idempotent, and a request that never reached AWS says nothing about the stack. This
// was missing at first and it was expensive. Cycle 48's recovery-stack physical destroy
// died 9 seconds in on `lookup iam.amazonaws.com: no such host`, which left the stack
// up - and because the recovery stack holds the S3 replication config on the SOURCE
// stack's DR buckets, the source teardown then could not delete their versioning
// ("InvalidBucketState: A replication configuration is present"). One transient DNS
// blip therefore stranded BOTH stacks and 49 resources. Apply had this retry; destroy,
// which is the half that actually costs money when it fails, did not.
func (o TGOptions) destroyModule(module string) error {
	const maxAttempts = 4
	var err error
	var lastOut string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cmd := exec.Command("terragrunt", "destroy", "--non-interactive", "-auto-approve")
		cmd.Dir = filepath.Join(o.WorkingDir, module)
		cmd.Env = o.env()
		var buf bytes.Buffer
		cmd.Stdout = io.MultiWriter(os.Stderr, &buf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
		err = cmd.Run()
		if err == nil {
			return nil
		}
		out := buf.String()
		lastOut = out
		if attempt < maxAttempts && vaultTokenExpiredRE.MatchString(out) && refreshVaultToken() {
			fmt.Fprintf(os.Stderr, "\n>> teardown: Vault token had expired; re-logged in and retrying %s destroy (attempt %d/%d)\n\n", module, attempt+1, maxAttempts)
			continue
		}
		if attempt < maxAttempts && transientNetRE.MatchString(out) {
			wait := time.Duration(attempt*30) * time.Second
			fmt.Fprintf(os.Stderr, "\n>> teardown: transient network failure (request never reached AWS); retrying %s destroy in %s (attempt %d/%d)\n\n", module, wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		}
		return wrapTGError(err, out)
	}
	return wrapTGError(err, lastOut)
}

// Destroy tears the stack down resiliently. `run --all destroy` aborts the whole
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
	o.clearMSKClusters()
	o.clearNLBProtection()
	return o.destroyModule("physical")
}

// refreshVaultToken best-effort re-logs-in to Vault and updates VAULT_TOKEN in the
// process env, returning whether it actually got a token. A long run can outlast the
// AWS-auth token's 1h TTL, and everything that reads Vault then 403s on lookup-self
// against VAULT_ADDR. run.sh's port-forward is still up, so we re-login the same way it
// did. Best-effort: if the vault/aws CLIs or VAULT_ADDR are absent, keep the inherited
// token — the cleanup-orphans backstop re-auths too.
//
// Note this mints a DIFFERENT token, so it only helps processes started afterwards.
// Steady-state renewal (vault.go) keeps the existing token alive instead; this is the
// recovery path for when it is already too late for that.
func refreshVaultToken() bool {
	if os.Getenv("VAULT_ADDR") == "" {
		return false // no Vault tunnel in this run (e.g. azure / SKIP_VAULT_TUNNEL)
	}
	if _, err := exec.LookPath("vault"); err != nil {
		return false
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
		return false
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
		return false
	}
	var resp struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Auth.ClientToken == "" {
		return false
	}
	_ = os.Setenv("VAULT_TOKEN", resp.Auth.ClientToken)
	fmt.Fprintf(os.Stderr, ">> vault: refreshed token (re-login via aws role=%s)\n", role)
	return true
}

// clearNLBProtection disables deletion protection on the stack's NLB before the
// physical destroy. v6.0.x baselines create the NLB protected (no protect_resources
// wiring on the alb module's default), so a baseline-failure teardown would otherwise
// stall ~15-20min on the internet gateway — the protected NLB's public addresses pin
// it (DependencyViolation) — before the cleanup-orphans backstop disables it. This
// makes the harness's own teardown self-sufficient. Best-effort + idempotent: a
// missing NLB or already-cleared protection is a silent no-op.
func (o TGOptions) clearNLBProtection() {
	if o.Identifier == "" {
		return
	}
	out, err := exec.Command("aws", "elbv2", "describe-load-balancers",
		"--region", o.Region, "--profile", o.Profile,
		"--query", fmt.Sprintf("LoadBalancers[?LoadBalancerName=='%s'].LoadBalancerArn|[0]", o.Identifier),
		"--output", "text").Output()
	arn := strings.TrimSpace(string(out))
	if err != nil || arn == "" || arn == "None" {
		return
	}
	if e := exec.Command("aws", "elbv2", "modify-load-balancer-attributes",
		"--load-balancer-arn", arn,
		"--attributes", "Key=deletion_protection.enabled,Value=false",
		"--region", o.Region, "--profile", o.Profile).Run(); e == nil {
		fmt.Fprintf(os.Stderr, "\n>> teardown: cleared NLB deletion-protection on %s (avoids IGW stall)\n", o.Identifier)
	}
}

// clearMSKClusters deletes any MSK cluster named for this stack that Terraform does not
// know about, before the physical destroy. A run killed mid-apply (SIGKILL, no trap) can
// finish creating MSK after state was last written, leaving an ACTIVE cluster absent from
// state - `destroy` then removes the MSK *configuration* but never the cluster, and the
// cluster's ENIs pin every subnet and security group in the VPC, so the VPC destroy fails
// forever and strands the whole network. Ported from cleanup-orphans.sh step 1b.
//
// No aws_msk_* resource exists anywhere in terraform/ at HEAD (verified by grep across
// the module tree) - this exists for cross-architecture upgrade teardowns against an
// older baseline ref that did create one, so a reader who greps for MSK today and finds
// nothing in terraform/ should not delete this as dead code.
//
// Prefix match, where clearNLBProtection right above it is an exact match, and that
// asymmetry is deliberate: the two resources were never named the same way. The NLB
// carried the bare identifier, while the MSK cluster carried a SUFFIX -
// cluster_name = "${local.identifier}-kafka" in terraform/physical/kafka.tf, deleted in
// 832cceb ("remove the webhooks feature and its MSK infrastructure"), which is the only
// baseline this function can still encounter. An exact match on o.Identifier would
// therefore find nothing, ever, and turn the whole function into a silent no-op. The
// prefix is safe to match on because it is the run-salted identifier (Config.Salted
// appends a run+config SHA suffix), so it cannot reach another stack's cluster.
//
// Shells out to the aws CLI, matching clearNLBProtection right above it, rather than
// adding the kafka Go SDK: TGOptions has no AWS SDK client threaded into it today, and
// adding one (plus a go.mod/go.sum entry) for two calls covering a resource type that no
// longer exists in terraform/ is more plumbing than the calls themselves. Best-effort:
// silent no-op on absence or any CLI failure, never fails the destroy.
func (o TGOptions) clearMSKClusters() {
	if o.Identifier == "" {
		return
	}
	out, err := exec.Command("aws", "kafka", "list-clusters",
		"--region", o.Region, "--profile", o.Profile,
		"--query", fmt.Sprintf("ClusterInfoList[?starts_with(ClusterName,'%s')].ClusterArn", o.Identifier),
		"--output", "text").Output()
	if err != nil {
		return
	}
	for _, carn := range strings.Fields(strings.TrimSpace(string(out))) {
		if carn == "" || carn == "None" {
			continue
		}
		if e := exec.Command("aws", "kafka", "delete-cluster",
			"--cluster-arn", carn, "--region", o.Region, "--profile", o.Profile).Run(); e == nil {
			fmt.Fprintf(os.Stderr, "\n>> teardown: MSK cluster delete requested: %s (avoids ENI-pinned VPC destroy failure)\n", carn)
		}
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
