package harness

import (
	"os"
	"testing"
)

// TestResidualEnforceDefaultsToTrue covers HARNESS_RESIDUAL_ENFORCE's whole contract:
// absent, empty, or unparseable must all keep today's behavior (enforce), because a
// laptop run with no env var set, or a lost/mistyped one in a workflow, must not go
// soft on a real orphan by accident. Only an explicit false-y value the stdlib
// recognizes turns the gate report-only.
func TestResidualEnforceDefaultsToTrue(t *testing.T) {
	cases := []struct {
		name string
		env  string // value to set; "" means unset entirely
		set  bool
		want bool
	}{
		{name: "unset", set: false, want: true},
		{name: "empty string", env: "", set: true, want: true},
		{name: "unparseable", env: "nope", set: true, want: true},
		{name: "explicit true", env: "true", set: true, want: true},
		{name: "explicit TRUE", env: "TRUE", set: true, want: true},
		{name: "explicit 1", env: "1", set: true, want: true},
		{name: "explicit false", env: "false", set: true, want: false},
		{name: "explicit FALSE", env: "FALSE", set: true, want: false},
		{name: "explicit 0", env: "0", set: true, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Set then unset, rather than trusting ambient absence: if the var
			// ever leaks into the test process env, the "unset" case would
			// silently stop testing the fail-safe default it exists to protect.
			t.Setenv("HARNESS_RESIDUAL_ENFORCE", "sentinel")
			if c.set {
				t.Setenv("HARNESS_RESIDUAL_ENFORCE", c.env)
			} else {
				os.Unsetenv("HARNESS_RESIDUAL_ENFORCE")
			}
			if got := residualEnforce(); got != c.want {
				t.Errorf("residualEnforce() with env=%q set=%v = %v, want %v", c.env, c.set, got, c.want)
			}
		})
	}
}

// TestResidualBlocksOnlyFailsOnABlockingFindingWithEnforceOn is the decision every one
// of the three gate call sites in phases.go (Provision, Teardown's post-destroy check,
// and Teardown's destroy-error check) makes: a clean or informational-only report never
// fails the phase, and a report with a blocking residual fails it only when enforce is
// true. detection/recording/logging at the call sites happen before this is ever
// asked, so this table only needs to cover the fail/don't-fail decision itself.
func TestResidualBlocksOnlyFailsOnABlockingFindingWithEnforceOn(t *testing.T) {
	blockingReport := &ResidualReport{Residuals: []Residual{
		{ARN: "arn:aws:eks:us-east-1:076248559428:cluster/orphan", Type: "eks:cluster", Blocking: true},
	}}
	informationalReport := &ResidualReport{Residuals: []Residual{
		{ARN: "arn:aws:rds:us-east-1:076248559428:cluster-snapshot:orphan", Type: "rds:cluster-snapshot", Blocking: false, Why: "service-created type"},
	}}
	cleanReport := &ResidualReport{}

	cases := []struct {
		name    string
		rep     *ResidualReport
		enforce bool
		want    bool
	}{
		{name: "blocking finding, enforce on -> fails the phase (provision call site)", rep: blockingReport, enforce: true, want: true},
		{name: "blocking finding, enforce off -> report-only, phase does not fail", rep: blockingReport, enforce: false, want: false},
		{name: "informational finding, enforce on -> never fails", rep: informationalReport, enforce: true, want: false},
		{name: "informational finding, enforce off -> never fails", rep: informationalReport, enforce: false, want: false},
		{name: "clean report, enforce on -> never fails", rep: cleanReport, enforce: true, want: false},
		{name: "clean report, enforce off -> never fails", rep: cleanReport, enforce: false, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := residualBlocks(c.rep, c.enforce); got != c.want {
				t.Errorf("residualBlocks(rep, enforce=%v) = %v, want %v", c.enforce, got, c.want)
			}
		})
	}
}

// TestResidualEnforceEnvVarDrivesTheProvisionCallSiteDecision exercises the actual lever
// an Argo pod sets (HARNESS_RESIDUAL_ENFORCE), through residualEnforce(), feeding the
// same residualBlocks() call the PROVISION residual check makes at phases.go's
// checkResiduals call site - the end-to-end path from env var to gate decision, not just
// the pure function in isolation.
func TestResidualEnforceEnvVarDrivesTheProvisionCallSiteDecision(t *testing.T) {
	blockingReport := &ResidualReport{Residuals: []Residual{
		{Address: "module.eks_cluster.aws_eks_cluster.this[0]", Type: "aws_eks_cluster", Blocking: true},
	}}

	t.Run("HARNESS_RESIDUAL_ENFORCE=false demotes a blocking provision finding", func(t *testing.T) {
		t.Setenv("HARNESS_RESIDUAL_ENFORCE", "false")
		if residualBlocks(blockingReport, residualEnforce()) {
			t.Error("provision residual check should be report-only with HARNESS_RESIDUAL_ENFORCE=false")
		}
	})

	t.Run("HARNESS_RESIDUAL_ENFORCE unset still fails a blocking provision finding", func(t *testing.T) {
		t.Setenv("HARNESS_RESIDUAL_ENFORCE", "sentinel")
		os.Unsetenv("HARNESS_RESIDUAL_ENFORCE")
		if !residualBlocks(blockingReport, residualEnforce()) {
			t.Error("provision residual check must still fail closed with no env var set")
		}
	})
}
