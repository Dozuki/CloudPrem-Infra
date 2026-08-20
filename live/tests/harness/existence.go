package harness

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/databasemigrationservice"
	dmstypes "github.com/aws/aws-sdk-go-v2/service/databasemigrationservice/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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

// Class C: classifyResidual can only reason about a residual's AWS TYPE - it has no way
// to tell a genuinely orphaned resource from stale Resource Groups Tagging API residue
// for a type outside insufficientAloneTypes (janitor.go measured that lag on security
// groups and pod identity associations; nothing says every type is exempt from the same
// lag, only that those two can never be the WHOLE story). This file is the second check:
// for every residual classifyResidual marked Blocking, confirm the live resource the ARN
// names actually still exists before letting that Blocking status stand.
//
// Three-valued, not a boolean, because "the probe could not tell" and "the probe
// confirmed it is gone" must never collapse into the same answer:
//
//	Exists     the resource is really there - stays Blocking.
//	NotFound   AWS's own API says it is gone - demoted, with Why explaining why.
//	Error      AccessDenied, throttling that survived one retry, an unregistered type,
//	           or any other unclassifiable outcome - demoted (fail OPEN, never silently
//	           enforced on an answer the probe does not actually have), AND a note lands
//	           in ResidualReport.Incomplete so a permissions gap or an outage reads as a
//	           visible gap, not as a second universal false-positive class.
type existenceState int

const (
	// existenceError is the zero value on purpose: any verifier that returns early
	// without explicitly reaching Exists or NotFound fails open by default.
	existenceError existenceState = iota
	existenceNotFound
	existenceExists
)

// existenceVerifier probes one live resource. clients is already scoped to the ARN's own
// region (or the caller's default region, for the one region-less type - see
// verifyResidualExistenceWith). arn is the residual's exact live ARN.
type existenceVerifier func(ctx context.Context, clients *verifierAPISet, arn string) (existenceState, string)

// verifierAPISet is the minimal per-region AWS surface the registered verifiers need.
// Interfaces, not concrete SDK clients, so tests fake every method without credentials
// or a live account - see existence_test.go's fakeVerifierAPI, which satisfies all of
// them from one struct of function fields.
type verifierAPISet struct {
	EKS  EKSExistenceAPI
	RDS  RDSExistenceAPI
	EC2  EC2ExistenceAPI
	IAM  IAMExistenceAPI
	S3   S3ExistenceAPI
	Logs LogsExistenceAPI
	DMS  DMSExistenceAPI
	SM   SecretsManagerExistenceAPI
	SQS  SQSExistenceAPI
}

type EKSExistenceAPI interface {
	DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	DescribeAddon(context.Context, *eks.DescribeAddonInput, ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
}

type RDSExistenceAPI interface {
	DescribeDBClusters(context.Context, *rds.DescribeDBClustersInput, ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBParameterGroups(context.Context, *rds.DescribeDBParameterGroupsInput, ...func(*rds.Options)) (*rds.DescribeDBParameterGroupsOutput, error)
	DescribeDBClusterParameterGroups(context.Context, *rds.DescribeDBClusterParameterGroupsInput, ...func(*rds.Options)) (*rds.DescribeDBClusterParameterGroupsOutput, error)
	DescribeDBSubnetGroups(context.Context, *rds.DescribeDBSubnetGroupsInput, ...func(*rds.Options)) (*rds.DescribeDBSubnetGroupsOutput, error)
	DescribeGlobalClusters(context.Context, *rds.DescribeGlobalClustersInput, ...func(*rds.Options)) (*rds.DescribeGlobalClustersOutput, error)
}

type EC2ExistenceAPI interface {
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeRouteTables(context.Context, *ec2.DescribeRouteTablesInput, ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeNetworkAcls(context.Context, *ec2.DescribeNetworkAclsInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkAclsOutput, error)
	DescribeInternetGateways(context.Context, *ec2.DescribeInternetGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeInternetGatewaysOutput, error)
	DescribeNatGateways(context.Context, *ec2.DescribeNatGatewaysInput, ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
	DescribeAddresses(context.Context, *ec2.DescribeAddressesInput, ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	DescribeVpcEndpoints(context.Context, *ec2.DescribeVpcEndpointsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcEndpointsOutput, error)
	DescribeVolumes(context.Context, *ec2.DescribeVolumesInput, ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
}

type IAMExistenceAPI interface {
	GetRole(context.Context, *iam.GetRoleInput, ...func(*iam.Options)) (*iam.GetRoleOutput, error)
	GetPolicy(context.Context, *iam.GetPolicyInput, ...func(*iam.Options)) (*iam.GetPolicyOutput, error)
}

type S3ExistenceAPI interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

type LogsExistenceAPI interface {
	DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
}

type DMSExistenceAPI interface {
	DescribeReplicationInstances(context.Context, *databasemigrationservice.DescribeReplicationInstancesInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationInstancesOutput, error)
	DescribeEndpoints(context.Context, *databasemigrationservice.DescribeEndpointsInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeEndpointsOutput, error)
	DescribeReplicationTasks(context.Context, *databasemigrationservice.DescribeReplicationTasksInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationTasksOutput, error)
	DescribeReplicationSubnetGroups(context.Context, *databasemigrationservice.DescribeReplicationSubnetGroupsInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationSubnetGroupsOutput, error)
	DescribeCertificates(context.Context, *databasemigrationservice.DescribeCertificatesInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeCertificatesOutput, error)
	DescribeReplicationConfigs(context.Context, *databasemigrationservice.DescribeReplicationConfigsInput, ...func(*databasemigrationservice.Options)) (*databasemigrationservice.DescribeReplicationConfigsOutput, error)
}

type SecretsManagerExistenceAPI interface {
	DescribeSecret(context.Context, *secretsmanager.DescribeSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
}

type SQSExistenceAPI interface {
	GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
}

// existenceVerifiers is the registry: one entry per AWS type classifyResidual can mark
// Blocking (terraformManagedTypes minus insufficientAloneTypes).
// TestEveryBlockingTypeHasAnExistenceVerifier enforces that this map never falls behind
// that set - the same completeness pattern as TestEveryManagedTypeExposesAnARN.
var existenceVerifiers = map[string]existenceVerifier{
	"eks:cluster":   verifyEKSCluster,
	"eks:nodegroup": verifyEKSNodegroup,
	"eks:addon":     verifyEKSAddon,

	"rds:cluster":        verifyRDSCluster,
	"rds:db":             verifyRDSInstance,
	"rds:pg":             verifyRDSParameterGroup,
	"rds:cluster-pg":     verifyRDSClusterParameterGroup,
	"rds:subgrp":         verifyRDSSubnetGroup,
	"rds:global-cluster": verifyRDSGlobalCluster,

	"ec2:vpc":              verifyEC2VPC,
	"ec2:subnet":           verifyEC2Subnet,
	"ec2:route-table":      verifyEC2RouteTable,
	"ec2:network-acl":      verifyEC2NetworkACL,
	"ec2:internet-gateway": verifyEC2InternetGateway,
	"ec2:natgateway":       verifyEC2NatGateway,
	"ec2:elastic-ip":       verifyEC2ElasticIP,
	"ec2:vpc-endpoint":     verifyEC2VPCEndpoint,
	"ec2:volume":           verifyEC2Volume,

	"kms:key": unimplementedVerifier("kms"),

	"s3": verifyS3Bucket,

	"iam:role":   verifyIAMRole,
	"iam:policy": verifyIAMPolicy,

	"logs:log-group": verifyLogGroup,

	"elasticloadbalancing:loadbalancer": unimplementedVerifier("elasticloadbalancingv2"),
	"elasticloadbalancing:targetgroup":  unimplementedVerifier("elasticloadbalancingv2"),

	"dms:rep":                verifyDMSReplicationInstance,
	"dms:endpoint":           verifyDMSEndpoint,
	"dms:task":               verifyDMSReplicationTask,
	"dms:subgrp":             verifyDMSSubnetGroup,
	"dms:cert":               verifyDMSCertificate,
	"dms:replication-config": verifyDMSReplicationConfig,

	"kafka:cluster": unimplementedVerifier("kafka"),

	"secretsmanager:secret": verifySecretsManagerSecret,

	"elasticache:replicationgroup":  unimplementedVerifier("elasticache"),
	"elasticfilesystem:file-system": unimplementedVerifier("efs"),
	"es:domain":                     unimplementedVerifier("opensearchservice"),

	"sqs": verifySQSQueue,
	"sns": unimplementedVerifier("sns"),
}

// unimplementedVerifier documents a REAL gap rather than papering over it. pkg names
// the AWS SDK service package (github.com/aws/aws-sdk-go-v2/service/<pkg>) this type's
// existence check needs, which live/tests/go.mod does not vendor today (kms, elbv2,
// kafka, elasticache, efs, opensearchservice and sns are all absent - checked against
// go.mod directly). Adding one means editing go.mod/go.sum, which sits outside
// live/tests/harness/ and this change's scope. Registering it here as an honest
// "cannot verify" - rather than leaving the type out of existenceVerifiers entirely -
// keeps TestEveryBlockingTypeHasAnExistenceVerifier meaningful (a real gap is still
// visible, in the Incomplete note, instead of masquerading as untested) and keeps a
// residual of one of these types from ever being silently enforced on with no probe.
// Follow-up: vendor the package and replace the entry with a real Describe call.
func unimplementedVerifier(pkg string) existenceVerifier {
	return func(_ context.Context, _ *verifierAPISet, _ string) (existenceState, string) {
		return existenceError, fmt.Sprintf("no existence probe wired for this type (needs github.com/aws/aws-sdk-go-v2/service/%s in go.mod)", pkg)
	}
}

// --- ARN parsing helpers -----------------------------------------------------------
//
// arnResourceID (janitor.go) covers most shapes here (it is reused directly below), but
// three shapes need something it does not provide: a resource part with NO separator at
// all (S3, SQS - arnResourceID returns "" for those, by design), a compound EKS resource
// part with more than one segment (nodegroup/addon), and an IAM path that needs BOTH
// the "/" split arnResourceID already does AND a second split to drop the path prefix.

// arnResourcePart returns an ARN's raw resource segment (the 6th colon field)
// unmodified - "bucket-name", "queue-name", "cluster/name/uuid" - for callers that need
// to do their own splitting rather than arnResourceID's single first-separator cut.
func arnResourcePart(arnStr string) string {
	parts := strings.SplitN(arnStr, ":", 6)
	if len(parts) < 6 {
		return ""
	}
	return parts[5]
}

// eksCompoundParts splits a nodegroup or addon ARN's resource part
// ("cluster-name/nodegroup-name/uuid") into the cluster name and the nodegroup/addon
// name DescribeNodegroup and DescribeAddon each require as two separate parameters.
func eksCompoundParts(arnStr string) (cluster, name string, ok bool) {
	tail := arnResourceID(arnStr)
	parts := strings.SplitN(tail, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// iamNameFromARN drops both the "role/" or "policy/" prefix (arnResourceID) and any
// path segment beyond it (IAM's GetRole/GetPolicy take the bare name; a role created
// with a path is otherwise addressed by "path/name", which GetRoleInput rejects).
func iamNameFromARN(arnStr string) string {
	tail := arnResourceID(arnStr)
	if i := strings.LastIndex(tail, "/"); i >= 0 {
		return tail[i+1:]
	}
	return tail
}

// s3BucketFromARN returns the bucket name from an S3 ARN, dropping any object-key
// suffix ("arn:aws:s3:::bucket/key" -> "bucket"). S3 bucket ARNs have no account or
// region field and no "/" or ":" separator ahead of the bucket name itself, so
// arnResourceID (which looks for one) returns "" for these - this is the fallback
// arnIsAccountedFor already uses for the identical shape.
func s3BucketFromARN(arnStr string) string {
	part := arnResourcePart(arnStr)
	if i := strings.Index(part, "/"); i >= 0 {
		return part[:i]
	}
	return part
}

// sqsQueueNameFromARN mirrors s3BucketFromARN: an SQS queue ARN's resource part is the
// bare queue name with no separator, so arnResourceID returns "".
func sqsQueueNameFromARN(arnStr string) string {
	return arnResourcePart(arnStr)
}

// --- retry + error classification ---------------------------------------------------

// callWithOneRetry retries exactly once, after a short fixed backoff, and ONLY for a
// throttling error - the one class of AWS error a retry can actually fix. AccessDenied
// and everything else is not something waiting half a second changes, and retrying
// those would just slow down every genuinely-blocked probe for no benefit.
func callWithOneRetry(ctx context.Context, fn func() error) error {
	err := fn()
	if err == nil || !isThrottling(err) {
		return err
	}
	select {
	case <-ctx.Done():
		return err
	case <-time.After(250 * time.Millisecond):
	}
	return fn()
}

func isThrottling(err error) bool {
	var ae smithy.APIError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.ErrorCode() {
	case "Throttling", "ThrottlingException", "TooManyRequestsException", "RequestLimitExceeded", "SlowDown", "RequestThrottled", "ThrottledException":
		return true
	}
	return false
}

// classifyByErrorCode is the EC2-style existence check: EC2 uses the ec2query protocol,
// which reports "not found" as a smithy.APIError CODE STRING ("InvalidVpcID.NotFound")
// rather than a modeled Go error type the way RDS/EKS/IAM/SQS's JSON protocols do.
// notFoundCodes lists every code that means "gone" for the caller's resource type.
//
// outEmpty is the caller's answer to "did the response actually contain the resource I
// filtered for?" on the err == nil path. Every caller here filters by an explicit id
// list (VpcIds, SubnetIds, ...), and the documented EC2 contract for that shape is: a
// missing id comes back as one of notFoundCodes, never as a 200 with an empty list. So
// an err == nil response with outEmpty true never happens on a healthy id-filtered
// call, and reading it as existenceExists would be confirming a resource the response
// never actually contained - that gets the same existenceError anomaly treatment as
// the nil-response cases elsewhere in this file, not a silent existenceExists.
func classifyByErrorCode(err error, outEmpty bool, notFoundCodes ...string) (existenceState, string) {
	if err == nil {
		if outEmpty {
			return existenceError, "no matching resource in the response despite no error (anomalous SDK response for an id-filtered call)"
		}
		return existenceExists, ""
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		for _, code := range notFoundCodes {
			if ae.ErrorCode() == code {
				return existenceNotFound, ae.ErrorCode()
			}
		}
		return existenceError, ae.ErrorCode()
	}
	return existenceError, err.Error()
}

// --- EKS ------------------------------------------------------------------------

func verifyEKSCluster(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	name := arnResourceID(arnStr)
	if name == "" {
		return existenceError, "could not extract cluster name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
		return e
	})
	return classifyEKSNotFound(err)
}

func verifyEKSNodegroup(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	cluster, name, ok := eksCompoundParts(arnStr)
	if !ok {
		return existenceError, "could not extract cluster/nodegroup name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.EKS.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(cluster), NodegroupName: aws.String(name)})
		return e
	})
	return classifyEKSNotFound(err)
}

func verifyEKSAddon(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	cluster, name, ok := eksCompoundParts(arnStr)
	if !ok {
		return existenceError, "could not extract cluster/addon name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.EKS.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(cluster), AddonName: aws.String(name)})
		return e
	})
	return classifyEKSNotFound(err)
}

func classifyEKSNotFound(err error) (existenceState, string) {
	if err == nil {
		return existenceExists, ""
	}
	var nf *ekstypes.ResourceNotFoundException
	if errors.As(err, &nf) {
		return existenceNotFound, "ResourceNotFoundException"
	}
	return existenceError, err.Error()
}

// --- RDS ------------------------------------------------------------------------
//
// DBClusterIdentifier (and every sibling *Identifier field below) accepts either the
// bare identifier or a full ARN, so these pass the bare identifier extracted by
// arnResourceID rather than the ARN itself - EXCEPT verifyRDSCluster, which has to
// handle a second ARN shape DBClusterIdentifier cannot take at all: see its own
// comment below.

// dbClusterResourceIDPattern recognizes Class A's secondary Aurora ARN tail
// (residuals.go): the Resource Groups Tagging API returns Aurora clusters under a
// SECOND ARN whose resource id is the cluster's DbClusterResourceId - AWS's own fixed
// internal format, "cluster-" followed by exactly 26 alphanumeric characters with no
// further hyphens. A user-chosen DBClusterIdentifier is never this exact shape (AWS
// cluster identifiers commonly contain hyphens throughout, e.g. "custx-bi-writer"), so
// this is a safe, explicit discriminator rather than a guess.
//
// Written lowercase and matched case-insensitively (looksLikeDBClusterResourceID lowers
// the input before testing, without mutating the caller's copy): the real
// DbClusterResourceId is UPPERCASE on the wire ("cluster-CO3QCVN73C42V4NVZBEDVLXPZU"),
// residuals.go's state index lower-cases its OWN copy for a consistent lookup key, but
// nothing upstream of this call guarantees what case the ARN this probe receives
// carries. A lowercase-only pattern against an uppercase tail misses the discriminator
// entirely, falls through to the DBClusterIdentifier branch below, which always raises
// DBClusterNotFoundFault for a resource-id-shaped value - silently restoring the exact
// bug this file exists to fix, just for the untested case.
var dbClusterResourceIDPattern = regexp.MustCompile(`^cluster-[0-9a-z]{26}$`)

func looksLikeDBClusterResourceID(id string) bool {
	return dbClusterResourceIDPattern.MatchString(strings.ToLower(id))
}

// dbClusterResourceIDLength is "cluster-" plus the fixed 26-character suffix
// dbClusterResourceIDPattern requires - guaranteed by looksLikeDBClusterResourceID
// having already matched before this is ever called.
const dbClusterResourceIDLength = len("cluster-") + 26

// canonicalDBClusterResourceID returns id in the case AWS actually stores
// DbClusterResourceId in: a lowercase "cluster-" literal followed by an uppercase
// 26-character suffix (verified against a live value, "cluster-CO3QCVN73C42V4NVZBEDVLXPZU").
// The caller (verifyRDSClusterByResourceID) must only reach this after
// looksLikeDBClusterResourceID has already confirmed id matches that shape
// case-insensitively; canonicalDBClusterResourceID does not re-validate it, it only
// re-cases it, so a plain strings.ToUpper(id) would be wrong here - that would also
// uppercase the "cluster-" literal, which is never capitalized on the wire.
func canonicalDBClusterResourceID(id string) string {
	if len(id) != dbClusterResourceIDLength {
		// Defensive only: looksLikeDBClusterResourceID's length check already rules
		// this out for every real caller.
		return strings.ToUpper(id)
	}
	return "cluster-" + strings.ToUpper(id[len("cluster-"):])
}

// verifyRDSCluster has to pick between two different DescribeDBClusters query shapes
// depending on which ARN it was handed:
//
//   - The PRIMARY ARN's tail is a real DBClusterIdentifier, which the identifier
//     field accepts directly and which raises DBClusterNotFoundFault (never a 200
//     with an empty list) when it does not exist - a true get-by-identifier call.
//   - Class A's SECONDARY ARN tail (residuals.go) is a DbClusterResourceId, which
//     DescribeDBClusters does NOT accept as DBClusterIdentifier - AWS silently
//     evaluates it as a literal identifier that can never match anything and raises
//     DBClusterNotFoundFault regardless of whether the cluster is actually live.
//     Passing this ARN's tail through arnResourceID as-is therefore always reports
//     "confirmed gone", even for a healthy cluster (mitigated today because
//     reconcileTagged also emits a separate residual for the primary ARN, which IS
//     probed correctly - see the finding writeup - but this call itself is still
//     wrong for that ARN and would misreport a genuine orphan of a cluster that
//     ONLY ever surfaces via its secondary ARN).
//     The fix: query by the documented db-cluster-resource-id Filter instead, which
//     IS a genuine filtered-list call - a legitimate "no match" comes back as a 200
//     with an empty DBClusters slice, not an error.
func verifyRDSCluster(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract cluster identifier from arn"
	}
	if looksLikeDBClusterResourceID(id) {
		return verifyRDSClusterByResourceID(ctx, c, id)
	}
	return verifyRDSClusterByIdentifier(ctx, c, id)
}

// verifyRDSClusterByIdentifier is the get-by-identifier call: a missing identifier
// always raises DBClusterNotFoundFault, so an empty success on this path never happens
// on a healthy call and is an anomaly, not a legitimate absence.
func verifyRDSClusterByIdentifier(ctx context.Context, c *verifierAPISet, id string) (existenceState, string) {
	var out *rds.DescribeDBClustersOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.RDS.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(id)})
		out, err = o, e
		return e
	})
	if err != nil {
		var nf *rdstypes.DBClusterNotFoundFault
		if errors.As(err, &nf) {
			return existenceNotFound, "DBClusterNotFoundFault"
		}
		return existenceError, err.Error()
	}
	if out == nil || len(out.DBClusters) == 0 {
		return existenceError, "no db cluster in response despite no error (anomalous SDK response)"
	}
	return existenceExists, ""
}

// verifyRDSClusterByResourceID handles Class A's secondary ARN. Two things this call
// cannot take on faith, both hardened here:
//
//   - Case. DbClusterResourceId's documented shape is fixed-case uppercase, but
//     nothing upstream of this call guarantees the ARN tail it was handed carries
//     that case (residuals.go's state index lower-cases its own lookup copy; this ARN
//     is not that copy). Hand AWS the value in the case it actually stores rather
//     than trusting the caller's case verbatim.
//   - The filter's match semantics. Nothing in this diff has a doc reference proving
//     db-cluster-resource-id matches case-insensitively (or at all, in every
//     partition). So an empty filter result alone is not proof of absence: retry once
//     via DBClusterIdentifier before concluding anything. That retry is also exactly
//     what resolves the opposite edge - a legal user-chosen cluster identifier that
//     happens to satisfy the resource-id shape (e.g. "cluster-abcdefghijklmnopqrstuvwxyz")
//     will never match the resource-id filter (it isn't one), but IS a real
//     DBClusterIdentifier, so the fallback finds it.
//
// If the fallback doesn't come back existenceExists, this returns existenceError, never
// existenceNotFound: a resource-id-shaped identifier passed through DBClusterIdentifier
// always raises DBClusterNotFoundFault regardless of whether the cluster is live (see
// verifyRDSClusterByIdentifier's own doc), so a NotFound from the fallback is exactly as
// uninformative here as the filter's own empty result - neither path has demonstrated
// match semantics strong enough to justify a definite "confirmed gone".
func verifyRDSClusterByResourceID(ctx context.Context, c *verifierAPISet, id string) (existenceState, string) {
	filterState, filterNote := rdsClusterByResourceIDFilter(ctx, c, canonicalDBClusterResourceID(id))
	if filterState != existenceNotFound {
		return filterState, filterNote
	}
	idState, idNote := verifyRDSClusterByIdentifier(ctx, c, id)
	if idState == existenceExists {
		return existenceExists, ""
	}
	return existenceError, fmt.Sprintf(
		"db-cluster-resource-id filter found no match (%s); DBClusterIdentifier fallback did not confirm existence either (%s) - filter match semantics are unconfirmed, so this is not treated as a definite NotFound",
		filterNote, idNote,
	)
}

func rdsClusterByResourceIDFilter(ctx context.Context, c *verifierAPISet, filterValue string) (existenceState, string) {
	var out *rds.DescribeDBClustersOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.RDS.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{
			Filters: []rdstypes.Filter{{Name: aws.String("db-cluster-resource-id"), Values: []string{filterValue}}},
		})
		out, err = o, e
		return e
	})
	if err != nil {
		return existenceError, err.Error()
	}
	// A NIL output with err == nil is an anomalous SDK response, not the documented
	// empty-list shape of "no match" for a filtered-list call - checked FIRST and
	// separately from the empty-slice case, the same anomaly-vs-legitimate-empty split
	// verifyLogGroup already applies. Collapsing the two would let a genuinely live
	// cluster read as confirmed-gone off a response this call never actually got.
	if out == nil {
		return existenceError, "empty response despite no error (anomalous SDK response)"
	}
	if len(out.DBClusters) == 0 {
		// A genuine filtered-list "no match" - see the function comment above.
		return existenceNotFound, "no db cluster matched this db-cluster-resource-id filter"
	}
	return existenceExists, ""
}

func verifyRDSInstance(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract instance identifier from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.RDS.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	var nf *rdstypes.DBInstanceNotFoundFault
	if errors.As(err, &nf) {
		return existenceNotFound, "DBInstanceNotFoundFault"
	}
	return existenceError, err.Error()
}

func verifyRDSParameterGroup(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract parameter group name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.RDS.DescribeDBParameterGroups(ctx, &rds.DescribeDBParameterGroupsInput{DBParameterGroupName: aws.String(id)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	var nf *rdstypes.DBParameterGroupNotFoundFault
	if errors.As(err, &nf) {
		return existenceNotFound, "DBParameterGroupNotFoundFault"
	}
	return existenceError, err.Error()
}

func verifyRDSClusterParameterGroup(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract cluster parameter group name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.RDS.DescribeDBClusterParameterGroups(ctx, &rds.DescribeDBClusterParameterGroupsInput{DBClusterParameterGroupName: aws.String(id)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	var nf *rdstypes.DBClusterParameterGroupNotFoundFault
	if errors.As(err, &nf) {
		return existenceNotFound, "DBClusterParameterGroupNotFoundFault"
	}
	return existenceError, err.Error()
}

func verifyRDSSubnetGroup(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract subnet group name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.RDS.DescribeDBSubnetGroups(ctx, &rds.DescribeDBSubnetGroupsInput{DBSubnetGroupName: aws.String(id)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	var nf *rdstypes.DBSubnetGroupNotFoundFault
	if errors.As(err, &nf) {
		return existenceNotFound, "DBSubnetGroupNotFoundFault"
	}
	return existenceError, err.Error()
}

// verifyRDSGlobalCluster is the one region-less type: a global cluster ARN
// ("arn:aws:rds::account:global-cluster:id") carries no region field, so
// verifyResidualExistenceWith falls back to the caller's default (primary) region to
// build the client - the API itself is a global-scoped read regardless of which
// region's endpoint answers it.
func verifyRDSGlobalCluster(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract global cluster identifier from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.RDS.DescribeGlobalClusters(ctx, &rds.DescribeGlobalClustersInput{GlobalClusterIdentifier: aws.String(id)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	var nf *rdstypes.GlobalClusterNotFoundFault
	if errors.As(err, &nf) {
		return existenceNotFound, "GlobalClusterNotFoundFault"
	}
	return existenceError, err.Error()
}

// --- EC2 ------------------------------------------------------------------------
//
// EC2 has no modeled per-fault Go types (see classifyByErrorCode); every verifier below
// matches the documented error CODE for its resource type. Each one also captures its
// response and passes classifyByErrorCode whether it came back empty: these are all
// id-filtered calls, so on err == nil the response is expected to actually contain the
// resource - an empty one is the same SDK-response anomaly discussed on
// classifyByErrorCode, not a legitimate "not found" (that always comes back as one of
// the notFoundCodes below instead).

func verifyEC2VPC(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeVpcsOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.Vpcs) == 0, "InvalidVpcID.NotFound")
}

func verifyEC2Subnet(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeSubnetsOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.Subnets) == 0, "InvalidSubnetID.NotFound")
}

func verifyEC2RouteTable(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeRouteTablesOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{RouteTableIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.RouteTables) == 0, "InvalidRouteTableID.NotFound")
}

func verifyEC2NetworkACL(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeNetworkAclsOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeNetworkAcls(ctx, &ec2.DescribeNetworkAclsInput{NetworkAclIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.NetworkAcls) == 0, "InvalidNetworkAclID.NotFound")
}

func verifyEC2InternetGateway(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeInternetGatewaysOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.InternetGateways) == 0, "InvalidInternetGatewayID.NotFound")
}

// verifyEC2NatGateway needs a second check beyond the error code: DescribeNatGateways
// returns a normal 200 for a nat gateway that has been deleted, with State == "deleted"
// on the (sole) returned object - unlike every other EC2 type here, "gone" is not
// exclusively an error path. A nil/empty response with err == nil is still the same
// anomaly as every other id-filtered EC2 call (see classifyByErrorCode): a missing id
// always raises NatGatewayNotFound instead, so it gets existenceError here too, not a
// silent existenceNotFound - reporting "confirmed gone" for a response that never
// actually described the nat gateway would hide a real leak behind a made-up answer.
func verifyEC2NatGateway(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeNatGatewaysOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{id}})
		out, err = o, e
		return e
	})
	if err != nil {
		return classifyByErrorCode(err, false, "NatGatewayNotFound")
	}
	if out == nil || len(out.NatGateways) == 0 {
		return existenceError, "no nat gateway in response despite no error (anomalous SDK response)"
	}
	if out.NatGateways[0].State == "deleted" {
		return existenceNotFound, "State=deleted"
	}
	return existenceExists, ""
}

func verifyEC2ElasticIP(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeAddressesOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{AllocationIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.Addresses) == 0, "InvalidAllocationID.NotFound")
}

func verifyEC2VPCEndpoint(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeVpcEndpointsOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeVpcEndpoints(ctx, &ec2.DescribeVpcEndpointsInput{VpcEndpointIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.VpcEndpoints) == 0, "InvalidVpcEndpointId.NotFound")
}

func verifyEC2Volume(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	var out *ec2.DescribeVolumesOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.EC2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
		out, err = o, e
		return e
	})
	return classifyByErrorCode(err, out == nil || len(out.Volumes) == 0, "InvalidVolume.NotFound")
}

// --- IAM ------------------------------------------------------------------------

func verifyIAMRole(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	name := iamNameFromARN(arnStr)
	if name == "" {
		return existenceError, "could not extract role name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
		return e
	})
	return classifyIAMNotFound(err)
}

func verifyIAMPolicy(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	// GetPolicy takes the ARN directly - no name extraction needed.
	err := callWithOneRetry(ctx, func() error {
		_, e := c.IAM.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arnStr)})
		return e
	})
	return classifyIAMNotFound(err)
}

func classifyIAMNotFound(err error) (existenceState, string) {
	if err == nil {
		return existenceExists, ""
	}
	var nf *iamtypes.NoSuchEntityException
	if errors.As(err, &nf) {
		return existenceNotFound, "NoSuchEntityException"
	}
	return existenceError, err.Error()
}

// --- S3 -------------------------------------------------------------------------

func verifyS3Bucket(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	bucket := s3BucketFromARN(arnStr)
	if bucket == "" {
		return existenceError, "could not extract bucket name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.S3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	// Same two codes manifest_s3.go already treats as "gone" for the identical
	// HEAD-request-has-no-body reason: a 404 comes back as a bare smithy code, not a
	// modeled S3 error type.
	var ae smithy.APIError
	if errors.As(err, &ae) && (ae.ErrorCode() == "NotFound" || ae.ErrorCode() == "NoSuchBucket") {
		return existenceNotFound, ae.ErrorCode()
	}
	return existenceError, err.Error()
}

// --- CloudWatch Logs --------------------------------------------------------------

// maxLogGroupPages bounds the prefix scan (same rule maxIAMListPages and
// maxDMSReplicationConfigPages already apply for the identical reason): a page cap so a
// paginator that never converges reads as "probe failed" rather than hanging the phase.
const maxLogGroupPages = 50

// verifyLogGroup: there is no per-name Describe for a log group, only a prefix list, so
// this treats a genuinely EMPTY page (non-nil, zero LogGroups - the documented shape of
// "nothing matched this prefix") as NotFound, and requires an EXACT name match among
// what the prefix returns across EVERY page - the prefix can legitimately catch other
// log groups that merely start with the same string, and can legitimately span more
// than one page of them. Reading only the first page and calling a miss there
// "confirmed gone" would misreport a real log group sitting on page 2 behind enough
// same-prefix siblings; every other paginating probe in this file degrades to
// existenceError rather than a definite NotFound when it cannot converge, and this call
// now matches that pattern. A NIL output with err == nil is a different thing entirely:
// DescribeLogGroups never legitimately returns that on success, so it is an anomalous
// SDK response, not evidence the log group is gone, and gets existenceError instead -
// collapsing the two would silently confirm-gone a log group this call never actually
// queried.
func verifyLogGroup(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	name := strings.TrimSuffix(arnResourceID(arnStr), ":*")
	if name == "" {
		return existenceError, "could not extract log group name from arn"
	}
	var nextToken *string
	for page := 0; page < maxLogGroupPages; page++ {
		var out *cloudwatchlogs.DescribeLogGroupsOutput
		var err error
		_ = callWithOneRetry(ctx, func() error {
			o, e := c.Logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
				LogGroupNamePrefix: aws.String(name),
				NextToken:          nextToken,
			})
			out, err = o, e
			return e
		})
		if err != nil {
			return existenceError, err.Error()
		}
		if out == nil {
			return existenceError, "empty response despite no error (anomalous SDK response)"
		}
		for _, lg := range out.LogGroups {
			if aws.ToString(lg.LogGroupName) == name {
				return existenceExists, ""
			}
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			return existenceNotFound, "not present in any page for this prefix"
		}
		nextToken = out.NextToken
	}
	return existenceError, "DescribeLogGroups did not converge within the page cap"
}

// --- DMS ------------------------------------------------------------------------
//
// Four of the five modeled Describe calls take an ARN-valued Filter (confirmed against
// the SDK's own field documentation: "Valid filter names: replication-instance-arn",
// "endpoint-arn", "replication-task-arn", "certificate-arn"), so those pass the ARN
// straight through with no extraction. replication-subnet-group has no ARN field in its
// API response at all (janitor.go's arnResourceID comment already notes this), so it
// filters by the bare identifier instead. replication-config's Filters shape is
// undocumented in the SDK comments, so that one lists (bounded, like the IAM policy
// pagination pattern in phases.go) and matches the ARN field in Go rather than trusting
// a guessed filter name.

func verifyDMSReplicationInstance(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	var out *databasemigrationservice.DescribeReplicationInstancesOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.DMS.DescribeReplicationInstances(ctx, &databasemigrationservice.DescribeReplicationInstancesInput{
			Filters: []dmstypes.Filter{{Name: aws.String("replication-instance-arn"), Values: []string{arnStr}}},
		})
		out, err = o, e
		return e
	})
	return classifyDMSListResult(err, out == nil, out != nil && len(out.ReplicationInstances) > 0)
}

func verifyDMSEndpoint(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	var out *databasemigrationservice.DescribeEndpointsOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.DMS.DescribeEndpoints(ctx, &databasemigrationservice.DescribeEndpointsInput{
			Filters: []dmstypes.Filter{{Name: aws.String("endpoint-arn"), Values: []string{arnStr}}},
		})
		out, err = o, e
		return e
	})
	return classifyDMSListResult(err, out == nil, out != nil && len(out.Endpoints) > 0)
}

func verifyDMSReplicationTask(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	var out *databasemigrationservice.DescribeReplicationTasksOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.DMS.DescribeReplicationTasks(ctx, &databasemigrationservice.DescribeReplicationTasksInput{
			Filters: []dmstypes.Filter{{Name: aws.String("replication-task-arn"), Values: []string{arnStr}}},
		})
		out, err = o, e
		return e
	})
	return classifyDMSListResult(err, out == nil, out != nil && len(out.ReplicationTasks) > 0)
}

func verifyDMSCertificate(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	var out *databasemigrationservice.DescribeCertificatesOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.DMS.DescribeCertificates(ctx, &databasemigrationservice.DescribeCertificatesInput{
			Filters: []dmstypes.Filter{{Name: aws.String("certificate-arn"), Values: []string{arnStr}}},
		})
		out, err = o, e
		return e
	})
	return classifyDMSListResult(err, out == nil, out != nil && len(out.Certificates) > 0)
}

func verifyDMSSubnetGroup(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	id := arnResourceID(arnStr)
	if id == "" {
		return existenceError, "could not extract subnet group identifier from arn"
	}
	var out *databasemigrationservice.DescribeReplicationSubnetGroupsOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.DMS.DescribeReplicationSubnetGroups(ctx, &databasemigrationservice.DescribeReplicationSubnetGroupsInput{
			Filters: []dmstypes.Filter{{Name: aws.String("replication-subnet-group-id"), Values: []string{id}}},
		})
		out, err = o, e
		return e
	})
	return classifyDMSListResult(err, out == nil, out != nil && len(out.ReplicationSubnetGroups) > 0)
}

// maxDMSReplicationConfigPages bounds the unfiltered scan (see the type's comment
// above): a page cap so a paginator that never converges reads as "probe failed" rather
// than hanging the phase, same rule maxIAMListPages already applies for the identical
// reason in phases.go.
const maxDMSReplicationConfigPages = 50

func verifyDMSReplicationConfig(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	var marker *string
	for page := 0; page < maxDMSReplicationConfigPages; page++ {
		var out *databasemigrationservice.DescribeReplicationConfigsOutput
		var err error
		_ = callWithOneRetry(ctx, func() error {
			o, e := c.DMS.DescribeReplicationConfigs(ctx, &databasemigrationservice.DescribeReplicationConfigsInput{Marker: marker})
			out, err = o, e
			return e
		})
		if err != nil {
			return classifyDMSListResult(err, false, false)
		}
		if out == nil {
			// An unfiltered list call: a genuine "no more configs" is a non-nil page
			// with an empty (or absent-Marker) slice, handled below by the loop
			// itself. A nil page with err == nil never happens on a healthy call, so
			// it is an anomaly - reporting it as NotFound would silently confirm a
			// replication config gone that this page never actually described.
			return existenceError, "empty page despite no error (anomalous SDK response)"
		}
		for _, rc := range out.ReplicationConfigs {
			if aws.ToString(rc.ReplicationConfigArn) == arnStr {
				return existenceExists, ""
			}
		}
		if out.Marker == nil || aws.ToString(out.Marker) == "" {
			return existenceNotFound, "not present in DescribeReplicationConfigs"
		}
		marker = out.Marker
	}
	return existenceError, "DescribeReplicationConfigs did not converge within the page cap"
}

// classifyDMSListResult turns a filtered Describe call's outcome into the three-valued
// result: a ResourceNotFoundFault (if DMS chooses to raise one for a filter with no
// matches, rather than returning an empty list, which is the more common behavior) maps
// to NotFound the same as an empty list does; any other error is unclassifiable.
//
// outIsNil separates that legitimate empty-list case from a genuinely anomalous
// response: these are all filtered-list calls, and the documented behavior for "no
// match" is a 200 with a non-nil, empty slice (found ends up false, same as always) -
// a NIL output with err == nil is not that; it is an SDK response that never actually
// described anything, and gets existenceError instead of the found-false NotFound path,
// the same anomaly-vs-legitimate-empty distinction applied throughout this file.
func classifyDMSListResult(err error, outIsNil bool, found bool) (existenceState, string) {
	if err == nil {
		if outIsNil {
			return existenceError, "empty response despite no error (anomalous SDK response)"
		}
		if found {
			return existenceExists, ""
		}
		return existenceNotFound, "no match for this filter"
	}
	var nf *dmstypes.ResourceNotFoundFault
	if errors.As(err, &nf) {
		return existenceNotFound, "ResourceNotFoundFault"
	}
	return existenceError, err.Error()
}

// --- Secrets Manager --------------------------------------------------------------

// verifySecretsManagerSecret handles the scheduled-deletion caveat named in the brief:
// a secret already scheduled for deletion (a 7-30 day window, DeleteSecret's default)
// still returns 200 from DescribeSecret with DeletedDate populated. Reading that as
// Exists would false-positive a correct destroy for the length of the whole window.
func verifySecretsManagerSecret(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	var out *secretsmanager.DescribeSecretOutput
	var err error
	_ = callWithOneRetry(ctx, func() error {
		o, e := c.SM.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(arnStr)})
		out, err = o, e
		return e
	})
	if err != nil {
		var nf *smtypes.ResourceNotFoundException
		if errors.As(err, &nf) {
			return existenceNotFound, "ResourceNotFoundException"
		}
		return existenceError, err.Error()
	}
	if out == nil {
		// DescribeSecret never legitimately returns a nil output on err == nil - a
		// live secret and a scheduled-deletion secret are both a populated struct
		// (DeletedDate distinguishes them below). Reading this as existenceExists
		// would be confirming a secret the response never actually described, so
		// this gets the same anomaly treatment as everywhere else in this file.
		return existenceError, "empty response despite no error (anomalous SDK response)"
	}
	if out.DeletedDate != nil {
		return existenceNotFound, "already scheduled for deletion"
	}
	return existenceExists, ""
}

// --- SQS ------------------------------------------------------------------------

func verifySQSQueue(ctx context.Context, c *verifierAPISet, arnStr string) (existenceState, string) {
	name := sqsQueueNameFromARN(arnStr)
	if name == "" {
		return existenceError, "could not extract queue name from arn"
	}
	err := callWithOneRetry(ctx, func() error {
		_, e := c.SQS.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
		return e
	})
	if err == nil {
		return existenceExists, ""
	}
	var nf *sqstypes.QueueDoesNotExist
	if errors.As(err, &nf) {
		return existenceNotFound, "QueueDoesNotExist"
	}
	return existenceError, err.Error()
}

// --- orchestration ----------------------------------------------------------------

// regionClientFactory returns the verifierAPISet for one region, building and caching
// real clients lazily in production (newRegionClientFactory) or returning fakes in
// tests. Region-awareness is mandatory here, not optional: residuals span the primary
// and DR region (checkResiduals queries both - see PhaseParams.residualRegions), and a
// client built for the wrong region silently NotFounds a real DR-region resource,
// which fails OPEN on an actual leak instead of catching it. That is worse than doing
// nothing, because it looks like verification happened.
type regionClientFactory func(region string) (*verifierAPISet, error)

// newVerifierClients is a package var, not a PhaseParams field, so tests can substitute
// a fake factory (existence_test.go) without widening PhaseParams (defined in
// phases.go, outside this change's scope) or adding a new field to every production
// call site. Tests that override this MUST restore it (t.Cleanup) so they never leak a
// fake factory into a test that runs after them in the same process.
var newVerifierClients = newRegionClientFactory

// newRegionClientFactory builds real AWS clients lazily, one verifierAPISet per
// distinct region, memoized so a batch of residuals spanning the primary and DR region
// builds each client set exactly once. Uses the same awsConfigFor helper (phases.go)
// production's other region-scoped clients (reclaimOutOfStateResourcesForRegion) use,
// so this honors --profile identically instead of silently falling back to the ambient
// default credentials.
func newRegionClientFactory(ctx context.Context, profile string) regionClientFactory {
	cache := map[string]*verifierAPISet{}
	return func(region string) (*verifierAPISet, error) {
		if region == "" {
			return nil, fmt.Errorf("no region to build a client for")
		}
		if cs, ok := cache[region]; ok {
			return cs, nil
		}
		cfg, err := awsConfigFor(ctx, profile, region)
		if err != nil {
			return nil, err
		}
		cs := &verifierAPISet{
			EKS:  eks.NewFromConfig(cfg),
			RDS:  rds.NewFromConfig(cfg),
			EC2:  ec2.NewFromConfig(cfg),
			IAM:  iam.NewFromConfig(cfg),
			S3:   s3.NewFromConfig(cfg),
			Logs: cloudwatchlogs.NewFromConfig(cfg),
			DMS:  databasemigrationservice.NewFromConfig(cfg),
			SM:   secretsmanager.NewFromConfig(cfg),
			SQS:  sqs.NewFromConfig(cfg),
		}
		cache[region] = cs
		return cs, nil
	}
}

// verifyResidualExistence is checkResiduals's Signal 4 (residuals.go): for every
// residual classifyResidual marked Blocking, confirm the target still exists before
// letting that status stand. Production entry point; verifyResidualExistenceWith below
// does the actual work and is what tests call directly with a fake factory.
func (p PhaseParams) verifyResidualExistence(ctx context.Context, rep *ResidualReport) {
	verifyResidualExistenceWith(ctx, rep, p.Region, newVerifierClients(ctx, p.Profile))
}

// verifyResidualExistenceWith mutates rep in place: any Blocking, ARN-bearing residual
// whose probe returns NotFound or Error is demoted, and every Error outcome (including
// "no verifier registered", which production can reach even though the completeness
// test below guards against it at review time) also lands an Incomplete note so a
// probe gap is never invisible in the report the way the false positives it replaces
// were.
//
// defaultRegion is used only when a residual's own ARN carries no region (rds:global-
// cluster is the one type here that has none) - never as a substitute for a real
// region an ARN does carry, which is exactly the mistake that would silently NotFound
// every DR-region orphan.
func verifyResidualExistenceWith(ctx context.Context, rep *ResidualReport, defaultRegion string, clientsFor regionClientFactory) {
	if rep == nil {
		return
	}
	for i := range rep.Residuals {
		res := &rep.Residuals[i]
		if !res.Blocking || res.ARN == "" {
			// Only a would-be-blocking, ARN-bearing (tag-reconcile) hit has a live
			// resource to probe. A state-remnant hit names a terraform ADDRESS, not
			// an ARN, and an already-non-blocking hit needs no further check.
			continue
		}
		_, awsType := arnResourceType(res.ARN)
		verifier, ok := existenceVerifiers[awsType]
		if !ok {
			rep.Incomplete = append(rep.Incomplete, fmt.Sprintf("no existence verifier registered for %s (%s)", awsType, res.ARN))
			res.Blocking = false
			res.Why = "no existence verifier registered for this type; see Incomplete"
			continue
		}
		region := arnRegion(res.ARN)
		if region == "" {
			region = defaultRegion
		}
		clients, err := clientsFor(region)
		if err != nil {
			rep.Incomplete = append(rep.Incomplete, fmt.Sprintf("existence probe for %s %s: could not build a client for region %q: %v", awsType, res.ARN, region, err))
			res.Blocking = false
			res.Why = "existence probe could not run (client build failed); see Incomplete"
			continue
		}
		state, note := verifier(ctx, clients, res.ARN)
		switch state {
		case existenceExists:
			// Stays Blocking; Why stays "" (classifyResidual never set one for a
			// Blocking hit).
		case existenceNotFound:
			res.Blocking = false
			res.Why = "existence probe confirmed the resource is gone: " + note
		default: // existenceError, including an explicitly unimplemented verifier
			res.Blocking = false
			res.Why = "existence probe could not confirm (failing open): " + note
			rep.Incomplete = append(rep.Incomplete, fmt.Sprintf("existence probe for %s %s: %s", awsType, res.ARN, note))
		}
	}
}
