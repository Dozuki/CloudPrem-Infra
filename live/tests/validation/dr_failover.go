package validation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// The shared failover executor: PrepareDRInstance and PromoteSecondary are the two
// halves of an Aurora Global Database failover, split exactly where the decision sits.
// Prepare is REVERSIBLE (a reader on the still-attached secondary; delete it and nothing
// happened) and can overlap the incident-diagnosis window. Promote is the ~2s
// IRREVERSIBLE act. Both the weekly promotion drill and the cpi-dr CLI call these same
// functions - that is the design's core property: the tool that runs at 2am is the tool
// the drill rehearses, not a parallel reimplementation that drifts.
//
// Both are idempotent and resumable from observed AWS state: a session dying mid-wait
// re-runs safely (create collisions resume the wait; an already-standalone cluster is
// promotion success, not an error).

// PrepareOutcome reports what prepare did (or resumed).
type PrepareOutcome struct {
	ClusterID  string
	InstanceID string
	Class      string
	Resumed    bool // an instance with this id already existed; its wait was resumed
	Duration   time.Duration
}

// PrepareDRInstance provisions the failover instance on the STILL-ATTACHED DR
// secondary: discovers the secondary from global-cluster membership (refusing the
// writer), picks db.serverless when the cluster carries a Serverless v2 scaling config
// (provisioned fallback otherwise), creates <cluster>-<suffix>, and waits for it to
// become available. On error the outcome still names any instance that now exists, so
// callers with cleanup obligations (the drill) know what to delete.
func PrepareDRInstance(ctx context.Context, drRegion, globalClusterID, instanceSuffix string) (PrepareOutcome, error) {
	var out PrepareOutcome
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return out, err
	}
	rc := rds.NewFromConfig(cfg)

	clusterID, _, err := drSecondaryCluster(ctx, rc, drRegion, globalClusterID)
	if err != nil {
		return out, err
	}
	out.ClusterID = clusterID

	class, err := drillClassFor(ctx, rc, clusterID)
	if err != nil {
		return out, err
	}
	out.Class = class
	out.InstanceID = clusterID + "-" + instanceSuffix

	start := time.Now()
	logStep("dr-failover: PREPARE - creating %s instance %s on the attached secondary (reversible; the promotion decision stays open)", class, out.InstanceID)
	if _, cerr := rc.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(out.InstanceID),
		DBClusterIdentifier:  aws.String(clusterID),
		DBInstanceClass:      aws.String(class),
		Engine:               aws.String("aurora-mysql"),
	}); cerr != nil {
		if !strings.Contains(cerr.Error(), "DBInstanceAlreadyExists") {
			return out, fmt.Errorf("dr-failover: CreateDBInstance (%s): %w", class, cerr)
		}
		out.Resumed = true
		logStep("dr-failover: instance %s already exists - resuming its wait (idempotent re-run)", out.InstanceID)
	}
	if werr := waitInstanceAvailable(ctx, rc, out.InstanceID, drillInstanceTimeout); werr != nil {
		return out, fmt.Errorf("dr-failover: instance never became available: %w", werr)
	}
	out.Duration = time.Since(start).Round(time.Second)
	logStep("dr-failover: prepare complete in %s (%s ready as a reader)", out.Duration, out.InstanceID)
	return out, nil
}

// PromoteOutcome reports the promotion.
type PromoteOutcome struct {
	ClusterID string
	Endpoint  string
	Resumed   bool // the cluster was already standalone (a prior promote finished)
	Duration  time.Duration
}

// PromoteSecondary removes clusterID from the global cluster (the irreversible act) and
// waits until it is a standalone writer: cluster available, the prepared instance
// promoted to writer, no residual global membership, endpoint present. Resumable: if the
// cluster is already standalone (a prior run promoted it), it verifies and succeeds.
//
// It does NOT decide whether promoting is right - callers preflight that (the CLI
// refuses while the primary answers; the drill promotes deliberately).
func PromoteSecondary(ctx context.Context, drRegion, globalClusterID, clusterID, instanceID string) (PromoteOutcome, error) {
	out := PromoteOutcome{ClusterID: clusterID}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return out, err
	}
	rc := rds.NewFromConfig(cfg)

	start := time.Now()
	memberID, memberArn, derr := drSecondaryCluster(ctx, rc, drRegion, globalClusterID)
	switch {
	case derr == nil && memberID != clusterID:
		return out, fmt.Errorf("dr-failover: global cluster's %s member is %s, not %s - refusing to promote a cluster that is not the one prepared", drRegion, memberID, clusterID)
	case derr == nil:
		logStep("dr-failover: PROMOTE - removing %s from global cluster %s", clusterID, globalClusterID)
		if _, rerr := rc.RemoveFromGlobalCluster(ctx, &rds.RemoveFromGlobalClusterInput{
			GlobalClusterIdentifier: aws.String(globalClusterID),
			DbClusterIdentifier:     aws.String(memberArn),
		}); rerr != nil {
			return out, fmt.Errorf("dr-failover: RemoveFromGlobalCluster: %w", rerr)
		}
	case strings.Contains(derr.Error(), "no member in"):
		// A prior promote already severed the membership; verify the standalone state
		// below instead of failing a resumed session.
		out.Resumed = true
		logStep("dr-failover: %s is no longer a member of %s - verifying an earlier promotion instead of repeating it", clusterID, globalClusterID)
	default:
		return out, derr
	}

	if err := waitClusterAvailable(ctx, rc, clusterID, drillPromoteTimeout); err != nil {
		return out, fmt.Errorf("dr-failover: promoted cluster never became available: %w", err)
	}
	// The prepared instance transitions reader -> writer during promotion (it may
	// bounce); writable means the member reports IsClusterWriter AND available.
	if err := waitInstanceWriter(ctx, rc, clusterID, instanceID, drillPromoteTimeout); err != nil {
		return out, fmt.Errorf("dr-failover: instance never became the writer after promotion: %w", err)
	}

	dbc, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	if err != nil || len(dbc.DBClusters) == 0 {
		return out, fmt.Errorf("dr-failover: describe promoted cluster: %w", err)
	}
	c := dbc.DBClusters[0]
	if c.GlobalClusterIdentifier != nil && *c.GlobalClusterIdentifier != "" {
		return out, fmt.Errorf("dr-failover: cluster still reports global membership %q after promotion", *c.GlobalClusterIdentifier)
	}
	if c.Endpoint == nil || *c.Endpoint == "" {
		return out, fmt.Errorf("dr-failover: promoted cluster has no writer endpoint")
	}
	out.Endpoint = *c.Endpoint
	out.Duration = time.Since(start).Round(time.Second)
	logStep("dr-failover: promotion complete in %s - %s is a standalone writer (endpoint %s)", out.Duration, clusterID, out.Endpoint)
	return out, nil
}

// DRSecondaryClusterID resolves the global cluster's member in the DR region (refusing
// the writer) - the CLI's discovery entry point over the same membership-derived lookup
// prepare and the drill use.
func DRSecondaryClusterID(ctx context.Context, drRegion, globalClusterID string) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return "", err
	}
	id, _, err := drSecondaryCluster(ctx, rds.NewFromConfig(cfg), drRegion, globalClusterID)
	return id, err
}

// PrimaryWriterReachable reports whether the global cluster's WRITER member answers the
// RDS control plane as an available cluster - the promote preflight. A reachable,
// healthy primary means promotion is the WRONG tool (a planned managed failover via
// `aws rds failover-global-cluster` keeps replication and loses nothing); the CLI
// refuses in that case, with no override flag - forced promotion against a live primary
// is an audited two-person decision outside this tool. Errors and timeouts report
// unreachable=false with the reason, because "the API cannot tell me" is exactly the
// evidence promotion runs on.
func PrimaryWriterReachable(ctx context.Context, globalClusterID, drRegion string) (reachable bool, detail string) {
	// Membership is read from the DR region - the region we know is alive.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return false, "aws config: " + err.Error()
	}
	gc, err := rds.NewFromConfig(cfg).DescribeGlobalClusters(ctx, &rds.DescribeGlobalClustersInput{
		GlobalClusterIdentifier: aws.String(globalClusterID),
	})
	if err != nil || len(gc.GlobalClusters) == 0 {
		return false, fmt.Sprintf("describe global cluster from %s: %v", drRegion, err)
	}
	var writerArn string
	for _, m := range gc.GlobalClusters[0].GlobalClusterMembers {
		if aws.ToBool(m.IsWriter) {
			writerArn = aws.ToString(m.DBClusterArn)
		}
	}
	if writerArn == "" {
		return false, "global cluster has no writer member"
	}
	parts := strings.Split(writerArn, ":")
	if len(parts) < 6 {
		return false, "unparseable writer ARN " + writerArn
	}
	primaryRegion, writerID := parts[3], parts[len(parts)-1]

	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	pcfg, err := config.LoadDefaultConfig(pctx, config.WithRegion(primaryRegion))
	if err != nil {
		return false, "aws config (primary): " + err.Error()
	}
	dbc, err := rds.NewFromConfig(pcfg).DescribeDBClusters(pctx, &rds.DescribeDBClustersInput{
		DBClusterIdentifier: aws.String(writerID),
	})
	if err != nil {
		return false, fmt.Sprintf("primary %s did not answer: %v", primaryRegion, err)
	}
	if len(dbc.DBClusters) == 0 {
		return false, fmt.Sprintf("primary %s answered but cluster %s is gone", primaryRegion, writerID)
	}
	st := aws.ToString(dbc.DBClusters[0].Status)
	return st == "available", fmt.Sprintf("primary cluster %s in %s reports %q", writerID, primaryRegion, st)
}
