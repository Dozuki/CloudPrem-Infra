package harness

import (
	"context"
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
