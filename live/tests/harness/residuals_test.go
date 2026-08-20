package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

// The fixture is the real smoke1bc2 shape (Lodestar-1xm.36): the primary Aurora cluster
// tracked only its READER, because the writer was still creating when the provision pod
// took SIGTERM. The EKS cluster is missing for the same reason and only its security
// group survived into state.
const smoke1bc2ShowJSON = `{
  "format_version": "1.0",
  "values": {
    "root_module": {
      "resources": [
        {"address": "aws_db_parameter_group.default", "type": "aws_db_parameter_group", "values": {"id": "smoke1bc2-bi-8f7895f13c1ac08cde23864b70", "arn": "arn:aws:rds:us-east-1:076248559428:pg:smoke1bc2-bi-8f7895f13c1ac08cde23864b70"}}
      ],
      "child_modules": [
        {
          "address": "module.aurora[0]",
          "resources": [
            {"address": "module.aurora[0].aws_rds_cluster.this[0]", "type": "aws_rds_cluster", "values": {"id": "smoke1bc2-bi", "cluster_identifier": "smoke1bc2-bi", "arn": "arn:aws:rds:us-east-1:076248559428:cluster:smoke1bc2-bi"}},
            {"address": "module.aurora[0].aws_rds_cluster_instance.this[\"reader\"]", "type": "aws_rds_cluster_instance", "values": {"id": "smoke1bc2-bi-reader", "arn": "arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-reader"}}
          ]
        },
        {
          "address": "module.eks_cluster",
          "resources": [
            {"address": "module.eks_cluster.aws_security_group.cluster[0]", "type": "aws_security_group", "values": {"id": "sg-04f8b0552e36bda84", "arn": "arn:aws:ec2:us-east-1:076248559428:security-group/sg-04f8b0552e36bda84"}},
            {"address": "module.eks_cluster.aws_eks_pod_identity_association.this[\"ebs-csi\"]", "type": "aws_eks_pod_identity_association", "values": {"id": "a-9lk2m", "association_arn": "arn:aws:eks:us-east-1:076248559428:podidentityassociation/smoke1bc2-bi/a-9lk2m"}}
          ],
          "child_modules": [
            {
              "address": "module.eks_cluster.module.kms",
              "resources": [
                {"address": "module.eks_cluster.module.kms.aws_kms_key.this[0]", "type": "aws_kms_key", "values": {"id": "9aa2aea3-2dc3-4560-a16f-3730a5e98190", "arn": "arn:aws:kms:us-east-1:076248559428:key/9aa2aea3-2dc3-4560-a16f-3730a5e98190"}}
              ]
            }
          ]
        }
      ]
    }
  }
}`

// indexFixture builds the state index the tests below diff against.
func indexFixture(t *testing.T) *stateIndex {
	t.Helper()
	idx := newStateIndex()
	if err := indexState([]byte(smoke1bc2ShowJSON), idx); err != nil {
		t.Fatalf("indexState: %v", err)
	}
	return idx
}

func TestIndexStateWalksNestedModulesAndKeepsTypes(t *testing.T) {
	idx := indexFixture(t)
	for _, want := range []string{
		"arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-reader",
		"arn:aws:kms:us-east-1:076248559428:key/9aa2aea3-2dc3-4560-a16f-3730a5e98190", // two module levels deep
	} {
		if _, ok := idx.arns[want]; !ok {
			t.Errorf("indexState did not collect ARN %q", want)
		}
	}
	if _, ok := idx.byType["rds:db"]["smoke1bc2-bi-reader"]; !ok {
		t.Error("rds:db reader not indexed under its own type")
	}
	if _, ok := idx.byType["rds:cluster"]["smoke1bc2-bi"]; !ok {
		t.Error("rds:cluster not indexed under its own type")
	}
	// The cluster's bare id must NOT leak into another service's bucket - that leak is
	// exactly what hid the EKS orphan.
	if _, ok := idx.byType["eks:cluster"]["smoke1bc2-bi"]; ok {
		t.Error("the RDS cluster id leaked into the eks:cluster bucket")
	}
}

// An empty state is a real answer (terraform owns nothing), not a parse error.
func TestIndexStateHandlesEmptyState(t *testing.T) {
	idx := newStateIndex()
	if err := indexState([]byte(`{"format_version":"1.0"}`), idx); err != nil {
		t.Fatalf("empty state should parse, got %v", err)
	}
	if len(idx.arns) != 0 || len(idx.byType) != 0 {
		t.Errorf("empty state yielded a non-empty index: %+v", idx)
	}
}

// The regression that matters most. CloudPrem names the Aurora cluster and the EKS
// cluster identically (both are local.identifier), so a bare-id match that ignores the
// AWS service declared the out-of-state EKS cluster "accounted for" by the in-state RDS
// cluster - and the check missed the exact orphan it was written to catch.
func TestReconcileTaggedDoesNotLetOneServiceAccountForAnother(t *testing.T) {
	idx := indexFixture(t)
	got := reconcileTagged([]string{"arn:aws:eks:us-east-1:076248559428:cluster/smoke1bc2-bi"}, idx)
	if len(got) != 1 {
		t.Fatalf("the out-of-state EKS cluster was accounted for by the same-named RDS cluster: %+v", got)
	}
	if got[0].Type != "eks:cluster" {
		t.Errorf("residual type = %q, want eks:cluster", got[0].Type)
	}
}

// The core of Lodestar-1xm.36.1: the out-of-state Aurora writer must surface, and
// everything terraform does track must not.
func TestReconcileTaggedFindsTheSmoke1bc2Orphans(t *testing.T) {
	idx := indexFixture(t)
	live := []string{
		"arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-writer",                     // orphan
		"arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-reader",                     // tracked (by arn)
		"arn:aws:ec2:us-east-1:076248559428:security-group/sg-04f8b0552e36bda84",        // tracked (by bare id)
		"arn:aws:rds:us-east-1:076248559428:pg:smoke1bc2-bi-8f7895f13c1ac08cde23864b70", // tracked
		"arn:aws:rds:us-east-1:076248559428:cluster:smoke1bc2-bi",                       // tracked
	}
	got := reconcileTagged(live, idx)
	if len(got) != 1 {
		t.Fatalf("reconcileTagged found %d residuals, want 1:\n%+v", len(got), got)
	}
	if got[0].ARN != "arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-writer" || got[0].Type != "rds:db" {
		t.Errorf("residual = %+v, want the rds:db writer orphan", got[0])
	}
	if !got[0].Blocking {
		t.Errorf("the writer orphan must be blocking, got why=%q", got[0].Why)
	}
	if got[0].Source != "tag-reconcile" {
		t.Errorf("residual source = %q, want tag-reconcile", got[0].Source)
	}
}

// The provider stores a pod identity association's own ARN as association_arn and its id
// as the bare association id, while the live ARN's resource part is
// "<cluster>/<association-id>". CloudPrem creates six of them, so getting this wrong
// meant six residuals on every single provision.
func TestReconcileTaggedMatchesPodIdentityAssociationARN(t *testing.T) {
	idx := indexFixture(t)
	live := "arn:aws:eks:us-east-1:076248559428:podidentityassociation/smoke1bc2-bi/a-9lk2m"
	if got := reconcileTagged([]string{live}, idx); len(got) != 0 {
		t.Errorf("state-owned pod identity association reported as a residual: %+v", got)
	}
}

// A security group is matched through its bare id, not its ARN.
func TestReconcileTaggedMatchesOnBareIDNotJustARN(t *testing.T) {
	idx := newStateIndex()
	idx.add("ec2:vpc", "vpc-006f0b30e5c537ad7")
	if got := reconcileTagged([]string{"arn:aws:ec2:us-east-1:076248559428:vpc/vpc-006f0b30e5c537ad7"}, idx); len(got) != 0 {
		t.Errorf("vpc tracked by bare id still reported as a residual: %+v", got)
	}
}

// Three classes of hit are reported but must never fail a run. Enforcing on any of them
// turns a healthy nightly red: automated RDS snapshots inherit the run's tags because
// every cluster here sets copy_tags_to_snapshot, the tagging index keeps returning
// deleted security groups, and a type CloudPrem's terraform never creates says nothing
// about terraform having lost anything.
func TestClassifyResidualDoesNotEnforceOnUnownedTypes(t *testing.T) {
	idx := indexFixture(t)
	cases := []struct {
		name, arn, wantWhy string
	}{
		{"automated rds snapshot", "arn:aws:rds:us-east-1:1:cluster-snapshot:rds:smoke1bc2-bi-2026-08-18", "service-created"},
		{"stale tagging-index security group", "arn:aws:ec2:us-east-1:1:security-group/sg-deadbeef", "tagging index"},
		{"type cloudprem terraform never creates", "arn:aws:ecr:us-east-1:1:repository/smoke1bc2-app", "unclassified"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := reconcileTagged([]string{c.arn}, idx)
			if len(got) != 1 {
				t.Fatalf("expected the hit to be REPORTED, got %+v", got)
			}
			if got[0].Blocking {
				t.Errorf("%s must not fail the run", c.name)
			}
			if !strings.Contains(got[0].Why, c.wantWhy) {
				t.Errorf("why = %q, want it to mention %q", got[0].Why, c.wantWhy)
			}
		})
	}
}

func TestStateRemnantsNamesTypeAndAddress(t *testing.T) {
	got := stateRemnants([]string{
		"module.vpc[0].aws_subnet.private[0]",
		"  ",
		"module.eks_cluster.aws_security_group.cluster[0]",
	})
	if len(got) != 2 {
		t.Fatalf("stateRemnants returned %d, want 2 (blank lines dropped): %+v", len(got), got)
	}
	if got[0].Type != "aws_security_group" || got[0].Source != "state-remnant" {
		t.Errorf("got[0] = %+v, want aws_security_group / state-remnant", got[0])
	}
	if got[1].Type != "aws_subnet" {
		t.Errorf("got[1].Type = %q, want aws_subnet", got[1].Type)
	}
}

// V1's real destroy output. The classes are what tell a human "something out of state
// is in the way" apart from "retry will clear it".
func TestDestroyErrorClassExtractsTheAWSCodes(t *testing.T) {
	err := errors.New(`destroy: exit status 1: Error: deleting RDS Cluster (smoke1bc2-bi): ` +
		`operation error RDS: DeleteDBCluster, https response error StatusCode: 400, ` +
		`api error InvalidDBClusterStateFault: Cluster can't be deleted. It still contains DB instances in a non-deleting state. ` +
		`Error: deleting EC2 Subnet (subnet-03f0a1057d2f68b7a): api error DependencyViolation: The subnet has dependencies`)
	got := destroyErrorClass(err)
	if got != "DependencyViolation,InvalidDBClusterStateFault" {
		t.Errorf("destroyErrorClass = %q, want %q", got, "DependencyViolation,InvalidDBClusterStateFault")
	}
	if destroyErrorClass(nil) != "" {
		t.Error("nil error should yield no class")
	}
	// No recognizable code: fall back to the message rather than to silence.
	if got := destroyErrorClass(errors.New("context deadline exceeded")); got == "" {
		t.Error("an unrecognized error must still carry its text, not vanish")
	}
}

// An incomplete report is never clean: it did not look, so it cannot say the stack is
// empty. This is the property that stops a missing tagging client from reading as a
// successful teardown.
func TestReportCleanRequiresACompleteCheck(t *testing.T) {
	if (&ResidualReport{}).Clean() != true {
		t.Error("an empty, complete report should be clean")
	}
	if (&ResidualReport{Incomplete: []string{"tag query: AccessDenied"}}).Clean() {
		t.Error("a report that could not run a signal must NOT be clean")
	}
	if (&ResidualReport{Residuals: []Residual{{ARN: "x"}}}).Clean() {
		t.Error("a report with residuals must not be clean")
	}
	var nilReport *ResidualReport
	if nilReport.Clean() {
		t.Error("no report at all is not clean")
	}
}

func TestSummaryNamesEveryResidual(t *testing.T) {
	rep := &ResidualReport{Phase: "teardown", Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:eks:us-east-1:1:cluster/smoke1bc2-bi", Type: "eks:cluster", Blocking: true},
		{Source: "state-remnant", Address: "module.vpc[0].aws_subnet.private[0]", Type: "aws_subnet", Blocking: true},
	}}
	s := rep.Summary()
	for _, want := range []string{"2 residual(s) after teardown", "eks:cluster", "smoke1bc2-bi", "module.vpc[0].aws_subnet.private[0]"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary does not name %q:\n%s", want, s)
		}
	}
}

// residualTagAPI serves one page of ARNs. Separate from janitor_test.go's fakeTagAPI so
// this file's fixtures cannot be changed out from under the janitor's suite.
type residualTagAPI struct{ arns []string }

func (f *residualTagAPI) GetResources(_ context.Context, _ *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	out := &resourcegroupstaggingapi.GetResourcesOutput{}
	for _, a := range f.arns {
		out.ResourceTagMappingList = append(out.ResourceTagMappingList, rgtypes.ResourceTagMapping{ResourceARN: aws.String(a)})
	}
	return out, nil
}

// checkResiduals must degrade, never fail. Without a recorded customer it cannot know
// which tags to query, and saying so is the only honest answer.
func TestCheckResidualsReportsIncompleteRatherThanGuessing(t *testing.T) {
	p := PhaseParams{Region: "us-east-1", Matrix: &Matrix{}, Tags: map[string]TagAPI{"us-east-1": &residualTagAPI{}}}
	// WorkingDir is a directory with no terragrunt in it, so both signals fail to run.
	rep := p.checkResiduals(context.Background(), TGOptions{WorkingDir: t.TempDir()}, "teardown", "", []string{"us-east-1"}, nil)
	if rep.Clean() {
		t.Fatal("a check with no customer and no state must not report clean")
	}
	if len(rep.Residuals) != 0 {
		t.Errorf("a check that could not enumerate must claim no residuals, got %+v", rep.Residuals)
	}
	joined := strings.Join(rep.Incomplete, "; ")
	if !strings.Contains(joined, "applied customer") {
		t.Errorf("incomplete reasons do not mention the missing customer: %q", joined)
	}
}

// The report has to survive the pod, so it must round-trip through the manifest JSON
// the same way every other field does.
func TestResidualReportRoundTripsThroughTheManifest(t *testing.T) {
	rm := &RunManifest{ConfigName: "bi_ha", Residuals: &ResidualReport{
		Phase: "teardown", CheckedAt: "2026-08-18T23:00:00Z", DestroyErr: "DependencyViolation",
		Residuals: []Residual{{Source: "tag-reconcile", ARN: "arn:aws:eks:us-east-1:1:cluster/smoke1bc2-bi", Type: "eks:cluster", Blocking: true}},
	}}
	b, err := json.Marshal(rm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RunManifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Residuals == nil || len(got.Residuals.Residuals) != 1 {
		t.Fatalf("residual report lost in round-trip: %+v", got.Residuals)
	}
	if got.Residuals.Residuals[0].ARN != "arn:aws:eks:us-east-1:1:cluster/smoke1bc2-bi" {
		t.Errorf("round-tripped ARN = %q", got.Residuals.Residuals[0].ARN)
	}
	if got.Residuals.DestroyErr != "DependencyViolation" {
		t.Errorf("round-tripped destroy error class = %q", got.Residuals.DestroyErr)
	}
	// A manifest with no report must not grow an empty object (omitempty), so older
	// readers and the golden manifest fixtures stay byte-identical.
	b2, _ := json.Marshal(&RunManifest{ConfigName: "bi_ha"})
	if strings.Contains(string(b2), "residuals") {
		t.Errorf("a manifest with no residual report should not serialize the key: %s", b2)
	}
}

// The post-teardown check has to be able to FAIL, which is the one thing the first
// version could not do. `terragrunt show -json` succeeds against an emptied state and
// returns a document with no values, so a successful destroy yields a readable-but-empty
// index and every surviving tagged resource is an orphan.
func TestReconcileAgainstAnEmptyButReadableStateFindsSurvivors(t *testing.T) {
	idx := newStateIndex()
	if err := indexState([]byte(`{"format_version":"1.0","values":{"root_module":{}}}`), idx); err != nil {
		t.Fatalf("an emptied state must parse: %v", err)
	}
	orphan := "arn:aws:eks:us-east-1:076248559428:cluster/smoke1bc2-bi"
	got := reconcileTagged([]string{orphan}, idx)
	if len(got) != 1 || !got[0].Blocking {
		t.Fatalf("a survivor of a successful destroy must block, got %+v", got)
	}
}

// The inverse, and the trap: a state read that FAILED is not an empty stack. Conflating
// them makes bad credentials or a missing worktree report every tagged resource as an
// orphan. checkResiduals must report incomplete and claim nothing, on either phase.
func TestUnreadableStateIsIncompleteNotEmpty(t *testing.T) {
	orphan := "arn:aws:eks:us-east-1:076248559428:cluster/smoke1bc2-bi"
	p := PhaseParams{
		Region: "us-east-1", Matrix: &Matrix{},
		Tags: map[string]TagAPI{"us-east-1": &residualTagAPI{arns: []string{orphan}}},
	}
	// No terragrunt tree here, so every ShowJSON errors: a failed read, not empty state.
	tg := TGOptions{WorkingDir: t.TempDir()}
	for _, phase := range []string{"provision", "teardown"} {
		rep := p.checkResiduals(context.Background(), tg, phase, "smoke1bc2", []string{"us-east-1"}, nil)
		if len(rep.Residuals) != 0 {
			t.Errorf("%s: an unreadable state claimed residuals: %+v", phase, rep.Residuals)
		}
		if len(rep.Incomplete) == 0 {
			t.Errorf("%s: an unreadable state must report the check incomplete", phase)
		}
		if rep.Clean() {
			t.Errorf("%s: an incomplete check is never clean", phase)
		}
	}
}

// serviceCreatedTypes and terraformManagedTypes must never overlap. An overlap is
// unresolvable from the AWS type alone - whichever branch classifyResidual reached first
// would silently decide the other case - and the direction that lost would be invisible.
// A real orphaned load balancer reported as "service-created" is exactly that silence.
func TestResidualTypeMapsAreDisjoint(t *testing.T) {
	if len(serviceCreatedTypes) == 0 || len(terraformManagedTypes) == 0 {
		t.Fatal("one of the maps is empty; this guard would pass vacuously")
	}
	for awsType := range serviceCreatedTypes {
		if terraformManagedTypes[awsType] {
			t.Errorf("%q is in BOTH serviceCreatedTypes and terraformManagedTypes; classifyResidual would silently exempt a terraform-owned orphan of this type", awsType)
		}
	}
	// The same guard for the lag list, with a deliberate allowlist. Both entries are
	// terraform-managed AND known to linger in the tagging index after deletion
	// (janitor.go:971-1014 measured it: 5 of 23 tagged security groups were already
	// InvalidGroup.NotFound, and pod identity associations outlive their clusters in the
	// index). Neither can hold a stack up on its own - AWS refuses to delete a VPC while
	// a non-default security group survives, so a real one can never outlive its VPC -
	// which is why exempting them costs nothing and enforcing on them would turn index
	// lag into failed teardowns. Anything else landing here is an oversight.
	lagExempt := map[string]bool{"ec2:security-group": true, "eks:podidentityassociation": true}
	for awsType := range insufficientAloneTypes {
		if terraformManagedTypes[awsType] && !lagExempt[awsType] {
			t.Errorf("%q is terraform-managed but listed as a tagging-index-lag type; add it to this test's allowlist with a reason or stop exempting it", awsType)
		}
	}
}

// Blocking() is what the phases enforce on, so an informational-only report must not
// fail anything while still being visible.
func TestBlockingSeparatesEnforcementFromReporting(t *testing.T) {
	rep := &ResidualReport{Residuals: []Residual{
		{ARN: "a", Type: "rds:snapshot", Blocking: false, Why: "service-created type"},
		{ARN: "b", Type: "eks:cluster", Blocking: true},
	}}
	if got := rep.Blocking(); len(got) != 1 || got[0].ARN != "b" {
		t.Errorf("Blocking() = %+v, want only the eks:cluster hit", got)
	}
	if rep.Clean() {
		t.Error("a report with any residual is not clean, blocking or not")
	}
	if (&ResidualReport{Residuals: []Residual{{ARN: "a", Blocking: false}}}).Blocking() != nil {
		t.Error("an informational-only report must have nothing to enforce on")
	}
}

// Class A: the Resource Groups Tagging API returns a live Aurora cluster under a SECOND
// ARN whose tail is the lowercased DbClusterResourceId, not the cluster identifier every
// other signal (state, describe, terraform's own id) uses. Both forms of the same
// cluster must reconcile clean, and the fix must not have widened case-insensitivity to
// any OTHER type.
func TestIndexStateAccountsForAuroraDualARN(t *testing.T) {
	showJSON := `{
	  "format_version": "1.0",
	  "values": {
	    "root_module": {
	      "resources": [
	        {"address": "module.aurora[0].aws_rds_cluster.this[0]", "type": "aws_rds_cluster", "values": {
	          "id": "custx-bi", "cluster_identifier": "custx-bi",
	          "arn": "arn:aws:rds:us-east-1:1:cluster:custx-bi",
	          "cluster_resource_id": "cluster-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	        }},
	        {"address": "module.eks_cluster.aws_security_group.cluster[0]", "type": "aws_security_group", "values": {"id": "sg-ABCDEF"}}
	      ]
	    }
	  }
	}`
	idx := newStateIndex()
	if err := indexState([]byte(showJSON), idx); err != nil {
		t.Fatalf("indexState: %v", err)
	}
	// The tagging API's secondary ARN for the SAME cluster: same account/region, but
	// the resource-id tail, lowercased. This is AWS's own observed behavior, not a
	// test artifact.
	dualARN := "arn:aws:rds:us-east-1:1:cluster:cluster-abcdefghijklmnopqrstuvwxyz"
	if got := reconcileTagged([]string{dualARN}, idx); len(got) != 0 {
		t.Fatalf("the dual (resource-id-form) ARN for an in-state Aurora cluster reported as a residual: %+v", got)
	}
	// The primary identifier-form ARN must still match - Class A must not have broken
	// the pre-existing, more common path.
	if got := reconcileTagged([]string{"arn:aws:rds:us-east-1:1:cluster:custx-bi"}, idx); len(got) != 0 {
		t.Fatalf("the primary identifier-form ARN regressed: %+v", got)
	}
	// The case-insensitive retry is scoped to rds:cluster ONLY: a security group id
	// that differs only by case must NOT match, or a real drift elsewhere in the index
	// would silently start passing.
	if got := reconcileTagged([]string{"arn:aws:ec2:us-east-1:1:security-group/sg-abcdef"}, idx); len(got) != 1 {
		t.Fatalf("case-insensitivity leaked into ec2:security-group, which must stay case-sensitive: %+v", got)
	}
}

// Class B: all five aws_dms_* types that have no plain `arn` attribute, plus the
// aws_dms_replication_config gap named in the brief (used in terraform/physical/bi.tf,
// previously absent from tfTypeToAWSType entirely).
func TestReconcileTaggedMatchesDMSShapes(t *testing.T) {
	showJSON := `{
	  "format_version": "1.0",
	  "values": {
	    "root_module": {
	      "resources": [
	        {"address": "aws_dms_replication_instance.this", "type": "aws_dms_replication_instance", "values": {"id": "custx-bi-repl", "replication_instance_arn": "arn:aws:dms:us-east-1:1:rep:ABCDEF123"}},
	        {"address": "aws_dms_endpoint.source", "type": "aws_dms_endpoint", "values": {"id": "custx-bi-src", "endpoint_arn": "arn:aws:dms:us-east-1:1:endpoint:GHIJKL456"}},
	        {"address": "aws_dms_replication_task.this", "type": "aws_dms_replication_task", "values": {"id": "custx-bi-task", "replication_task_arn": "arn:aws:dms:us-east-1:1:task:MNOPQR789"}},
	        {"address": "aws_dms_replication_subnet_group.this", "type": "aws_dms_replication_subnet_group", "values": {"id": "custx-bi-subgrp", "replication_subnet_group_arn": "arn:aws:dms:us-east-1:1:subgrp:custx-bi-subgrp"}},
	        {"address": "aws_dms_certificate.this", "type": "aws_dms_certificate", "values": {"id": "custx-bi-cert", "certificate_arn": "arn:aws:dms:us-east-1:1:cert:STUVWX012"}},
	        {"address": "aws_dms_replication_config.this", "type": "aws_dms_replication_config", "values": {"id": "custx-bi-cfg", "arn": "arn:aws:dms:us-east-1:1:replication-config:YZABCD345"}}
	      ]
	    }
	  }
	}`
	idx := newStateIndex()
	if err := indexState([]byte(showJSON), idx); err != nil {
		t.Fatalf("indexState: %v", err)
	}
	live := []string{
		"arn:aws:dms:us-east-1:1:rep:ABCDEF123",
		"arn:aws:dms:us-east-1:1:endpoint:GHIJKL456",
		"arn:aws:dms:us-east-1:1:task:MNOPQR789",
		"arn:aws:dms:us-east-1:1:subgrp:custx-bi-subgrp",
		"arn:aws:dms:us-east-1:1:cert:STUVWX012",
		"arn:aws:dms:us-east-1:1:replication-config:YZABCD345",
	}
	if got := reconcileTagged(live, idx); len(got) != 0 {
		t.Fatalf("terraform-owned DMS resources reported as residuals: %+v", got)
	}
	// An orphaned replication-config must classify Blocking, not unclassified - this is
	// the missing-tfTypeToAWSType-entry gap the brief names explicitly.
	orphan := "arn:aws:dms:us-east-1:1:replication-config:ORPHAN999"
	got := reconcileTagged([]string{orphan}, idx)
	if len(got) != 1 || !got[0].Blocking || got[0].Type != "dms:replication-config" {
		t.Fatalf("an orphaned dms replication-config did not classify as a blocking dms:replication-config residual: %+v", got)
	}
}

// Class B completeness guard: a terraform type with neither a plain provider `arn`
// attribute nor a primaryARNFields override is invisible to indexState's ARN harvest -
// a live orphan of that type would misreport as unclassified rather than blocking. This
// is what would have caught the DMS gap before it shipped.
//
// typesWithPlainARNAttribute is a reviewed table, not a live schema query: regenerate it
// by running `terraform providers schema -json` against the aws provider version pinned
// in terraform/physical (verified here against 6.61.0) and checking
// `.provider_schemas["registry.terraform.io/hashicorp/aws"].resource_schemas["<type>"].block.attributes.arn`
// for each type in tfTypeToAWSType.
var typesWithPlainARNAttribute = map[string]bool{
	"aws_eks_cluster":                   true,
	"aws_eks_node_group":                true,
	"aws_eks_addon":                     true,
	"aws_rds_cluster":                   true, // also has a primaryARNFields override (Class A)
	"aws_rds_cluster_instance":          true,
	"aws_db_instance":                   true,
	"aws_db_parameter_group":            true,
	"aws_rds_cluster_parameter_group":   true,
	"aws_db_subnet_group":               true,
	"aws_rds_global_cluster":            true,
	"aws_vpc":                           true,
	"aws_subnet":                        true,
	"aws_security_group":                true,
	"aws_route_table":                   true,
	"aws_network_acl":                   true,
	"aws_internet_gateway":              true,
	"aws_nat_gateway":                   true,
	"aws_eip":                           true,
	"aws_vpc_endpoint":                  true,
	"aws_ebs_volume":                    true,
	"aws_kms_key":                       true,
	"aws_kms_replica_key":               true,
	"aws_kms_external_key":              true,
	"aws_s3_bucket":                     true,
	"aws_iam_role":                      true,
	"aws_iam_policy":                    true,
	"aws_cloudwatch_log_group":          true,
	"aws_lb":                            true,
	"aws_lb_target_group":               true,
	"aws_msk_cluster":                   true, // also has a primaryARNFields override
	"aws_secretsmanager_secret":         true,
	"aws_elasticache_replication_group": true,
	"aws_efs_file_system":               true,
	"aws_opensearch_domain":             true,
	"aws_sqs_queue":                     true,
	"aws_sns_topic":                     true,
	"aws_dms_replication_config":        true, // the one DMS type WITH a plain arn
	// aws_eks_pod_identity_association and the five other aws_dms_* types have NO
	// plain arn attribute - every one of them is covered by a primaryARNFields entry
	// instead, checked below.
}

func TestEveryManagedTypeExposesAnARN(t *testing.T) {
	for tfType := range tfTypeToAWSType {
		_, hasOverride := primaryARNFields[tfType]
		if !typesWithPlainARNAttribute[tfType] && !hasOverride {
			t.Errorf("%q has neither a plain `arn` attribute (add it to typesWithPlainARNAttribute after checking the provider schema) nor a primaryARNFields override - indexState cannot see this type's ARN at all, and a live orphan of it will misreport as unclassified instead of blocking. Regenerate the schema table with: terraform providers schema -json", tfType)
		}
	}
}

// Class D (the silent-continue defect): a `terragrunt show -json` failure for ONE
// module out of two must not produce a HALF index that reports every resource the
// unread module alone would have accounted for as an orphan. checkResiduals must demote
// every tag-reconcile finding to non-blocking and name the failure in Incomplete, never
// silently `continue`.
//
// Exercised end-to-end through checkResiduals (not just indexState) by putting a fake
// `terragrunt` on PATH: one module directory succeeds with an empty-but-readable state,
// the other exits non-zero. This is the same shape ShowJSON actually sees in
// production (a real subprocess, not a mock), without needing a real terragrunt tree.
func TestCheckResidualsPartialIndexDemotesTagReconcileFindings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake terragrunt below is a #!/bin/sh script")
	}
	workDir := t.TempDir()
	for _, mod := range residualModules {
		if err := os.MkdirAll(filepath.Join(workDir, mod), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", mod, err)
		}
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		`case "$PWD" in
  */physical) echo '{"format_version":"1.0","values":{"root_module":{}}}' ; exit 0 ;;
  */logical) exit 1 ;;
  *) exit 1 ;;
esac
`
	fakeTG := filepath.Join(binDir, "terragrunt")
	if err := os.WriteFile(fakeTG, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake terragrunt: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	orphan := "arn:aws:eks:us-east-1:1:cluster/custx-bi"
	p := PhaseParams{
		Region: "us-east-1", Matrix: &Matrix{},
		Tags: map[string]TagAPI{"us-east-1": &residualTagAPI{arns: []string{orphan}}},
	}
	tg := TGOptions{WorkingDir: workDir}
	rep := p.checkResiduals(context.Background(), tg, "teardown", "custx", []string{"us-east-1"}, nil)

	foundIncompletePhysicalLogical := false
	for _, inc := range rep.Incomplete {
		if strings.Contains(inc, "logical") {
			foundIncompletePhysicalLogical = true
		}
	}
	if !foundIncompletePhysicalLogical {
		t.Errorf("the failed logical module's show -json error was not recorded in Incomplete: %+v", rep.Incomplete)
	}
	if len(rep.Blocking()) != 0 {
		t.Fatalf("a partial index (one of two modules unread) must never let a tag-reconcile finding stay Blocking: %+v", rep.Residuals)
	}
	found := false
	for _, res := range rep.Residuals {
		if res.ARN == orphan {
			found = true
			if !strings.Contains(res.Why, "partial") {
				t.Errorf("demoted finding's Why does not explain the partial index: %q", res.Why)
			}
		}
	}
	if !found {
		t.Fatalf("the orphan must still be REPORTED (non-blocking), not dropped: %+v", rep.Residuals)
	}
}

// The wiring gap Lodestar-p52f flagged: every existence-probe test in existence_test.go
// calls verifyResidualExistenceWith directly with a fake factory, so none of them
// exercise the actual production seam - PhaseParams.verifyResidualExistence reading the
// package var newVerifierClients - and none of them go through checkResiduals, which is
// what actually calls verifyResidualExistence (residuals.go). Deleting that one call
// site would leave every existing test green: the ones that construct a report by hand
// never notice the probe never ran, and this file's other checkResiduals tests never
// produce a residual that would need probing (the partial-index test's orphan is
// demoted by the partial-index path itself, before existence probing would matter).
//
// This test closes that gap by overriding newVerifierClients (not verifyResidualExistenceWith)
// and driving the whole thing through checkResiduals with the same fake-terragrunt-on-PATH
// technique TestCheckResidualsPartialIndexDemotesTagReconcileFindings uses: a tag-reconcile
// orphan that state genuinely does not have, so classifyResidual marks it Blocking, and the
// only thing that can demote it is a real existence probe wired all the way through. If the
// checkResiduals -> verifyResidualExistence call is ever deleted, factoryCalled stays false
// and the residual stays Blocking, and this test fails on both counts.
func TestCheckResidualsCallsTheRealExistenceProbeSeam(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake terragrunt below is a #!/bin/sh script")
	}
	workDir := t.TempDir()
	for _, mod := range residualModules {
		if err := os.MkdirAll(filepath.Join(workDir, mod), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", mod, err)
		}
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\necho '{\"format_version\":\"1.0\",\"values\":{\"root_module\":{}}}'\nexit 0\n"
	fakeTG := filepath.Join(binDir, "terragrunt")
	if err := os.WriteFile(fakeTG, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake terragrunt: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	orphan := "arn:aws:rds:us-east-1:1:db:custx-bi-writer"
	factoryCalled := false
	oldFactory := newVerifierClients
	t.Cleanup(func() { newVerifierClients = oldFactory })
	newVerifierClients = func(_ context.Context, _ string) regionClientFactory {
		return func(string) (*verifierAPISet, error) {
			factoryCalled = true
			return (&fakeVerifierAPI{describeDBInstances: func(*rds.DescribeDBInstancesInput) (*rds.DescribeDBInstancesOutput, error) {
				return nil, &rdstypes.DBInstanceNotFoundFault{Message: aws.String("gone")}
			}}).apiSet(), nil
		}
	}

	p := PhaseParams{
		Region: "us-east-1", Matrix: &Matrix{},
		Tags: map[string]TagAPI{"us-east-1": &residualTagAPI{arns: []string{orphan}}},
	}
	tg := TGOptions{WorkingDir: workDir}
	rep := p.checkResiduals(context.Background(), tg, "teardown", "custx", []string{"us-east-1"}, nil)

	if !factoryCalled {
		t.Fatal("newVerifierClients was never invoked - checkResiduals is not wired to the existence probe (this is exactly the seam Lodestar-p52f item 6 flagged)")
	}
	if len(rep.Blocking()) != 0 {
		t.Fatalf("the probe confirmed the RDS instance is gone; it must not still be Blocking: %+v", rep.Residuals)
	}
	found := false
	for _, res := range rep.Residuals {
		if res.ARN == orphan {
			found = true
			if res.Blocking {
				t.Errorf("orphan residual still Blocking after a confirmed-gone probe result: %+v", res)
			}
		}
	}
	if !found {
		t.Fatalf("the orphan must still be REPORTED (demoted, not dropped): %+v", rep.Residuals)
	}
}
