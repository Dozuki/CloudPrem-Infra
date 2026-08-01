package harness

import "testing"

// The value of a retry rule is entirely in what it refuses to retry. Retrying a real
// AWS rejection turns a clear 47-second failure into the same failure four times over,
// and worse, hides it. Both halves are asserted here.
func TestTransientNetRE(t *testing.T) {
	retry := []string{
		// The exact failure that killed a recovery run's rebuild apply, 2026-07-29.
		`Error: reading IAM roles: operation error IAM: ListRoles, https response error StatusCode: 0, RequestID: , request send failed, Post "https://iam.amazonaws.com/": dial tcp: lookup iam.amazonaws.com: no such host`,
		`Error: read tcp 10.0.0.1:443: connection reset by peer`,
		`Error: Get "https://sts.amazonaws.com": net/http: TLS handshake timeout`,
		`Error: dial tcp 1.2.3.4:443: i/o timeout`,
	}
	for _, s := range retry {
		if !transientNetRE.MatchString(s) {
			t.Errorf("should be retried but did not match:\n%s", s)
		}
	}

	// Real answers from AWS. The request landed and was rejected on its merits, so the
	// run must fail. Two of these are genuine failures from earlier harness cycles.
	noRetry := []string{
		`Error: creating EC2 VPC Endpoint: api error InvalidServiceName: The Vpc Endpoint Service does not exist`,
		`Error: AccessDenied: User is not authorized to perform: iam:CreateRole`,
		`Error: InvalidParameterValue: The parameter DBClusterSnapshotIdentifier is not a valid identifier`,
		`Error: creating RDS Cluster: DBClusterAlreadyExistsFault`,
		`Error: EntityAlreadyExists: Role with name smokesrc-min-flux-source-controller already exists`,
	}
	for _, s := range noRetry {
		if transientNetRE.MatchString(s) {
			t.Errorf("must NOT be retried, this is a real AWS rejection:\n%s", s)
		}
	}
}

// eksKmsPropagationRE's whole job is telling the IAM-propagation race apart from a key
// policy that genuinely denies the cluster role. AWS returns the identical sentence for
// both, so the regex can only be as narrow as that sentence; everything else that can
// come back from CreateCluster or a KMS call must still fail on attempt one.
func TestEksKmsPropagationRE(t *testing.T) {
	retry := []string{
		// The exact failure from the 2026-08-01 matrix (harness-bi-ha-77wqp): the
		// encryption-policy attachment landed at +0s relative to CreateCluster instead of
		// min's +4s, and EKS evaluated the cluster role before its new policy propagated.
		`Error: creating EKS Cluster (bi): operation error EKS: CreateCluster, https response error StatusCode: 400, RequestID: 4b1e2c3d-1234-5678-9abc-def012345678, InvalidParameterException: Access denied to KMS key arn:aws:kms:us-east-1:111122223333:key/00000000-0000-0000-0000-000000000000 due to explicit deny policy or revoked grant`,
	}
	for _, s := range retry {
		if !eksKmsPropagationRE.MatchString(s) {
			t.Errorf("should be retried but did not match:\n%s", s)
		}
	}

	// Real misconfigurations that share a KMS/EKS-authorization flavor but are not this
	// race: a genuinely missing kms:DescribeKey grant, a key policy with no principal, an
	// EKS role actually missing its managed policies, and a key that plain does not exist.
	// None of these say "explicit deny policy or revoked grant", so none should retry.
	noRetry := []string{
		`Error: AccessDeniedException: User is not authorized to perform: kms:DescribeKey on this resource`,
		`Error: MalformedPolicyDocumentException: Policy contains a statement with no principal`,
		`Error: InvalidParameterException: The provided role doesn't have the Amazon EKS Managed Policies associated with it`,
		`Error: KMSAccessDeniedException: The ciphertext refers to a customer master key that does not exist`,
	}
	for _, s := range noRetry {
		if eksKmsPropagationRE.MatchString(s) {
			t.Errorf("must NOT be retried, this is a real misconfiguration:\n%s", s)
		}
	}
}
