package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
	dmstypes "github.com/aws/aws-sdk-go-v2/service/databasemigrationservice/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

// fakeVerifierAPI is one struct of function fields satisfying every per-service
// interface verifierAPISet composes (EKSExistenceAPI, RDSExistenceAPI, ...) at once -
// no network, no credentials. A nil field for a method a test never calls panics with a
// clear message rather than silently returning a zero value that could be misread as a
// real "found nothing" answer.
type fakeVerifierAPI struct {
	describeCluster   func(*eks.DescribeClusterInput) (*eks.DescribeClusterOutput, error)
	describeNodegroup func(*eks.DescribeNodegroupInput) (*eks.DescribeNodegroupOutput, error)
	describeAddon     func(*eks.DescribeAddonInput) (*eks.DescribeAddonOutput, error)

	describeDBClusters               func(*rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error)
	describeDBInstances              func(*rds.DescribeDBInstancesInput) (*rds.DescribeDBInstancesOutput, error)
	describeDBParameterGroups        func(*rds.DescribeDBParameterGroupsInput) (*rds.DescribeDBParameterGroupsOutput, error)
	describeDBClusterParameterGroups func(*rds.DescribeDBClusterParameterGroupsInput) (*rds.DescribeDBClusterParameterGroupsOutput, error)
	describeDBSubnetGroups           func(*rds.DescribeDBSubnetGroupsInput) (*rds.DescribeDBSubnetGroupsOutput, error)
	describeGlobalClusters           func(*rds.DescribeGlobalClustersInput) (*rds.DescribeGlobalClustersOutput, error)

	describeVpcs             func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error)
	describeSubnets          func(*ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
	describeRouteTables      func(*ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error)
	describeNetworkAcls      func(*ec2.DescribeNetworkAclsInput) (*ec2.DescribeNetworkAclsOutput, error)
	describeInternetGateways func(*ec2.DescribeInternetGatewaysInput) (*ec2.DescribeInternetGatewaysOutput, error)
	describeNatGateways      func(*ec2.DescribeNatGatewaysInput) (*ec2.DescribeNatGatewaysOutput, error)
	describeAddresses        func(*ec2.DescribeAddressesInput) (*ec2.DescribeAddressesOutput, error)
	describeVpcEndpoints     func(*ec2.DescribeVpcEndpointsInput) (*ec2.DescribeVpcEndpointsOutput, error)
	describeVolumes          func(*ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error)

	getRole   func(*iam.GetRoleInput) (*iam.GetRoleOutput, error)
	getPolicy func(*iam.GetPolicyInput) (*iam.GetPolicyOutput, error)

	headBucket func(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error)

	describeLogGroups func(*cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error)

	describeReplicationInstances    func(*databasemigrationservice.DescribeReplicationInstancesInput) (*databasemigrationservice.DescribeReplicationInstancesOutput, error)
	describeEndpoints               func(*databasemigrationservice.DescribeEndpointsInput) (*databasemigrationservice.DescribeEndpointsOutput, error)
	describeReplicationTasks        func(*databasemigrationservice.DescribeReplicationTasksInput) (*databasemigrationservice.DescribeReplicationTasksOutput, error)
	describeReplicationSubnetGroups func(*databasemigrationservice.DescribeReplicationSubnetGroupsInput) (*databasemigrationservice.DescribeReplicationSubnetGroupsOutput, error)
	describeCertificates            func(*databasemigrationservice.DescribeCertificatesInput) (*databasemigrationservice.DescribeCertificatesOutput, error)
	describeReplicationConfigs      func(*databasemigrationservice.DescribeReplicationConfigsInput) (*databasemigrationservice.DescribeReplicationConfigsOutput, error)

	describeSecret func(*secretsmanager.DescribeSecretInput) (*secretsmanager.DescribeSecretOutput, error)

	getQueueUrl func(*sqs.GetQueueUrlInput) (*sqs.GetQueueUrlOutput, error)
}

func (f *fakeVerifierAPI) DescribeCluster(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	return f.describeCluster(in)
}
func (f *fakeVerifierAPI) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	return f.describeNodegroup(in)
}
func (f *fakeVerifierAPI) DescribeAddon(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	return f.describeAddon(in)
}

func (f *fakeVerifierAPI) DescribeDBClusters(_ context.Context, in *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	return f.describeDBClusters(in)
}
func (f *fakeVerifierAPI) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	return f.describeDBInstances(in)
}
func (f *fakeVerifierAPI) DescribeDBParameterGroups(_ context.Context, in *rds.DescribeDBParameterGroupsInput, _ ...func(*rds.Options)) (*rds.DescribeDBParameterGroupsOutput, error) {
	return f.describeDBParameterGroups(in)
}
func (f *fakeVerifierAPI) DescribeDBClusterParameterGroups(_ context.Context, in *rds.DescribeDBClusterParameterGroupsInput, _ ...func(*rds.Options)) (*rds.DescribeDBClusterParameterGroupsOutput, error) {
	return f.describeDBClusterParameterGroups(in)
}
func (f *fakeVerifierAPI) DescribeDBSubnetGroups(_ context.Context, in *rds.DescribeDBSubnetGroupsInput, _ ...func(*rds.Options)) (*rds.DescribeDBSubnetGroupsOutput, error) {
	return f.describeDBSubnetGroups(in)
}
func (f *fakeVerifierAPI) DescribeGlobalClusters(_ context.Context, in *rds.DescribeGlobalClustersInput, _ ...func(*rds.Options)) (*rds.DescribeGlobalClustersOutput, error) {
	return f.describeGlobalClusters(in)
}

func (f *fakeVerifierAPI) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return f.describeVpcs(in)
}
func (f *fakeVerifierAPI) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return f.describeSubnets(in)
}
func (f *fakeVerifierAPI) DescribeRouteTables(_ context.Context, in *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	return f.describeRouteTables(in)
}
func (f *fakeVerifierAPI) DescribeNetworkAcls(_ context.Context, in *ec2.DescribeNetworkAclsInput, _ ...func(*ec2.Options)) (*ec2.DescribeNetworkAclsOutput, error) {
	return f.describeNetworkAcls(in)
}
func (f *fakeVerifierAPI) DescribeInternetGateways(_ context.Context, in *ec2.DescribeInternetGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error) {
	return f.describeInternetGateways(in)
}
func (f *fakeVerifierAPI) DescribeNatGateways(_ context.Context, in *ec2.DescribeNatGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	return f.describeNatGateways(in)
}
func (f *fakeVerifierAPI) DescribeAddresses(_ context.Context, in *ec2.DescribeAddressesInput, _ ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error) {
	return f.describeAddresses(in)
}
func (f *fakeVerifierAPI) DescribeVpcEndpoints(_ context.Context, in *ec2.DescribeVpcEndpointsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error) {
	return f.describeVpcEndpoints(in)
}
func (f *fakeVerifierAPI) DescribeVolumes(_ context.Context, in *ec2.DescribeVolumesInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	return f.describeVolumes(in)
}

func (f *fakeVerifierAPI) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	return f.getRole(in)
}
func (f *fakeVerifierAPI) GetPolicy(_ context.Context, in *iam.GetPolicyInput, _ ...func(*iam.Options)) (*iam.GetPolicyOutput, error) {
	return f.getPolicy(in)
}

func (f *fakeVerifierAPI) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return f.headBucket(in)
}

func (f *fakeVerifierAPI) DescribeLogGroups(_ context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	return f.describeLogGroups(in)
}

func (f *fakeVerifierAPI) DescribeReplicationInstances(_ context.Context, in *databasemigrationservice.DescribeReplicationInstancesInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationInstancesOutput, error) {
	return f.describeReplicationInstances(in)
}
func (f *fakeVerifierAPI) DescribeEndpoints(_ context.Context, in *databasemigrationservice.DescribeEndpointsInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeEndpointsOutput, error) {
	return f.describeEndpoints(in)
}
func (f *fakeVerifierAPI) DescribeReplicationTasks(_ context.Context, in *databasemigrationservice.DescribeReplicationTasksInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationTasksOutput, error) {
	return f.describeReplicationTasks(in)
}
func (f *fakeVerifierAPI) DescribeReplicationSubnetGroups(_ context.Context, in *databasemigrationservice.DescribeReplicationSubnetGroupsInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationSubnetGroupsOutput, error) {
	return f.describeReplicationSubnetGroups(in)
}
func (f *fakeVerifierAPI) DescribeCertificates(_ context.Context, in *databasemigrationservice.DescribeCertificatesInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeCertificatesOutput, error) {
	return f.describeCertificates(in)
}
func (f *fakeVerifierAPI) DescribeReplicationConfigs(_ context.Context, in *databasemigrationservice.DescribeReplicationConfigsInput, _ ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationConfigsOutput, error) {
	return f.describeReplicationConfigs(in)
}

func (f *fakeVerifierAPI) DescribeSecret(_ context.Context, in *secretsmanager.DescribeSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	return f.describeSecret(in)
}

func (f *fakeVerifierAPI) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return f.getQueueUrl(in)
}

func (f *fakeVerifierAPI) apiSet() *verifierAPISet {
	return &verifierAPISet{EKS: f, RDS: f, EC2: f, IAM: f, S3: f, Logs: f, DMS: f, SM: f, SQS: f}
}

// Completeness guard for Class C, mirroring TestEveryManagedTypeExposesAnARN for Class
// B: every AWS type classifyResidual can mark Blocking must have a registered verifier,
// or a real orphan of a newly-added type would reach production with nothing to
// confirm it - and nothing here would flag the gap.
func TestEveryBlockingTypeHasAnExistenceVerifier(t *testing.T) {
	if len(terraformManagedTypes) == 0 {
		t.Fatal("terraformManagedTypes is empty; this guard would pass vacuously")
	}
	for awsType := range terraformManagedTypes {
		if insufficientAloneTypes[awsType] {
			continue // classifyResidual never marks these Blocking - see its ordering comment
		}
		if _, ok := existenceVerifiers[awsType]; !ok {
			t.Errorf("%q can be classified Blocking by classifyResidual but has no entry in existenceVerifiers - register a real verifier, or unimplementedVerifier(\"<sdk-pkg>\") as an honest placeholder, so this type's residuals always get a probe outcome", awsType)
		}
	}
}

// --- three-valued outcomes, one representative type per AWS protocol family --------

func TestVerifyEKSClusterThreeValuedOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		want    existenceState
		wantErr string
	}{
		{"exists", nil, existenceExists, ""},
		{"not found", &ekstypes.ResourceNotFoundException{Message: aws.String("gone")}, existenceNotFound, ""},
		{"access denied", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}, existenceError, "AccessDeniedException"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeVerifierAPI{describeCluster: func(*eks.DescribeClusterInput) (*eks.DescribeClusterOutput, error) {
				if c.err != nil {
					return nil, c.err
				}
				return &eks.DescribeClusterOutput{}, nil
			}}
			state, note := verifyEKSCluster(context.Background(), api.apiSet(), "arn:aws:eks:us-east-1:1:cluster/custx-bi")
			if state != c.want {
				t.Errorf("state = %v, want %v (note=%q)", state, c.want, note)
			}
			if c.wantErr != "" && !strings.Contains(note, c.wantErr) {
				t.Errorf("note = %q, want it to mention %q", note, c.wantErr)
			}
		})
	}
}

func TestVerifyEKSNodegroupParsesCompoundARN(t *testing.T) {
	var gotCluster, gotNodegroup string
	api := &fakeVerifierAPI{describeNodegroup: func(in *eks.DescribeNodegroupInput) (*eks.DescribeNodegroupOutput, error) {
		gotCluster, gotNodegroup = aws.ToString(in.ClusterName), aws.ToString(in.NodegroupName)
		return &eks.DescribeNodegroupOutput{}, nil
	}}
	state, _ := verifyEKSNodegroup(context.Background(), api.apiSet(), "arn:aws:eks:us-east-1:1:nodegroup/custx-bi/workers/a1b2c3")
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists", state)
	}
	if gotCluster != "custx-bi" || gotNodegroup != "workers" {
		t.Errorf("parsed (cluster,nodegroup) = (%q,%q), want (custx-bi,workers)", gotCluster, gotNodegroup)
	}
}

func TestVerifyRDSClusterNotFound(t *testing.T) {
	api := &fakeVerifierAPI{describeDBClusters: func(*rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		return nil, &rdstypes.DBClusterNotFoundFault{Message: aws.String("gone")}
	}}
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), "arn:aws:rds:us-east-1:1:cluster:custx-bi")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound (note=%q)", state, note)
	}
}

// EC2 has no modeled fault types; "not found" is a bare smithy error CODE. Getting the
// exact documented code wrong is exactly the bug this test would catch.
func TestVerifyEC2VPCMatchesDocumentedNotFoundCode(t *testing.T) {
	api := &fakeVerifierAPI{describeVpcs: func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "InvalidVpcID.NotFound", Message: "gone"}
	}}
	state, _ := verifyEC2VPC(context.Background(), api.apiSet(), "arn:aws:ec2:us-east-1:1:vpc/vpc-0123456789")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound", state)
	}
	// A DIFFERENT EC2 error code must not be mistaken for "gone" - only the documented
	// code for this specific resource type counts.
	api.describeVpcs = func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "InvalidSubnetID.NotFound", Message: "wrong resource's code"}
	}
	state, note := verifyEC2VPC(context.Background(), api.apiSet(), "arn:aws:ec2:us-east-1:1:vpc/vpc-0123456789")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError for an unrecognized code (note=%q)", state, note)
	}
}

// A nat gateway is the one EC2 type where "gone" is not exclusively an error path:
// DescribeNatGateways returns a normal 200 with State=="deleted".
func TestVerifyEC2NatGatewayTreatsStateDeletedAsGone(t *testing.T) {
	api := &fakeVerifierAPI{describeNatGateways: func(*ec2.DescribeNatGatewaysInput) (*ec2.DescribeNatGatewaysOutput, error) {
		return &ec2.DescribeNatGatewaysOutput{NatGateways: []ec2types.NatGateway{{State: ec2types.NatGatewayStateDeleted}}}, nil
	}}
	state, note := verifyEC2NatGateway(context.Background(), api.apiSet(), "arn:aws:ec2:us-east-1:1:natgateway/nat-0123456789")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound for State=deleted (note=%q)", state, note)
	}
	api.describeNatGateways = func(*ec2.DescribeNatGatewaysInput) (*ec2.DescribeNatGatewaysOutput, error) {
		return &ec2.DescribeNatGatewaysOutput{NatGateways: []ec2types.NatGateway{{State: ec2types.NatGatewayStateAvailable}}}, nil
	}
	state, _ = verifyEC2NatGateway(context.Background(), api.apiSet(), "arn:aws:ec2:us-east-1:1:natgateway/nat-0123456789")
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists for a live nat gateway", state)
	}
}

func TestVerifyIAMRoleAndPolicy(t *testing.T) {
	api := &fakeVerifierAPI{
		getRole: func(in *iam.GetRoleInput) (*iam.GetRoleOutput, error) {
			if aws.ToString(in.RoleName) != "custx-bi-flux-source-controller" {
				t.Errorf("GetRole got role name %q, want the path stripped to the bare name", aws.ToString(in.RoleName))
			}
			return nil, &iamtypes.NoSuchEntityException{Message: aws.String("gone")}
		},
		getPolicy: func(in *iam.GetPolicyInput) (*iam.GetPolicyOutput, error) {
			if aws.ToString(in.PolicyArn) != "arn:aws:iam::1:policy/custx-bi-policy" {
				t.Errorf("GetPolicy got %q, want the full ARN passed through unmodified", aws.ToString(in.PolicyArn))
			}
			return &iam.GetPolicyOutput{}, nil
		},
	}
	state, _ := verifyIAMRole(context.Background(), api.apiSet(), "arn:aws:iam::1:role/service-role/custx-bi-flux-source-controller")
	if state != existenceNotFound {
		t.Errorf("role state = %v, want existenceNotFound", state)
	}
	state, _ = verifyIAMPolicy(context.Background(), api.apiSet(), "arn:aws:iam::1:policy/custx-bi-policy")
	if state != existenceExists {
		t.Errorf("policy state = %v, want existenceExists", state)
	}
}

// DescribeDBClusterParameterGroups' deserializer maps the wire code
// "DBParameterGroupNotFound" to *rdstypes.DBParameterGroupNotFoundFault, not the
// cluster-flavored fault type the API name suggests. Before the fix this probe only
// matched *rdstypes.DBClusterParameterGroupNotFoundFault, which the SDK never actually
// returns from this call, so it could never reach a definite NotFound.
func TestVerifyRDSClusterParameterGroupPlainFaultIsNotFound(t *testing.T) {
	api := &fakeVerifierAPI{describeDBClusterParameterGroups: func(*rds.DescribeDBClusterParameterGroupsInput) (*rds.DescribeDBClusterParameterGroupsOutput, error) {
		return nil, &rdstypes.DBParameterGroupNotFoundFault{Message: aws.String("gone")}
	}}
	state, note := verifyRDSClusterParameterGroup(context.Background(), api.apiSet(), "arn:aws:rds:us-east-1:1:cluster-pg:custx-pg14")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound (note=%q)", state, note)
	}
	if note != "DBParameterGroupNotFoundFault" {
		t.Errorf("note = %q, want DBParameterGroupNotFoundFault", note)
	}
}

func TestVerifyS3BucketNotFoundCodes(t *testing.T) {
	for _, code := range []string{"NotFound", "NoSuchBucket"} {
		api := &fakeVerifierAPI{headBucket: func(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
			return nil, &smithy.GenericAPIError{Code: code, Message: "gone"}
		}}
		state, _ := verifyS3Bucket(context.Background(), api.apiSet(), "arn:aws:s3:::custx-bi-uploads")
		if state != existenceNotFound {
			t.Errorf("code %s: state = %v, want existenceNotFound", code, state)
		}
	}
}

// An S3 ARN carries no region, so a DR-region bucket gets HeadBucket'd against the
// primary region and answers with a bare HTTP redirect (301/307/308, no body). s3shared
// derives a smithy error code from http.StatusText for that shape, matching neither
// "NotFound" nor "NoSuchBucket". Before the fix that fell through to existenceError and
// the residual was demoted as unconfirmed; the redirect is actually proof the bucket
// exists, so the residual must stay Blocking.
func TestVerifyS3BucketRedirectMeansExists(t *testing.T) {
	for _, code := range []string{"MovedPermanently", "PermanentRedirect", "TemporaryRedirect"} {
		api := &fakeVerifierAPI{headBucket: func(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
			return nil, &smithy.GenericAPIError{Code: code, Message: "redirect"}
		}}
		state, note := verifyS3Bucket(context.Background(), api.apiSet(), "arn:aws:s3:::custx-dr-uploads")
		if state != existenceExists {
			t.Errorf("code %s: state = %v, want existenceExists (note=%q)", code, state, note)
		}
	}

	// End to end: a DR-region bucket residual must stay Blocking, not get silently
	// demoted the way every DR S3 orphan was before this fix.
	api := &fakeVerifierAPI{headBucket: func(*s3.HeadBucketInput) (*s3.HeadBucketOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "MovedPermanently", Message: "redirect"}
	}}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:s3:::custx-dr-uploads", Type: "s3", Blocking: true},
	}}
	clientsFor := func(string) (*verifierAPISet, error) { return api.apiSet(), nil }
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", clientsFor)
	if !rep.Residuals[0].Blocking {
		t.Fatalf("DR-region S3 residual was demoted (Why=%q); want it to stay Blocking", rep.Residuals[0].Why)
	}
}

// No per-name Describe exists for a log group: an empty page is NotFound, and a
// same-prefix DIFFERENT log group must not be mistaken for the target.
func TestVerifyLogGroupPrefixSemantics(t *testing.T) {
	api := &fakeVerifierAPI{describeLogGroups: func(*cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
		return &cloudwatchlogs.DescribeLogGroupsOutput{}, nil
	}}
	state, note := verifyLogGroup(context.Background(), api.apiSet(), "arn:aws:logs:us-east-1:1:log-group:/custx-bi/app:*")
	if state != existenceNotFound {
		t.Fatalf("empty page: state = %v, want existenceNotFound (note=%q)", state, note)
	}

	api.describeLogGroups = func(*cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
		return &cloudwatchlogs.DescribeLogGroupsOutput{LogGroups: []cwltypes.LogGroup{{LogGroupName: aws.String("/custx-bi/app-extra")}}}, nil
	}
	state, note = verifyLogGroup(context.Background(), api.apiSet(), "arn:aws:logs:us-east-1:1:log-group:/custx-bi/app:*")
	if state != existenceNotFound {
		t.Fatalf("prefix caught a DIFFERENT log group: state = %v, want existenceNotFound (note=%q)", state, note)
	}

	api.describeLogGroups = func(*cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
		return &cloudwatchlogs.DescribeLogGroupsOutput{LogGroups: []cwltypes.LogGroup{{LogGroupName: aws.String("/custx-bi/app")}}}, nil
	}
	state, _ = verifyLogGroup(context.Background(), api.apiSet(), "arn:aws:logs:us-east-1:1:log-group:/custx-bi/app:*")
	if state != existenceExists {
		t.Fatalf("exact match: state = %v, want existenceExists", state)
	}
}

// --- Round 2 finding 4: verifyLogGroup only ever read the first DescribeLogGroups
// page. A prefix catching more than one page of siblings would then report a log group
// on page 2 as a definite NotFound, when the scan never actually looked at it. Fails
// against the pre-fix code, which built the input with no NextToken and returned
// existenceNotFound as soon as page 1 had no exact match, never issuing a second call.
// Confirmed by hand: reverting verifyLogGroup to drop the NextToken plumbing and return
// on the first page reproduces this failure (the mock's second closure - which the
// pre-fix call never invokes - would go uncalled and the exact match would be missed).
func TestVerifyLogGroupMatchOnSecondPageIsNotReportedGone(t *testing.T) {
	var calls int
	api := &fakeVerifierAPI{describeLogGroups: func(in *cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
		calls++
		switch calls {
		case 1:
			if in.NextToken != nil {
				t.Errorf("first call NextToken = %q, want nil", aws.ToString(in.NextToken))
			}
			return &cloudwatchlogs.DescribeLogGroupsOutput{
				LogGroups: []cwltypes.LogGroup{{LogGroupName: aws.String("/custx-bi/app-extra")}},
				NextToken: aws.String("page-2"),
			}, nil
		case 2:
			if aws.ToString(in.NextToken) != "page-2" {
				t.Errorf("second call NextToken = %q, want page-2", aws.ToString(in.NextToken))
			}
			return &cloudwatchlogs.DescribeLogGroupsOutput{
				LogGroups: []cwltypes.LogGroup{{LogGroupName: aws.String("/custx-bi/app")}},
			}, nil
		default:
			t.Fatalf("unexpected call %d", calls)
			return nil, nil
		}
	}}
	state, note := verifyLogGroup(context.Background(), api.apiSet(), "arn:aws:logs:us-east-1:1:log-group:/custx-bi/app:*")
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (must read the second page)", calls)
	}
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists - the exact match on page 2 must not be missed (note=%q)", state, note)
	}
}

func TestVerifyDMSEndpointFiltersByARN(t *testing.T) {
	target := "arn:aws:dms:us-east-1:1:endpoint:custx-bi-src"
	api := &fakeVerifierAPI{describeEndpoints: func(in *databasemigrationservice.DescribeEndpointsInput) (*databasemigrationservice.DescribeEndpointsOutput, error) {
		if len(in.Filters) != 1 || in.Filters[0].Name == nil || *in.Filters[0].Name != "endpoint-arn" {
			t.Fatalf("expected an endpoint-arn filter, got %+v", in.Filters)
		}
		return &databasemigrationservice.DescribeEndpointsOutput{}, nil
	}}
	state, note := verifyDMSEndpoint(context.Background(), api.apiSet(), target)
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound for an empty filtered result (note=%q)", state, note)
	}
}

// dms:subgrp has no ARN field in the API response at all (janitor.go's arnResourceID
// comment) - it must filter by the bare identifier, not the ARN.
func TestVerifyDMSSubnetGroupFiltersByIdentifier(t *testing.T) {
	api := &fakeVerifierAPI{describeReplicationSubnetGroups: func(in *databasemigrationservice.DescribeReplicationSubnetGroupsInput) (*databasemigrationservice.DescribeReplicationSubnetGroupsOutput, error) {
		if len(in.Filters) != 1 || aws.ToString(in.Filters[0].Name) != "replication-subnet-group-id" || len(in.Filters[0].Values) != 1 || in.Filters[0].Values[0] != "custx-bi-subgrp" {
			t.Fatalf("expected a replication-subnet-group-id filter for the bare identifier, got %+v", in.Filters)
		}
		return &databasemigrationservice.DescribeReplicationSubnetGroupsOutput{
			ReplicationSubnetGroups: []dmstypes.ReplicationSubnetGroup{{ReplicationSubnetGroupIdentifier: aws.String("custx-bi-subgrp")}},
		}, nil
	}}
	state, _ := verifyDMSSubnetGroup(context.Background(), api.apiSet(), "arn:aws:dms:us-east-1:1:subgrp:custx-bi-subgrp")
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists", state)
	}
}

// replication-config has no documented filter name, so it lists and matches the ARN
// field in Go - this exercises the pagination and the exact-ARN match.
func TestVerifyDMSReplicationConfigListsAndMatchesByARN(t *testing.T) {
	target := "arn:aws:dms:us-east-1:1:replication-config:custx-bi-cfg"
	api := &fakeVerifierAPI{describeReplicationConfigs: func(in *databasemigrationservice.DescribeReplicationConfigsInput) (*databasemigrationservice.DescribeReplicationConfigsOutput, error) {
		if in.Marker == nil {
			return &databasemigrationservice.DescribeReplicationConfigsOutput{
				ReplicationConfigs: []dmstypes.ReplicationConfig{{ReplicationConfigArn: aws.String("arn:aws:dms:us-east-1:1:replication-config:other")}},
				Marker:             aws.String("page2"),
			}, nil
		}
		return &databasemigrationservice.DescribeReplicationConfigsOutput{
			ReplicationConfigs: []dmstypes.ReplicationConfig{{ReplicationConfigArn: aws.String(target)}},
		}, nil
	}}
	state, note := verifyDMSReplicationConfig(context.Background(), api.apiSet(), target)
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists across two pages (note=%q)", state, note)
	}
}

// The caveat named in the brief: a secret already scheduled for deletion still returns
// 200 from DescribeSecret with DeletedDate populated. Reading that as Exists would
// false-positive a correct destroy for the whole 7-30 day window.
func TestVerifySecretsManagerSecretScheduledDeletionIsGone(t *testing.T) {
	api := &fakeVerifierAPI{describeSecret: func(*secretsmanager.DescribeSecretInput) (*secretsmanager.DescribeSecretOutput, error) {
		return &secretsmanager.DescribeSecretOutput{DeletedDate: aws.Time(time.Now())}, nil
	}}
	state, note := verifySecretsManagerSecret(context.Background(), api.apiSet(), "arn:aws:secretsmanager:us-east-1:1:secret:custx-bi-db-abc123")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound for a scheduled-deletion secret (note=%q)", state, note)
	}
	api.describeSecret = func(*secretsmanager.DescribeSecretInput) (*secretsmanager.DescribeSecretOutput, error) {
		return &secretsmanager.DescribeSecretOutput{}, nil
	}
	state, _ = verifySecretsManagerSecret(context.Background(), api.apiSet(), "arn:aws:secretsmanager:us-east-1:1:secret:custx-bi-db-abc123")
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists for a live secret", state)
	}
	api.describeSecret = func(*secretsmanager.DescribeSecretInput) (*secretsmanager.DescribeSecretOutput, error) {
		return nil, &smtypes.ResourceNotFoundException{Message: aws.String("gone")}
	}
	state, _ = verifySecretsManagerSecret(context.Background(), api.apiSet(), "arn:aws:secretsmanager:us-east-1:1:secret:custx-bi-db-abc123")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound for ResourceNotFoundException", state)
	}
}

func TestVerifySQSQueueDoesNotExist(t *testing.T) {
	api := &fakeVerifierAPI{getQueueUrl: func(*sqs.GetQueueUrlInput) (*sqs.GetQueueUrlOutput, error) {
		return nil, &sqstypes.QueueDoesNotExist{Message: aws.String("gone")}
	}}
	state, _ := verifySQSQueue(context.Background(), api.apiSet(), "arn:aws:sqs:us-east-1:1:custx-bi-queue")
	if state != existenceNotFound {
		t.Fatalf("state = %v, want existenceNotFound", state)
	}
}

// A type with no SDK client vendored today (go.mod lacks kms, elbv2, kafka, elasticache,
// efs, opensearchservice, sns) must still fail OPEN with a clear, actionable note rather
// than either panicking or silently deciding Exists/NotFound with no probe at all.
func TestUnimplementedVerifierFailsOpenWithAnActionableNote(t *testing.T) {
	state, note := existenceVerifiers["kms:key"](context.Background(), &verifierAPISet{}, "arn:aws:kms:us-east-1:1:key/abc")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError", state)
	}
	if !strings.Contains(note, "aws-sdk-go-v2/service/kms") {
		t.Errorf("note = %q, want it to name the missing SDK package so the gap is actionable", note)
	}
}

// --- orchestration: region-awareness and the fail-open wiring ----------------------

// MANDATORY per the brief: a residual whose ARN is in the DR region must build (and
// probe with) a client for THAT region, never the caller's default/primary region. A
// default-region client would silently NotFound a real DR-region resource and fail open
// on an actual leak - worse than not probing at all, because it looks like verification
// happened.
func TestVerifyResidualExistenceIsRegionAware(t *testing.T) {
	const primaryRegion = "us-east-1"
	const drRegion = "us-west-2"
	drARN := "arn:aws:eks:" + drRegion + ":1:cluster/custx-bi-dr"

	var requestedRegions []string
	factory := func(region string) (*verifierAPISet, error) {
		requestedRegions = append(requestedRegions, region)
		return (&fakeVerifierAPI{describeCluster: func(*eks.DescribeClusterInput) (*eks.DescribeClusterOutput, error) {
			return &eks.DescribeClusterOutput{}, nil // Exists
		}}).apiSet(), nil
	}

	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: drARN, Type: "eks:cluster", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, primaryRegion, factory)

	if len(requestedRegions) != 1 || requestedRegions[0] != drRegion {
		t.Fatalf("client factory was called with regions %v, want exactly [%q] (the ARN's own DR region, never the default %q)", requestedRegions, drRegion, primaryRegion)
	}
	if !rep.Residuals[0].Blocking {
		t.Errorf("a genuinely-existing DR-region resource must stay Blocking, got Why=%q", rep.Residuals[0].Why)
	}
}

// rds:global-cluster is the one type whose ARN carries no region at all - the default
// region must be used, and ONLY for that reason (see the region-awareness test above
// for the opposite case: an ARN that DOES carry a region must never fall back).
func TestVerifyResidualExistenceFallsBackToDefaultRegionOnlyWhenARNHasNone(t *testing.T) {
	const primaryRegion = "us-east-1"
	globalARN := "arn:aws:rds::1:global-cluster:custx-bi-global"

	var requestedRegions []string
	factory := func(region string) (*verifierAPISet, error) {
		requestedRegions = append(requestedRegions, region)
		return (&fakeVerifierAPI{describeGlobalClusters: func(*rds.DescribeGlobalClustersInput) (*rds.DescribeGlobalClustersOutput, error) {
			return nil, &rdstypes.GlobalClusterNotFoundFault{Message: aws.String("gone")}
		}}).apiSet(), nil
	}

	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: globalARN, Type: "rds:global-cluster", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, primaryRegion, factory)

	if len(requestedRegions) != 1 || requestedRegions[0] != primaryRegion {
		t.Fatalf("client factory was called with regions %v, want exactly [%q]", requestedRegions, primaryRegion)
	}
	if rep.Residuals[0].Blocking {
		t.Error("a confirmed-gone global cluster must be demoted")
	}
}

// The core Class C scenario: a residual classifyResidual marked Blocking, but the live
// resource is actually already gone (the Resource Groups Tagging API lagging behind a
// successful delete, the same class of staleness insufficientAloneTypes already
// documents for security groups and pod identity associations - just for a type outside
// that allowlist). The probe must demote it AND explain why, and must NOT add an
// Incomplete note for a clean NotFound (that note is reserved for Error).
func TestVerifyResidualExistenceDemotesAConfirmedGoneOrphan(t *testing.T) {
	factory := func(string) (*verifierAPISet, error) {
		return (&fakeVerifierAPI{describeDBInstances: func(*rds.DescribeDBInstancesInput) (*rds.DescribeDBInstancesOutput, error) {
			return nil, &rdstypes.DBInstanceNotFoundFault{Message: aws.String("gone")}
		}}).apiSet(), nil
	}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:rds:us-east-1:1:db:custx-bi-writer", Type: "rds:db", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", factory)

	if rep.Residuals[0].Blocking {
		t.Fatal("a resource the probe confirmed is gone must be demoted")
	}
	if !strings.Contains(rep.Residuals[0].Why, "gone") {
		t.Errorf("Why = %q, want it to explain the probe confirmed the resource is gone", rep.Residuals[0].Why)
	}
	if len(rep.Incomplete) != 0 {
		t.Errorf("a clean NotFound must not add an Incomplete note (that is reserved for Error): %+v", rep.Incomplete)
	}
}

// The Error path: AccessDenied must demote (fail open) AND leave a visible Incomplete
// note - collapsing this into the same silent-non-blocking outcome as a clean NotFound
// is exactly the ambiguity the three-valued design exists to prevent.
func TestVerifyResidualExistenceFailsOpenLoudlyOnAnUnclassifiableError(t *testing.T) {
	factory := func(string) (*verifierAPISet, error) {
		return (&fakeVerifierAPI{describeDBInstances: func(*rds.DescribeDBInstancesInput) (*rds.DescribeDBInstancesOutput, error) {
			return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}
		}}).apiSet(), nil
	}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:rds:us-east-1:1:db:custx-bi-writer", Type: "rds:db", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", factory)

	if rep.Residuals[0].Blocking {
		t.Fatal("an unclassifiable probe error must fail open (demoted), never left Blocking")
	}
	if len(rep.Incomplete) != 1 || !strings.Contains(rep.Incomplete[0], "AccessDeniedException") {
		t.Errorf("expected exactly one Incomplete note naming the probe failure, got %+v", rep.Incomplete)
	}
}

// A state-remnant residual carries an Address, never an ARN - there is no live resource
// for the existence probe to check, so it must pass through untouched.
func TestVerifyResidualExistenceLeavesStateRemnantsAlone(t *testing.T) {
	called := false
	factory := func(string) (*verifierAPISet, error) {
		called = true
		return nil, nil
	}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "state-remnant", Address: "module.vpc[0].aws_subnet.private[0]", Type: "aws_subnet", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", factory)
	if called {
		t.Error("the existence probe must never run for a state-remnant hit - it has no ARN to check")
	}
	if !rep.Residuals[0].Blocking {
		t.Error("a state-remnant hit must be left untouched")
	}
}

// A residual of a type with no registered verifier at all (unreachable in production
// today - TestEveryBlockingTypeHasAnExistenceVerifier guards it - but the fail-open
// behavior itself must still be correct if that guard is ever bypassed).
func TestVerifyResidualExistenceDemotesAnUnregisteredType(t *testing.T) {
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:made-up-service:us-east-1:1:thing/x", Type: "made-up-service:thing", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", func(string) (*verifierAPISet, error) { return nil, nil })
	if rep.Residuals[0].Blocking {
		t.Fatal("an unregistered type must fail open")
	}
	if len(rep.Incomplete) != 1 {
		t.Fatalf("expected exactly one Incomplete note, got %+v", rep.Incomplete)
	}
}

// --- Finding 1 sweep: a nil/empty response with err == nil is an anomalous SDK
// response, never a legitimate "confirmed gone" - collapsing the two silently hides a
// real leak behind a made-up answer. Each of these fails against the pre-fix code,
// which read the nil response as existenceNotFound (or, for Secrets Manager,
// existenceExists) instead of existenceError.

func TestVerifyEC2NatGatewayNilResponseIsErrorNotNotFound(t *testing.T) {
	api := &fakeVerifierAPI{describeNatGateways: func(*ec2.DescribeNatGatewaysInput) (*ec2.DescribeNatGatewaysOutput, error) {
		return nil, nil
	}}
	state, note := verifyEC2NatGateway(context.Background(), api.apiSet(), "arn:aws:ec2:us-east-1:1:natgateway/nat-0123456789")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError for a nil response with no error (note=%q)", state, note)
	}
}

func TestVerifyDMSReplicationConfigNilResponseIsErrorNotNotFound(t *testing.T) {
	api := &fakeVerifierAPI{describeReplicationConfigs: func(*databasemigrationservice.DescribeReplicationConfigsInput) (*databasemigrationservice.DescribeReplicationConfigsOutput, error) {
		return nil, nil
	}}
	state, note := verifyDMSReplicationConfig(context.Background(), api.apiSet(), "arn:aws:dms:us-east-1:1:replication-config:custx-bi-cfg")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError for a nil page with no error (note=%q)", state, note)
	}
}

func TestVerifyLogGroupNilResponseIsErrorButNonNilEmptyPageStaysNotFound(t *testing.T) {
	api := &fakeVerifierAPI{describeLogGroups: func(*cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
		return nil, nil
	}}
	state, note := verifyLogGroup(context.Background(), api.apiSet(), "arn:aws:logs:us-east-1:1:log-group:/custx-bi/app:*")
	if state != existenceError {
		t.Fatalf("nil output: state = %v, want existenceError (note=%q)", state, note)
	}
	// A genuinely empty NON-NIL page is still the legitimate NotFound case (already
	// covered by TestVerifyLogGroupPrefixSemantics) - this confirms the anomaly fix did
	// not swallow it.
	api.describeLogGroups = func(*cloudwatchlogs.DescribeLogGroupsInput) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
		return &cloudwatchlogs.DescribeLogGroupsOutput{}, nil
	}
	state, _ = verifyLogGroup(context.Background(), api.apiSet(), "arn:aws:logs:us-east-1:1:log-group:/custx-bi/app:*")
	if state != existenceNotFound {
		t.Fatalf("non-nil empty page: state = %v, want existenceNotFound", state)
	}
}

func TestVerifyDMSFilteredCallsNilResponseIsErrorNotNotFound(t *testing.T) {
	cases := []struct {
		name string
		arn  string
		call existenceVerifier
		set  func(*fakeVerifierAPI)
	}{
		{
			name: "replication instance",
			arn:  "arn:aws:dms:us-east-1:1:rep:custx-bi-rep",
			call: verifyDMSReplicationInstance,
			set: func(f *fakeVerifierAPI) {
				f.describeReplicationInstances = func(*databasemigrationservice.DescribeReplicationInstancesInput) (*databasemigrationservice.DescribeReplicationInstancesOutput, error) {
					return nil, nil
				}
			},
		},
		{
			name: "endpoint",
			arn:  "arn:aws:dms:us-east-1:1:endpoint:custx-bi-src",
			call: verifyDMSEndpoint,
			set: func(f *fakeVerifierAPI) {
				f.describeEndpoints = func(*databasemigrationservice.DescribeEndpointsInput) (*databasemigrationservice.DescribeEndpointsOutput, error) {
					return nil, nil
				}
			},
		},
		{
			name: "replication task",
			arn:  "arn:aws:dms:us-east-1:1:task:custx-bi-task",
			call: verifyDMSReplicationTask,
			set: func(f *fakeVerifierAPI) {
				f.describeReplicationTasks = func(*databasemigrationservice.DescribeReplicationTasksInput) (*databasemigrationservice.DescribeReplicationTasksOutput, error) {
					return nil, nil
				}
			},
		},
		{
			name: "certificate",
			arn:  "arn:aws:dms:us-east-1:1:cert:custx-bi-cert",
			call: verifyDMSCertificate,
			set: func(f *fakeVerifierAPI) {
				f.describeCertificates = func(*databasemigrationservice.DescribeCertificatesInput) (*databasemigrationservice.DescribeCertificatesOutput, error) {
					return nil, nil
				}
			},
		},
		{
			name: "subnet group",
			arn:  "arn:aws:dms:us-east-1:1:subgrp:custx-bi-subgrp",
			call: verifyDMSSubnetGroup,
			set: func(f *fakeVerifierAPI) {
				f.describeReplicationSubnetGroups = func(*databasemigrationservice.DescribeReplicationSubnetGroupsInput) (*databasemigrationservice.DescribeReplicationSubnetGroupsOutput, error) {
					return nil, nil
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeVerifierAPI{}
			c.set(api)
			state, note := c.call(context.Background(), api.apiSet(), c.arn)
			if state != existenceError {
				t.Fatalf("state = %v, want existenceError for a nil response with no error (note=%q)", state, note)
			}
		})
	}
}

func TestVerifySecretsManagerSecretNilResponseIsErrorNotExists(t *testing.T) {
	api := &fakeVerifierAPI{describeSecret: func(*secretsmanager.DescribeSecretInput) (*secretsmanager.DescribeSecretOutput, error) {
		return nil, nil
	}}
	state, note := verifySecretsManagerSecret(context.Background(), api.apiSet(), "arn:aws:secretsmanager:us-east-1:1:secret:custx-bi-db-abc123")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError for a nil response with no error, not a confident existenceExists (note=%q)", state, note)
	}
}

// --- Finding 3: the EC2 id-filtered verifiers must not read err == nil as Exists
// without checking the response actually contained the resource. Fails against the
// pre-fix code, which discarded the output entirely and returned existenceExists on any
// err == nil, including an empty list.

func TestVerifyEC2FilteredListsTreatEmptySuccessAsErrorNotExists(t *testing.T) {
	cases := []struct {
		name string
		arn  string
		call existenceVerifier
		set  func(*fakeVerifierAPI)
	}{
		{
			name: "vpc",
			arn:  "arn:aws:ec2:us-east-1:1:vpc/vpc-0123456789",
			call: verifyEC2VPC,
			set: func(f *fakeVerifierAPI) {
				f.describeVpcs = func(*ec2.DescribeVpcsInput) (*ec2.DescribeVpcsOutput, error) {
					return &ec2.DescribeVpcsOutput{}, nil
				}
			},
		},
		{
			name: "subnet",
			arn:  "arn:aws:ec2:us-east-1:1:subnet/subnet-0123456789",
			call: verifyEC2Subnet,
			set: func(f *fakeVerifierAPI) {
				f.describeSubnets = func(*ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
					return &ec2.DescribeSubnetsOutput{}, nil
				}
			},
		},
		{
			name: "route table",
			arn:  "arn:aws:ec2:us-east-1:1:route-table/rtb-0123456789",
			call: verifyEC2RouteTable,
			set: func(f *fakeVerifierAPI) {
				f.describeRouteTables = func(*ec2.DescribeRouteTablesInput) (*ec2.DescribeRouteTablesOutput, error) {
					return &ec2.DescribeRouteTablesOutput{}, nil
				}
			},
		},
		{
			name: "network acl",
			arn:  "arn:aws:ec2:us-east-1:1:network-acl/acl-0123456789",
			call: verifyEC2NetworkACL,
			set: func(f *fakeVerifierAPI) {
				f.describeNetworkAcls = func(*ec2.DescribeNetworkAclsInput) (*ec2.DescribeNetworkAclsOutput, error) {
					return &ec2.DescribeNetworkAclsOutput{}, nil
				}
			},
		},
		{
			name: "internet gateway",
			arn:  "arn:aws:ec2:us-east-1:1:internet-gateway/igw-0123456789",
			call: verifyEC2InternetGateway,
			set: func(f *fakeVerifierAPI) {
				f.describeInternetGateways = func(*ec2.DescribeInternetGatewaysInput) (*ec2.DescribeInternetGatewaysOutput, error) {
					return &ec2.DescribeInternetGatewaysOutput{}, nil
				}
			},
		},
		{
			name: "elastic ip",
			arn:  "arn:aws:ec2:us-east-1:1:elastic-ip/eipalloc-0123456789",
			call: verifyEC2ElasticIP,
			set: func(f *fakeVerifierAPI) {
				f.describeAddresses = func(*ec2.DescribeAddressesInput) (*ec2.DescribeAddressesOutput, error) {
					return &ec2.DescribeAddressesOutput{}, nil
				}
			},
		},
		{
			name: "vpc endpoint",
			arn:  "arn:aws:ec2:us-east-1:1:vpc-endpoint/vpce-0123456789",
			call: verifyEC2VPCEndpoint,
			set: func(f *fakeVerifierAPI) {
				f.describeVpcEndpoints = func(*ec2.DescribeVpcEndpointsInput) (*ec2.DescribeVpcEndpointsOutput, error) {
					return &ec2.DescribeVpcEndpointsOutput{}, nil
				}
			},
		},
		{
			name: "volume",
			arn:  "arn:aws:ec2:us-east-1:1:volume/vol-0123456789",
			call: verifyEC2Volume,
			set: func(f *fakeVerifierAPI) {
				f.describeVolumes = func(*ec2.DescribeVolumesInput) (*ec2.DescribeVolumesOutput, error) {
					return &ec2.DescribeVolumesOutput{}, nil
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeVerifierAPI{}
			c.set(api)
			state, note := c.call(context.Background(), api.apiSet(), c.arn)
			if state != existenceError {
				t.Fatalf("state = %v, want existenceError for a 200 with an empty list - the documented contract for a missing id-filtered resource is an error code, not an empty success (note=%q)", state, note)
			}
		})
	}
}

// --- Finding 2: verifyRDSCluster must route Class A's secondary (DbClusterResourceId)
// ARN through a db-cluster-resource-id Filter, never through DBClusterIdentifier, which
// always 404s for that shape regardless of the cluster's real state.

func TestLooksLikeDBClusterResourceIDDiscriminatesFromUserIdentifiers(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"cluster-co3qcvn73c42v4nvzbedvlxpzu", true}, // real DbClusterResourceId shape, lower-cased
		{"cluster-short", false},                     // too short to be a resource id
		{"custx-bi-cluster-01", false},               // ordinary hyphenated identifier
		{"cluster-has-hyphens-inside-it-abc", false}, // hyphens inside the suffix
	}
	for _, c := range cases {
		if got := looksLikeDBClusterResourceID(c.id); got != c.want {
			t.Errorf("looksLikeDBClusterResourceID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// --- Round 2 (findings 1-3): the discriminator missed the real (uppercase) wire shape
// of DbClusterResourceId entirely, and even once it matches, the filter call itself
// mixed up which case to hand AWS and folded an anomalous nil response into the same
// branch as a legitimate empty one. Each test below fails against the code as it stood
// before this round: reverting looksLikeDBClusterResourceID to match the raw (not
// lower-cased) id reproduces the finding-1 failure; reverting
// verifyRDSClusterByResourceID to send `id` verbatim instead of strings.ToUpper(id), or
// to return the filter's own state directly instead of falling back through
// verifyRDSClusterByIdentifier, reproduces the finding-2 failures; reverting
// rdsClusterByResourceIDFilter to check len(out.DBClusters) before out == nil
// reproduces the finding-3 failure. Confirmed by hand for each test below.

func TestLooksLikeDBClusterResourceIDMatchesUppercaseWireShape(t *testing.T) {
	// The real DbClusterResourceId shape as the tagging API actually returns it - see
	// the package comment on dbClusterResourceIDPattern. Fails-before-fix: with the
	// pre-round-2 pattern (matched against the raw, not lower-cased, id) this returned
	// false, since the pattern only recognized the all-lowercase shape.
	if !looksLikeDBClusterResourceID("cluster-CO3QCVN73C42V4NVZBEDVLXPZU") {
		t.Fatal("looksLikeDBClusterResourceID(uppercase wire shape) = false, want true")
	}
}

func TestVerifyRDSClusterUppercaseResourceIDIsDetectedAndProbedViaFilter(t *testing.T) {
	resourceIDARN := "arn:aws:rds:us-east-1:1:cluster:cluster-CO3QCVN73C42V4NVZBEDVLXPZU"
	var gotFilters []rdstypes.Filter
	var gotIdentifier *string
	api := &fakeVerifierAPI{describeDBClusters: func(in *rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		gotFilters = in.Filters
		gotIdentifier = in.DBClusterIdentifier
		return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{DBClusterIdentifier: aws.String("custx-bi")}}}, nil
	}}
	// Fails-before-fix: with the lowercase-only discriminator, this ARN's tail never
	// looks like a resource id, so it falls through to the DBClusterIdentifier branch
	// (gotIdentifier would be set, gotFilters empty) - exactly the original bug.
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), resourceIDARN)
	if gotIdentifier != nil {
		t.Errorf("DBClusterIdentifier = %q, want it left unset - an uppercase resource-id must still route through the filter, never DBClusterIdentifier", aws.ToString(gotIdentifier))
	}
	if len(gotFilters) != 1 || aws.ToString(gotFilters[0].Name) != "db-cluster-resource-id" || len(gotFilters[0].Values) != 1 || gotFilters[0].Values[0] != "cluster-CO3QCVN73C42V4NVZBEDVLXPZU" {
		t.Fatalf("expected a single db-cluster-resource-id filter carrying the uppercase tail, got %+v", gotFilters)
	}
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists for a filter match (note=%q)", state, note)
	}
}

func TestVerifyRDSClusterLowercaseResourceIDStillWorks(t *testing.T) {
	// No regression: a lowercase tail (the shape residuals.go's own index normalizes
	// to) must still be detected and probed via the filter, uppercased on the wire per
	// finding 2.
	resourceIDARN := "arn:aws:rds:us-east-1:1:cluster:cluster-co3qcvn73c42v4nvzbedvlxpzu"
	var gotFilters []rdstypes.Filter
	var gotIdentifier *string
	api := &fakeVerifierAPI{describeDBClusters: func(in *rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		gotFilters = in.Filters
		gotIdentifier = in.DBClusterIdentifier
		return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{DBClusterIdentifier: aws.String("custx-bi")}}}, nil
	}}
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), resourceIDARN)
	if gotIdentifier != nil {
		t.Errorf("DBClusterIdentifier = %q, want it left unset - a resource-id must never be passed there (it always 404s)", aws.ToString(gotIdentifier))
	}
	// Fails-before-fix: the pre-round-2 code sent the filter value verbatim
	// ("cluster-co3qcvn73c42v4nvzbedvlxpzu"); this asserts it is uppercased to the
	// case AWS actually stores DbClusterResourceId in.
	if len(gotFilters) != 1 || aws.ToString(gotFilters[0].Name) != "db-cluster-resource-id" || len(gotFilters[0].Values) != 1 || gotFilters[0].Values[0] != "cluster-CO3QCVN73C42V4NVZBEDVLXPZU" {
		t.Fatalf("expected a single db-cluster-resource-id filter carrying the uppercased tail, got %+v", gotFilters)
	}
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists for a filter match (note=%q)", state, note)
	}
}

func TestVerifyRDSClusterResourceIDFilterEmptyForALiveClusterFallsBackAndDoesNotConfirmGone(t *testing.T) {
	// The filter has undocumented match semantics, so an empty result for a cluster
	// that IS live must not be trusted on its own: the fallback through
	// DBClusterIdentifier must run, and - because that fallback always 404s for a
	// genuine resource-id shape (see verifyRDSClusterByIdentifier's doc) - the overall
	// answer must land on existenceError, never a definite existenceNotFound.
	resourceIDARN := "arn:aws:rds:us-east-1:1:cluster:cluster-co3qcvn73c42v4nvzbedvlxpzu"
	var filterCalls, identifierCalls int
	api := &fakeVerifierAPI{describeDBClusters: func(in *rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		if len(in.Filters) == 1 {
			filterCalls++
			return &rds.DescribeDBClustersOutput{}, nil // empty despite the cluster being live
		}
		identifierCalls++
		return nil, &rdstypes.DBClusterNotFoundFault{Message: aws.String("gone")} // documented behavior for a resource-id-shaped identifier
	}}
	// Fails-before-fix: the pre-round-2 code returned existenceNotFound directly off
	// the empty filter result, with no fallback call at all.
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), resourceIDARN)
	if filterCalls != 1 || identifierCalls != 1 {
		t.Fatalf("filterCalls=%d identifierCalls=%d, want exactly one of each (fallback must run)", filterCalls, identifierCalls)
	}
	if state == existenceNotFound {
		t.Fatalf("state = existenceNotFound, want anything but a definite NotFound off an unconfirmed empty filter result (note=%q)", note)
	}
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError", state)
	}
}

func TestVerifyRDSClusterUserIdentifierMatchingResourceIDShapeStillResolves(t *testing.T) {
	// A legal user-chosen cluster identifier can satisfy the resource-id pattern
	// (leading letter, alphanumeric, no trailing/double hyphen, right length). The
	// filter will never match it (it isn't a real DbClusterResourceId), but the
	// fallback through DBClusterIdentifier must find it, since it IS the real
	// identifier - resolving both directions with the same retry.
	arn := "arn:aws:rds:us-east-1:1:cluster:cluster-abcdefghijklmnopqrstuvwxyz"
	var filterCalls, identifierCalls int
	api := &fakeVerifierAPI{describeDBClusters: func(in *rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		if len(in.Filters) == 1 {
			filterCalls++
			return &rds.DescribeDBClustersOutput{}, nil // no cluster has this as its resource id
		}
		identifierCalls++
		if aws.ToString(in.DBClusterIdentifier) != "cluster-abcdefghijklmnopqrstuvwxyz" {
			t.Errorf("DBClusterIdentifier = %q, want cluster-abcdefghijklmnopqrstuvwxyz", aws.ToString(in.DBClusterIdentifier))
		}
		return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{DBClusterIdentifier: in.DBClusterIdentifier}}}, nil
	}}
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), arn)
	if filterCalls != 1 || identifierCalls != 1 {
		t.Fatalf("filterCalls=%d identifierCalls=%d, want exactly one of each", filterCalls, identifierCalls)
	}
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists - the fallback must resolve a real user identifier that happens to match the resource-id shape (note=%q)", state, note)
	}
}

func TestVerifyRDSClusterResourceIDFilterNilResponseIsErrorNotNotFound(t *testing.T) {
	// Finding 3: a NIL out with err == nil on the byResourceID path must be checked
	// BEFORE the empty-slice check, and must return existenceError (with an Incomplete
	// note at the ResidualReport level), never a definite NotFound.
	resourceIDARN := "arn:aws:rds:us-east-1:1:cluster:cluster-co3qcvn73c42v4nvzbedvlxpzu"
	api := &fakeVerifierAPI{describeDBClusters: func(*rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		return nil, nil // anomalous: never legitimately happens on a healthy filtered-list call
	}}
	// Fails-before-fix: the pre-round-2 code folded `out == nil` into the same
	// `out == nil || len(out.DBClusters) == 0` branch as a genuine empty list and
	// returned existenceNotFound for both.
	state, note := rdsClusterByResourceIDFilter(context.Background(), api.apiSet(), "CLUSTER-CO3QCVN73C42V4NVZBEDVLXPZU")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError for a nil response with no error (note=%q)", state, note)
	}

	// And end to end through verifyResidualExistenceWith: a nil response must land an
	// Incomplete note and fail the residual open (not-Blocking demoted for the RIGHT
	// reason - "could not confirm", not "confirmed gone").
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: resourceIDARN, Type: "rds:cluster", Blocking: true},
	}}
	clientsFor := func(string) (*verifierAPISet, error) { return api.apiSet(), nil }
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", clientsFor)
	if rep.Residuals[0].Blocking {
		t.Fatal("a nil-response probe must fail open (Blocking demoted)")
	}
	if len(rep.Incomplete) != 1 {
		t.Fatalf("expected exactly one Incomplete note, got %+v", rep.Incomplete)
	}
}

// A regional resource type whose ARN happens to carry an empty region field (malformed
// or an unexpected shape) must never fall back to the caller's default region - that
// fallback is reserved for the region-less allowlist (iam, s3, rds:global-cluster).
// Before the fix, ANY empty-region ARN hit the fallback, so a wrong-region probe could
// have reported "confirmed gone" for a type that was never actually checked in its own
// region. This pins that the probe never even runs (clientsFor is never called) and the
// residual fails open with an Incomplete note, not a definite NotFound.
func TestVerifyResidualExistenceWithEmptyRegionNonRegionLessTypeFailsOpen(t *testing.T) {
	arn := "arn:aws:rds::1:cluster:custx-something" // empty region field, rds:cluster is regional
	api := &fakeVerifierAPI{describeDBClusters: func(*rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{DBClusterIdentifier: aws.String("custx-something")}}}, nil
	}}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: arn, Type: "rds:cluster", Blocking: true},
	}}
	var clientsForCalled bool
	clientsFor := func(region string) (*verifierAPISet, error) {
		clientsForCalled = true
		return api.apiSet(), nil
	}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", clientsFor)
	if clientsForCalled {
		t.Fatal("clientsFor was called - the defaultRegion fallback must not fire for a non-region-less type with an empty ARN region")
	}
	if rep.Residuals[0].Blocking {
		t.Fatal("residual must fail open (Blocking demoted), not stay Blocking off a probe that never ran")
	}
	if strings.Contains(rep.Residuals[0].Why, "confirmed the resource is gone") {
		t.Fatalf("Why = %q, must not report a definite NotFound", rep.Residuals[0].Why)
	}
	if len(rep.Incomplete) != 1 {
		t.Fatalf("expected exactly one Incomplete note, got %+v", rep.Incomplete)
	}
}

func TestVerifyRDSClusterResourceIDFilterNoMatchWithConfirmingFallbackIsNotFound(t *testing.T) {
	// The one shape that DOES still resolve to a definite NotFound off this path: the
	// filter comes back empty AND the fallback genuinely 404s - which is what always
	// happens for a real orphaned resource-id (see verifyRDSClusterByIdentifier's own
	// doc), so this call alone can never distinguish "genuinely gone" from "filter
	// missed it" - per finding 2 it now reports existenceError (fail open), covered
	// above by TestVerifyRDSClusterResourceIDFilterEmptyForALiveClusterFallsBackAndDoes
	// NotConfirmGone. This test only pins down that the fallback error path (as
	// opposed to a fallback existenceExists) never leaks through as existenceExists.
	resourceIDARN := "arn:aws:rds:us-east-1:1:cluster:cluster-co3qcvn73c42v4nvzbedvlxpzu"
	api := &fakeVerifierAPI{describeDBClusters: func(*rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		return &rds.DescribeDBClustersOutput{}, nil // both filter and fallback see this
	}}
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), resourceIDARN)
	if state == existenceExists {
		t.Fatalf("state = existenceExists, want anything but existenceExists off two empty responses (note=%q)", note)
	}
}

// TestVerifyRDSClusterRealProductionOutageARN pins the exact ARN shape that was
// causing the live false-positive: a blocking residual observed on a running harness
// job,
//
//	arn:aws:rds:us-east-1:076248559428:cluster:cluster-vcmmhmjp26uk3ie76rwwvnh2xu
//
// Production confirms the tagging API's tail arrives LOWERCASE (26 lowercase chars
// after "cluster-"), so finding 1's case-insensitive detection is defensive here (the
// old lowercase-only pattern already matched this real shape) - the actual bug is
// finding 2: the lowercase tail was sent to db-cluster-resource-id verbatim, a value
// AWS's DbClusterResourceId never stores, so the filter could never match a live
// cluster. This asserts the filter is canonicalized to uppercase for this exact ARN.
func TestVerifyRDSClusterRealProductionOutageARN(t *testing.T) {
	const outageARN = "arn:aws:rds:us-east-1:076248559428:cluster:cluster-vcmmhmjp26uk3ie76rwwvnh2xu"
	var gotFilters []rdstypes.Filter
	api := &fakeVerifierAPI{describeDBClusters: func(in *rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		gotFilters = in.Filters
		if len(in.Filters) == 1 {
			return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{DBClusterIdentifier: aws.String("prod-cluster")}}}, nil
		}
		return &rds.DescribeDBClustersOutput{}, nil
	}}
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), outageARN)
	if len(gotFilters) != 1 || aws.ToString(gotFilters[0].Name) != "db-cluster-resource-id" {
		t.Fatalf("expected a single db-cluster-resource-id filter, got %+v", gotFilters)
	}
	if want := "cluster-VCMMHMJP26UK3IE76RWWVNH2XU"; len(gotFilters[0].Values) != 1 || gotFilters[0].Values[0] != want {
		t.Fatalf("filter value = %+v, want [%q] - the lowercase tagging-API tail must be canonicalized to AWS's actual wire case", gotFilters[0].Values, want)
	}
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists - this ARN belongs to a live cluster in production (note=%q)", state, note)
	}
}

func TestVerifyRDSClusterOrdinaryIdentifierUnaffectedByResourceIDBranch(t *testing.T) {
	api := &fakeVerifierAPI{describeDBClusters: func(in *rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		if aws.ToString(in.DBClusterIdentifier) != "custx-bi-writer" {
			t.Errorf("DBClusterIdentifier = %q, want custx-bi-writer", aws.ToString(in.DBClusterIdentifier))
		}
		if len(in.Filters) != 0 {
			t.Errorf("Filters = %+v, want none for an ordinary identifier", in.Filters)
		}
		return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{{}}}, nil
	}}
	state, _ := verifyRDSCluster(context.Background(), api.apiSet(), "arn:aws:rds:us-east-1:1:cluster:custx-bi-writer")
	if state != existenceExists {
		t.Fatalf("state = %v, want existenceExists for an ordinary identifier match", state)
	}
}

func TestVerifyRDSClusterOrdinaryIdentifierEmptySuccessIsAnomalyError(t *testing.T) {
	api := &fakeVerifierAPI{describeDBClusters: func(*rds.DescribeDBClustersInput) (*rds.DescribeDBClustersOutput, error) {
		return &rds.DescribeDBClustersOutput{}, nil // never legitimately happens for DBClusterIdentifier
	}}
	state, note := verifyRDSCluster(context.Background(), api.apiSet(), "arn:aws:rds:us-east-1:1:cluster:custx-bi-writer")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError - a missing identifier always raises DBClusterNotFoundFault, so an empty success is anomalous, not a legitimate absence (note=%q)", state, note)
	}
}

// --- Finding 4: region derivation across more ARN shapes than the two existing
// orchestration tests exercise (a regional and a region-less ARN).

func TestArnRegionAcrossARNShapes(t *testing.T) {
	cases := []struct {
		name string
		arn  string
		want string
	}{
		{"regional", "arn:aws:eks:us-east-1:1:cluster/custx-bi", "us-east-1"},
		{"dr region", "arn:aws:eks:us-west-2:1:cluster/custx-bi-dr", "us-west-2"},
		{"region-less (rds global cluster)", "arn:aws:rds::1:global-cluster:custx-bi-global", ""},
		{"region-less (iam)", "arn:aws:iam::1:role/custx-bi-role", ""},
		{"region-less (s3)", "arn:aws:s3:::custx-bi-uploads", ""},
		{"malformed - too few fields", "arn:aws:ec2:us-east-1", ""},
		{"malformed - no arn prefix", "not-an-arn:aws:ec2:us-east-1:1:vpc/vpc-1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := arnRegion(c.arn); got != c.want {
				t.Errorf("arnRegion(%q) = %q, want %q", c.arn, got, c.want)
			}
		})
	}
}

// --- Finding 4: pagination coverage for the one paginated probe in this file.

func TestVerifyDMSReplicationConfigPageCapFailsOpen(t *testing.T) {
	pages := 0
	api := &fakeVerifierAPI{describeReplicationConfigs: func(*databasemigrationservice.DescribeReplicationConfigsInput) (*databasemigrationservice.DescribeReplicationConfigsOutput, error) {
		pages++
		return &databasemigrationservice.DescribeReplicationConfigsOutput{
			ReplicationConfigs: []dmstypes.ReplicationConfig{{ReplicationConfigArn: aws.String("arn:aws:dms:us-east-1:1:replication-config:never-matches")}},
			Marker:             aws.String("keep-going"),
		}, nil
	}}
	state, note := verifyDMSReplicationConfig(context.Background(), api.apiSet(), "arn:aws:dms:us-east-1:1:replication-config:custx-bi-cfg")
	if state != existenceError {
		t.Fatalf("state = %v, want existenceError once the page cap is hit (note=%q)", state, note)
	}
	if pages != maxDMSReplicationConfigPages {
		t.Errorf("pages fetched = %d, want exactly maxDMSReplicationConfigPages (%d)", pages, maxDMSReplicationConfigPages)
	}
	if !strings.Contains(note, "did not converge") {
		t.Errorf("note = %q, want it to explain the page cap was hit", note)
	}
}

// --- Finding 4: the fail-open orchestration paths, tested distinctly - each must land
// on Error-plus-Incomplete, never a bare NotFound that would read as a confirmed
// absence.

func TestVerifyResidualExistenceClientFactoryFailureFailsOpenLoudly(t *testing.T) {
	wantErr := errors.New("no credentials for this region")
	factory := func(string) (*verifierAPISet, error) { return nil, wantErr }
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:rds:us-east-1:1:db:custx-bi-writer", Type: "rds:db", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", factory)
	if rep.Residuals[0].Blocking {
		t.Fatal("a client-factory failure must fail open (demoted), never left Blocking")
	}
	if len(rep.Incomplete) != 1 || !strings.Contains(rep.Incomplete[0], "no credentials for this region") {
		t.Errorf("expected exactly one Incomplete note naming the client-build failure, got %+v", rep.Incomplete)
	}
}

func TestVerifyResidualExistenceMalformedARNFailsOpenLoudly(t *testing.T) {
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "not-a-real-arn", Type: "rds:db", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", func(string) (*verifierAPISet, error) { return nil, nil })
	if rep.Residuals[0].Blocking {
		t.Fatal("a malformed ARN must fail open (demoted), never left Blocking")
	}
	if len(rep.Incomplete) != 1 {
		t.Fatalf("expected exactly one Incomplete note, got %+v", rep.Incomplete)
	}
}

func TestVerifyResidualExistenceNilResponseFailsOpenLoudlyThroughOrchestration(t *testing.T) {
	factory := func(string) (*verifierAPISet, error) {
		return (&fakeVerifierAPI{describeNatGateways: func(*ec2.DescribeNatGatewaysInput) (*ec2.DescribeNatGatewaysOutput, error) {
			return nil, nil
		}}).apiSet(), nil
	}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:ec2:us-east-1:1:natgateway/nat-0123456789", Type: "ec2:natgateway", Blocking: true},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", factory)
	if rep.Residuals[0].Blocking {
		t.Fatal("an anomalous nil response must fail open (demoted), never left Blocking")
	}
	if len(rep.Incomplete) != 1 {
		t.Fatalf("expected exactly one Incomplete note distinguishing this from a clean NotFound, got %+v", rep.Incomplete)
	}
	if strings.Contains(rep.Residuals[0].Why, "confirmed the resource is gone") {
		t.Errorf("Why = %q, must not read as a confirmed NotFound for an unclassifiable nil response", rep.Residuals[0].Why)
	}
}

// --- Finding 4: the invariant that must never break - existence verification can only
// ever demote a Blocking residual, never promote a non-blocking one. This is the exact
// direction that would recreate the original false-positive outage if it regressed.

func TestVerifyResidualExistenceNeverPromotesANonBlockingResidual(t *testing.T) {
	probed := false
	factory := func(string) (*verifierAPISet, error) {
		return (&fakeVerifierAPI{describeDBInstances: func(*rds.DescribeDBInstancesInput) (*rds.DescribeDBInstancesOutput, error) {
			probed = true
			return &rds.DescribeDBInstancesOutput{}, nil // would report existenceExists if ever reached
		}}).apiSet(), nil
	}
	rep := &ResidualReport{Residuals: []Residual{
		{Source: "tag-reconcile", ARN: "arn:aws:rds:us-east-1:1:db:custx-bi-writer", Type: "rds:db", Blocking: false},
	}}
	verifyResidualExistenceWith(context.Background(), rep, "us-east-1", factory)
	if probed {
		t.Error("existence verification probed a residual that was already non-blocking - it must only ever demote, never promote")
	}
	if rep.Residuals[0].Blocking {
		t.Error("a non-blocking residual must never become Blocking as a side effect of existence verification")
	}
}

// --- Finding 4: callWithOneRetry and isThrottling had zero coverage.

func TestCallWithOneRetryRetriesExactlyOnceOnThrottling(t *testing.T) {
	calls := 0
	err := callWithOneRetry(context.Background(), func() error {
		calls++
		if calls < 2 {
			return &smithy.GenericAPIError{Code: "ThrottlingException", Message: "slow down"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil after the retry succeeds", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want exactly 2 (initial call plus one retry)", calls)
	}
}

func TestCallWithOneRetryDoesNotRetryNonThrottlingErrors(t *testing.T) {
	calls := 0
	wantErr := &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}
	err := callWithOneRetry(context.Background(), func() error {
		calls++
		return wantErr
	})
	if calls != 1 {
		t.Errorf("calls = %d, want exactly 1 - only throttling is worth retrying", calls)
	}
	if err != wantErr {
		t.Errorf("err = %v, want the original error passed through unchanged", err)
	}
}

func TestCallWithOneRetryGivesUpAfterOneRetry(t *testing.T) {
	calls := 0
	err := callWithOneRetry(context.Background(), func() error {
		calls++
		return &smithy.GenericAPIError{Code: "ThrottlingException", Message: "still slow"}
	})
	if calls != 2 {
		t.Errorf("calls = %d, want exactly 2 (no more than one retry even when the retry also throttles)", calls)
	}
	if err == nil {
		t.Error("err = nil, want the throttling error surfaced after the retry also fails")
	}
}

func TestIsThrottlingRecognizesEveryDocumentedCode(t *testing.T) {
	for _, code := range []string{"Throttling", "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded", "SlowDown", "RequestThrottled", "ThrottledException"} {
		if !isThrottling(&smithy.GenericAPIError{Code: code, Message: "x"}) {
			t.Errorf("isThrottling(%q) = false, want true", code)
		}
	}
}

func TestIsThrottlingRejectsNonThrottlingAndNonAPIErrors(t *testing.T) {
	if isThrottling(&smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}) {
		t.Error("isThrottling(AccessDeniedException) = true, want false")
	}
	if isThrottling(nil) {
		t.Error("isThrottling(nil) = true, want false")
	}
	if isThrottling(errors.New("plain error, not a smithy.APIError")) {
		t.Error("isThrottling(plain error) = true, want false")
	}
}
