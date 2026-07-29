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
