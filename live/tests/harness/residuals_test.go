package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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
        {"address": "aws_db_parameter_group.default", "values": {"id": "smoke1bc2-bi-8f7895f13c1ac08cde23864b70", "arn": "arn:aws:rds:us-east-1:076248559428:pg:smoke1bc2-bi-8f7895f13c1ac08cde23864b70"}}
      ],
      "child_modules": [
        {
          "address": "module.aurora[0]",
          "resources": [
            {"address": "module.aurora[0].aws_rds_cluster_instance.this[\"reader\"]", "values": {"id": "smoke1bc2-bi-reader", "arn": "arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-reader"}}
          ]
        },
        {
          "address": "module.eks_cluster",
          "resources": [
            {"address": "module.eks_cluster.aws_security_group.cluster[0]", "values": {"id": "sg-04f8b0552e36bda84", "arn": "arn:aws:ec2:us-east-1:076248559428:security-group/sg-04f8b0552e36bda84"}}
          ],
          "child_modules": [
            {
              "address": "module.eks_cluster.module.kms",
              "resources": [
                {"address": "module.eks_cluster.module.kms.aws_kms_key.this[0]", "values": {"id": "9aa2aea3-2dc3-4560-a16f-3730a5e98190"}}
              ]
            }
          ]
        }
      ]
    }
  }
}`

func TestPhysicalIDsFromShowWalksNestedModules(t *testing.T) {
	ids, err := physicalIDsFromShow([]byte(smoke1bc2ShowJSON))
	if err != nil {
		t.Fatalf("physicalIDsFromShow: %v", err)
	}
	for _, want := range []string{
		"smoke1bc2-bi-reader",
		"arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-reader",
		"sg-04f8b0552e36bda84",
		"9aa2aea3-2dc3-4560-a16f-3730a5e98190", // two module levels deep
	} {
		if _, ok := ids[want]; !ok {
			t.Errorf("physicalIDsFromShow did not collect %q; a missed id makes a tracked resource look like a residual", want)
		}
	}
	if _, ok := ids["smoke1bc2-bi-writer"]; ok {
		t.Fatal("fixture is wrong: the writer must NOT be in state, that is the whole defect")
	}
}

// An empty state is a real answer (terraform owns nothing), not a parse error.
func TestPhysicalIDsFromShowHandlesEmptyState(t *testing.T) {
	ids, err := physicalIDsFromShow([]byte(`{"format_version":"1.0"}`))
	if err != nil {
		t.Fatalf("empty state should parse, got %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("empty state yielded %d ids, want 0", len(ids))
	}
}

// The core of Lodestar-1xm.36.1: the out-of-state Aurora writer and EKS cluster must
// surface, and everything terraform does track must not.
func TestReconcileTaggedFindsTheSmoke1bc2Orphans(t *testing.T) {
	ids, err := physicalIDsFromShow([]byte(smoke1bc2ShowJSON))
	if err != nil {
		t.Fatalf("physicalIDsFromShow: %v", err)
	}
	live := []string{
		"arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-writer",  // orphan
		"arn:aws:eks:us-east-1:076248559428:cluster/smoke1bc2-bi",    // orphan
		"arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-reader",  // tracked (by arn)
		"arn:aws:ec2:us-east-1:076248559428:security-group/sg-04f8b0552e36bda84", // tracked (by bare id)
		"arn:aws:rds:us-east-1:076248559428:pg:smoke1bc2-bi-8f7895f13c1ac08cde23864b70", // tracked
	}
	got := reconcileTagged(live, ids)
	if len(got) != 2 {
		t.Fatalf("reconcileTagged found %d residuals, want 2:\n%+v", len(got), got)
	}
	// Sorted, so this order is stable.
	if got[0].ARN != "arn:aws:eks:us-east-1:076248559428:cluster/smoke1bc2-bi" || got[0].Type != "eks:cluster" {
		t.Errorf("first residual = %+v, want the eks:cluster orphan", got[0])
	}
	if got[1].ARN != "arn:aws:rds:us-east-1:076248559428:db:smoke1bc2-bi-writer" || got[1].Type != "rds:db" {
		t.Errorf("second residual = %+v, want the rds:db writer orphan", got[1])
	}
	for _, r := range got {
		if r.Source != "tag-reconcile" {
			t.Errorf("residual %q has source %q, want tag-reconcile", r.ARN, r.Source)
		}
	}
}

// A security group is matched through its bare id, not its ARN. Getting this wrong is
// how an address-vs-ARN comparison reports every resource in the stack as a residual.
func TestReconcileTaggedMatchesOnBareIDNotJustARN(t *testing.T) {
	ids := map[string]struct{}{"vpc-006f0b30e5c537ad7": {}}
	if got := reconcileTagged([]string{"arn:aws:ec2:us-east-1:076248559428:vpc/vpc-006f0b30e5c537ad7"}, ids); len(got) != 0 {
		t.Errorf("vpc tracked by bare id still reported as a residual: %+v", got)
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
		{Source: "tag-reconcile", ARN: "arn:aws:eks:us-east-1:1:cluster/smoke1bc2-bi", Type: "eks:cluster"},
		{Source: "state-remnant", Address: "module.vpc[0].aws_subnet.private[0]", Type: "aws_subnet"},
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
		Residuals: []Residual{{Source: "tag-reconcile", ARN: "arn:aws:eks:us-east-1:1:cluster/smoke1bc2-bi", Type: "eks:cluster"}},
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
