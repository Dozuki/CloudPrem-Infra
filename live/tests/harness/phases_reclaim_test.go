package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// ---- fakes ----

// fakeEC2Reclaim answers DescribeVolumes/DescribeLaunchTemplates by looking up the
// EXACT filter value the call carried (the identifier-scoped "<id>-dynamic-pvc-*" /
// "<id>-*" pattern production code sends), so a test proves one candidate's reclaim can
// never see another candidate's resources - a filter-blind fake would hide that bug.
type fakeEC2Reclaim struct {
	volumesByFilter         map[string][]ec2types.Volume
	launchTemplatesByFilter map[string][]ec2types.LaunchTemplate

	deletedVolumes   []string
	deletedTemplates []string
}

func (f *fakeEC2Reclaim) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	var key string
	for _, flt := range in.Filters {
		if aws.ToString(flt.Name) == "tag:Name" && len(flt.Values) > 0 {
			key = flt.Values[0]
		}
	}
	return &ec2.DescribeVolumesOutput{Volumes: f.volumesByFilter[key]}, nil
}

func (f *fakeEC2Reclaim) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	f.deletedVolumes = append(f.deletedVolumes, aws.ToString(in.VolumeId))
	return &ec2.DeleteVolumeOutput{}, nil
}

func (f *fakeEC2Reclaim) DescribeLaunchTemplates(_ context.Context, in *ec2.DescribeLaunchTemplatesInput, _ ...func(*ec2.Options)) (*ec2.DescribeLaunchTemplatesOutput, error) {
	var key string
	for _, flt := range in.Filters {
		if aws.ToString(flt.Name) == "launch-template-name" && len(flt.Values) > 0 {
			key = flt.Values[0]
		}
	}
	return &ec2.DescribeLaunchTemplatesOutput{LaunchTemplates: f.launchTemplatesByFilter[key]}, nil
}

func (f *fakeEC2Reclaim) DeleteLaunchTemplate(_ context.Context, in *ec2.DeleteLaunchTemplateInput, _ ...func(*ec2.Options)) (*ec2.DeleteLaunchTemplateOutput, error) {
	f.deletedTemplates = append(f.deletedTemplates, aws.ToString(in.LaunchTemplateId))
	return &ec2.DeleteLaunchTemplateOutput{}, nil
}

// fakeLogsReclaim answers DescribeLogGroups by exact prefix, same reasoning as
// fakeEC2Reclaim above.
type fakeLogsReclaim struct {
	groupsByPrefix map[string][]string
	deleted        []string
}

func (f *fakeLogsReclaim) DescribeLogGroups(_ context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	prefix := aws.ToString(in.LogGroupNamePrefix)
	var out []cwtypes.LogGroup
	for _, name := range f.groupsByPrefix[prefix] {
		n := name
		out = append(out, cwtypes.LogGroup{LogGroupName: &n})
	}
	return &cloudwatchlogs.DescribeLogGroupsOutput{LogGroups: out}, nil
}

func (f *fakeLogsReclaim) DeleteLogGroup(_ context.Context, in *cloudwatchlogs.DeleteLogGroupInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.LogGroupName))
	return &cloudwatchlogs.DeleteLogGroupOutput{}, nil
}

// fakeIAMReclaim models one role (or none, if roleExists is false) and records call
// order so a test can prove detach/delete-policy happen strictly before DeleteRole -
// IAM itself enforces that ordering, and this is the regression test for it.
type fakeIAMReclaim struct {
	roleExists bool
	attached   []iamtypes.AttachedPolicy
	inline     []string
	getRoleErr error // non-NoSuchEntity error, to test the "other sub-steps still run" guarantee
	calls      []string
}

func (f *fakeIAMReclaim) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	f.calls = append(f.calls, "GetRole:"+aws.ToString(in.RoleName))
	if f.getRoleErr != nil {
		return nil, f.getRoleErr
	}
	if !f.roleExists {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	return &iam.GetRoleOutput{}, nil
}

func (f *fakeIAMReclaim) ListAttachedRolePolicies(_ context.Context, in *iam.ListAttachedRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListAttachedRolePoliciesOutput, error) {
	f.calls = append(f.calls, "ListAttachedRolePolicies")
	return &iam.ListAttachedRolePoliciesOutput{AttachedPolicies: f.attached}, nil
}

func (f *fakeIAMReclaim) DetachRolePolicy(_ context.Context, in *iam.DetachRolePolicyInput, _ ...func(*iam.Options)) (*iam.DetachRolePolicyOutput, error) {
	f.calls = append(f.calls, "DetachRolePolicy:"+aws.ToString(in.PolicyArn))
	return &iam.DetachRolePolicyOutput{}, nil
}

func (f *fakeIAMReclaim) ListRolePolicies(_ context.Context, in *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	f.calls = append(f.calls, "ListRolePolicies")
	return &iam.ListRolePoliciesOutput{PolicyNames: f.inline}, nil
}

func (f *fakeIAMReclaim) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	f.calls = append(f.calls, "DeleteRolePolicy:"+aws.ToString(in.PolicyName))
	return &iam.DeleteRolePolicyOutput{}, nil
}

func (f *fakeIAMReclaim) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	f.calls = append(f.calls, "DeleteRole:"+aws.ToString(in.RoleName))
	return &iam.DeleteRoleOutput{}, nil
}

// ---- tests ----

func TestReclaimOutOfStateResourcesEmptyIdentifierIsNoOp(t *testing.T) {
	ec2f := &fakeEC2Reclaim{}
	logsf := &fakeLogsReclaim{}
	iamf := &fakeIAMReclaim{roleExists: true}
	d := reclaimDeps{EC2: ec2f, Logs: logsf, IAM: iamf}

	if err := reclaimOutOfStateResources(context.Background(), d, ""); err != nil {
		t.Fatalf("reclaimOutOfStateResources(\"\") = %v, want nil", err)
	}
	if len(ec2f.deletedVolumes) != 0 || len(ec2f.deletedTemplates) != 0 || len(logsf.deleted) != 0 || len(iamf.calls) != 0 {
		t.Fatalf("empty identifier issued calls: ec2=%v/%v logs=%v iam=%v", ec2f.deletedVolumes, ec2f.deletedTemplates, logsf.deleted, iamf.calls)
	}
}

// TestReclaimOutOfStateResourcesScopedToIdentifier is the candidate-isolation
// guarantee: two stacks' volumes, launch templates, and log groups are seeded side by
// side, and reclaiming candidate A must never touch anything keyed to candidate B.
func TestReclaimOutOfStateResourcesScopedToIdentifier(t *testing.T) {
	ec2f := &fakeEC2Reclaim{
		volumesByFilter: map[string][]ec2types.Volume{
			"smokeaa-min-dynamic-pvc-*": {{VolumeId: aws.String("vol-aaa")}},
			"smokebb-min-dynamic-pvc-*": {{VolumeId: aws.String("vol-bbb")}},
		},
		launchTemplatesByFilter: map[string][]ec2types.LaunchTemplate{
			"smokeaa-min-*": {{LaunchTemplateId: aws.String("lt-aaa")}},
			"smokebb-min-*": {{LaunchTemplateId: aws.String("lt-bbb")}},
		},
	}
	logsf := &fakeLogsReclaim{
		groupsByPrefix: map[string][]string{
			"/aws/lambda/smokeaa-min-": {"/aws/lambda/smokeaa-min-sns_to_slack"},
			"/aws/lambda/smokebb-min-": {"/aws/lambda/smokebb-min-sns_to_slack"},
			"dms-tasks-smokeaa-min-":   {"dms-tasks-smokeaa-min-aurora-migration"},
			"dms-tasks-smokebb-min-":   {"dms-tasks-smokebb-min-aurora-migration"},
		},
	}
	iamf := &fakeIAMReclaim{roleExists: false} // absent: isolate this test to EC2/Logs

	d := reclaimDeps{EC2: ec2f, Logs: logsf, IAM: iamf}
	if err := reclaimOutOfStateResources(context.Background(), d, "smokeaa-min"); err != nil {
		t.Fatalf("reclaimOutOfStateResources: %v", err)
	}

	if len(ec2f.deletedVolumes) != 1 || ec2f.deletedVolumes[0] != "vol-aaa" {
		t.Fatalf("deletedVolumes = %v, want [vol-aaa] only - smokebb's volume must never be touched", ec2f.deletedVolumes)
	}
	if len(ec2f.deletedTemplates) != 1 || ec2f.deletedTemplates[0] != "lt-aaa" {
		t.Fatalf("deletedTemplates = %v, want [lt-aaa] only", ec2f.deletedTemplates)
	}
	if len(logsf.deleted) != 2 {
		t.Fatalf("deleted log groups = %v, want exactly the 2 smokeaa-min groups", logsf.deleted)
	}
	for _, name := range logsf.deleted {
		if !strings.Contains(name, "smokeaa-min") {
			t.Fatalf("deleted log group %q belongs to another stack", name)
		}
	}
}

func TestReclaimFluxRoleOrderingDetachAndDeletePolicyBeforeDeleteRole(t *testing.T) {
	iamf := &fakeIAMReclaim{
		roleExists: true,
		attached:   []iamtypes.AttachedPolicy{{PolicyArn: aws.String("arn:aws:iam::123:policy/p1")}},
		inline:     []string{"inline1"},
	}
	if err := reclaimFluxSourceControllerRole(context.Background(), iamf, "smokeaa-min"); err != nil {
		t.Fatalf("reclaimFluxSourceControllerRole: %v", err)
	}
	want := []string{
		"GetRole:smokeaa-min-flux-source-controller",
		"ListAttachedRolePolicies",
		"DetachRolePolicy:arn:aws:iam::123:policy/p1",
		"ListRolePolicies",
		"DeleteRolePolicy:inline1",
		"DeleteRole:smokeaa-min-flux-source-controller",
	}
	if len(iamf.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", iamf.calls, want)
	}
	for i, c := range want {
		if iamf.calls[i] != c {
			t.Fatalf("calls[%d] = %q, want %q (full: %v)", i, iamf.calls[i], c, iamf.calls)
		}
	}
	deleteRoleIdx, detachIdx, deletePolicyIdx := -1, -1, -1
	for i, c := range iamf.calls {
		switch {
		case strings.HasPrefix(c, "DeleteRole:"):
			deleteRoleIdx = i
		case strings.HasPrefix(c, "DetachRolePolicy:"):
			detachIdx = i
		case strings.HasPrefix(c, "DeleteRolePolicy:"):
			deletePolicyIdx = i
		}
	}
	if deleteRoleIdx < detachIdx || deleteRoleIdx < deletePolicyIdx {
		t.Fatalf("DeleteRole (idx %d) did not run strictly after detach (idx %d) and delete-policy (idx %d): %v", deleteRoleIdx, detachIdx, deletePolicyIdx, iamf.calls)
	}
}

func TestReclaimFluxRoleAbsentRoleIsNoOp(t *testing.T) {
	iamf := &fakeIAMReclaim{roleExists: false}
	if err := reclaimFluxSourceControllerRole(context.Background(), iamf, "smokeaa-min"); err != nil {
		t.Fatalf("reclaimFluxSourceControllerRole: %v", err)
	}
	if len(iamf.calls) != 1 || iamf.calls[0] != "GetRole:smokeaa-min-flux-source-controller" {
		t.Fatalf("calls = %v, want only a GetRole check", iamf.calls)
	}
}

// TestReclaimOutOfStateResourcesOneSubStepFailureDoesNotBlockOthers is the "one stuck
// IAM role must not abort the volume sweep" guarantee: IAM's GetRole fails with a real
// (non-NoSuchEntity) error, and the EC2/Logs sub-steps must still run to completion.
func TestReclaimOutOfStateResourcesOneSubStepFailureDoesNotBlockOthers(t *testing.T) {
	ec2f := &fakeEC2Reclaim{
		volumesByFilter: map[string][]ec2types.Volume{
			"smokeaa-min-dynamic-pvc-*": {{VolumeId: aws.String("vol-aaa")}},
		},
	}
	logsf := &fakeLogsReclaim{
		groupsByPrefix: map[string][]string{
			"/aws/lambda/smokeaa-min-": {"/aws/lambda/smokeaa-min-sns_to_slack"},
		},
	}
	iamf := &fakeIAMReclaim{getRoleErr: errors.New("access denied")}

	d := reclaimDeps{EC2: ec2f, Logs: logsf, IAM: iamf}
	err := reclaimOutOfStateResources(context.Background(), d, "smokeaa-min")
	if err == nil {
		t.Fatal("reclaimOutOfStateResources: want a non-nil error naming the IAM failure")
	}
	if !strings.Contains(err.Error(), "flux-source-controller") {
		t.Fatalf("error = %v, want it to name the flux-source-controller sub-step", err)
	}
	if len(ec2f.deletedVolumes) != 1 || ec2f.deletedVolumes[0] != "vol-aaa" {
		t.Fatalf("deletedVolumes = %v, want the volume sweep to still have run despite the IAM failure", ec2f.deletedVolumes)
	}
	if len(logsf.deleted) != 1 {
		t.Fatalf("deleted log groups = %v, want the log-group sweep to still have run despite the IAM failure", logsf.deleted)
	}
}
