package validation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// Aurora promotion drill: prove DR RECOVERY works, not just that the artifact exists.
//
// Aurora Global Database recovery is a promotion, not a restore - the runbook removes the
// headless DR secondary from the global cluster (making it a standalone writer-capable
// cluster) and provisions an instance on it. None of that path was ever exercised: the
// existence check verifies a member in the DR region and stops. Everything after -
// whether RemoveFromGlobalCluster succeeds, whether the promoted cluster reaches
// available, whether an instance of the chosen class can actually be created on this
// engine version, whether the wreckage can be destroyed afterwards - was taken on faith.
// This drill runs it for real on the ephemeral stack, where breaking the global
// relationship is free.
//
// What it deliberately does NOT prove: data. The DR VPC is air-gapped by design (private
// subnets, no NAT/IGW, a security group with no ingress until failover), so nothing the
// harness can reach can run a query against the promoted cluster. A sentinel-row read via
// an ephemeral in-VPC Lambda is the phase-2 follow-up; until then the drill logs that
// limitation rather than implying more than it verified.
//
// Sequencing: the drill runs LAST among validators, immediately before teardown, because
// promotion is one-way - afterwards the stack has no DR secondary, so any check that
// expects one (DR existence) must already have run. Teardown after the drill is itself
// part of the test: the promoted cluster has drifted from Terraform's view of it
// (global_cluster_identifier gone), and the destroy must cope. The drill deletes the
// instance it created - an instance Terraform has never heard of would otherwise wedge
// the cluster's destroy with "Cluster cannot be deleted, it still has instances".

const (
	drillInstanceClass   = "db.r6g.large" // provisioned, supported on every aurora-mysql 3.x/8.x family
	drillPromoteTimeout  = 10 * time.Minute
	drillInstanceTimeout = 25 * time.Minute
	drillCleanupTimeout  = 20 * time.Minute
	drillPollEvery       = 20 * time.Second
)

// AuroraPromotionDrill promotes the DR secondary, provisions an instance, verifies the
// cluster reaches available as a standalone writer, then removes the instance so the
// stack's own teardown can destroy the promoted cluster.
func AuroraPromotionDrill(ctx context.Context, drRegion, globalClusterID, runID string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return err
	}
	rc := rds.NewFromConfig(cfg)

	clusterID, clusterArn, err := drSecondaryCluster(ctx, rc, drRegion, globalClusterID)
	if err != nil {
		return err
	}
	logStep("dr-drill: promoting %s out of global cluster %s", clusterID, globalClusterID)

	started := time.Now()
	if _, err := rc.RemoveFromGlobalCluster(ctx, &rds.RemoveFromGlobalClusterInput{
		GlobalClusterIdentifier: aws.String(globalClusterID),
		DbClusterIdentifier:     aws.String(clusterArn),
	}); err != nil {
		return fmt.Errorf("dr-drill: RemoveFromGlobalCluster: %w", err)
	}
	if err := waitClusterAvailable(ctx, rc, clusterID, drillPromoteTimeout); err != nil {
		return fmt.Errorf("dr-drill: promoted cluster never became available: %w", err)
	}
	promoteTook := time.Since(started).Round(time.Second)
	logStep("dr-drill: promotion complete in %s — %s is standalone", promoteTook, clusterID)

	// A promoted cluster with no instances is storage, not a database. Provisioning the
	// instance is the step that proves recovery produces something a repointed app could
	// use - and it is where an engine-version/instance-class incompatibility would
	// surface, which no amount of metadata inspection can rule out.
	instanceID := fmt.Sprintf("%s-drill-%s", clusterID, shortRunID(runID))
	logStep("dr-drill: creating %s instance %s (this is the slow part, ~10m)", drillInstanceClass, instanceID)
	instStart := time.Now()
	if _, err := rc.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBClusterIdentifier:  aws.String(clusterID),
		DBInstanceClass:      aws.String(drillInstanceClass),
		Engine:               aws.String("aurora-mysql"),
	}); err != nil {
		return fmt.Errorf("dr-drill: CreateDBInstance: %w", err)
	}

	// From here on the instance exists, so cleanup must run even when verification
	// fails - a leftover instance Terraform never knew about makes the whole stack
	// undestroyable. The error (if any) still wins over cleanup's outcome.
	verifyErr := func() error {
		if err := waitInstanceAvailable(ctx, rc, instanceID, drillInstanceTimeout); err != nil {
			return fmt.Errorf("dr-drill: instance never became available: %w", err)
		}
		logStep("dr-drill: instance available in %s", time.Since(instStart).Round(time.Second))

		out, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
		if err != nil || len(out.DBClusters) == 0 {
			return fmt.Errorf("dr-drill: describe promoted cluster: %w", err)
		}
		c := out.DBClusters[0]
		if c.GlobalClusterIdentifier != nil && *c.GlobalClusterIdentifier != "" {
			return fmt.Errorf("dr-drill: cluster still reports global membership %q after promotion", *c.GlobalClusterIdentifier)
		}
		if c.Endpoint == nil || *c.Endpoint == "" {
			return fmt.Errorf("dr-drill: promoted cluster has no writer endpoint")
		}
		logStep("dr-drill: RECOVERY VERIFIED — standalone writer %s (endpoint %s). NOT verified: data content (DR VPC is air-gapped; sentinel-read via in-VPC probe is the phase-2 follow-up)",
			clusterID, *c.Endpoint)
		return nil
	}()

	logStep("dr-drill: cleaning up instance %s", instanceID)
	if _, err := rc.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		SkipFinalSnapshot:    aws.Bool(true),
	}); err != nil {
		return combineErrs(verifyErr, fmt.Errorf("dr-drill: DeleteDBInstance %s FAILED - teardown will wedge on it, delete manually: %w", instanceID, err))
	}
	if err := waitInstanceGone(ctx, rc, instanceID, drillCleanupTimeout); err != nil {
		return combineErrs(verifyErr, fmt.Errorf("dr-drill: instance %s not deleted in time - teardown may wedge on it: %w", instanceID, err))
	}
	logStep("dr-drill: instance removed; promoted cluster %s left for the stack's own teardown (destroying it post-promotion is part of the test)", clusterID)
	return verifyErr
}

// drSecondaryCluster finds the global cluster's member in the DR region. Done via the
// membership list rather than a naming convention so a renamed cluster cannot silently
// point the drill at the wrong thing.
func drSecondaryCluster(ctx context.Context, rc *rds.Client, drRegion, globalClusterID string) (id, arn string, err error) {
	out, err := rc.DescribeGlobalClusters(ctx, &rds.DescribeGlobalClustersInput{
		GlobalClusterIdentifier: aws.String(globalClusterID),
	})
	if err != nil {
		return "", "", fmt.Errorf("dr-drill: describe global cluster %s: %w", globalClusterID, err)
	}
	for _, g := range out.GlobalClusters {
		for _, m := range g.GlobalClusterMembers {
			if m.DBClusterArn == nil || !strings.Contains(*m.DBClusterArn, ":"+drRegion+":") {
				continue
			}
			if m.IsWriter != nil && *m.IsWriter {
				return "", "", fmt.Errorf("dr-drill: the %s member %s is the WRITER - refusing to promote the primary", drRegion, *m.DBClusterArn)
			}
			return clusterIDFromArn(*m.DBClusterArn), *m.DBClusterArn, nil
		}
	}
	return "", "", fmt.Errorf("dr-drill: global cluster %s has no member in %s", globalClusterID, drRegion)
}

// clusterIDFromArn extracts the cluster identifier from an RDS cluster ARN
// (arn:aws:rds:region:acct:cluster:the-id -> the-id).
func clusterIDFromArn(arn string) string {
	parts := strings.Split(arn, ":")
	return parts[len(parts)-1]
}

// shortRunID compresses a harness run id into something that fits RDS identifier rules
// alongside the cluster name ("local-1785230883-fresh-full" -> "1785230883").
func shortRunID(runID string) string {
	for _, part := range strings.Split(runID, "-") {
		if len(part) >= 8 && strings.Trim(part, "0123456789") == "" {
			return part
		}
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, strings.ToLower(runID))
	if len(safe) > 12 {
		safe = safe[len(safe)-12:]
	}
	return strings.Trim(safe, "-")
}

func waitClusterAvailable(ctx context.Context, rc *rds.Client, id string, timeout time.Duration) error {
	return pollUntil(ctx, timeout, func() (bool, string, error) {
		out, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(id)})
		if err != nil || len(out.DBClusters) == 0 {
			return false, "", err
		}
		st := aws.ToString(out.DBClusters[0].Status)
		return st == "available", "cluster " + st, nil
	})
}

func waitInstanceAvailable(ctx context.Context, rc *rds.Client, id string, timeout time.Duration) error {
	return pollUntil(ctx, timeout, func() (bool, string, error) {
		out, err := rc.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
		if err != nil || len(out.DBInstances) == 0 {
			return false, "", err
		}
		st := aws.ToString(out.DBInstances[0].DBInstanceStatus)
		if st == "failed" || st == "incompatible-parameters" || st == "incompatible-restore" {
			return false, "", fmt.Errorf("instance reached terminal status %q", st)
		}
		return st == "available", "instance " + st, nil
	})
}

func waitInstanceGone(ctx context.Context, rc *rds.Client, id string, timeout time.Duration) error {
	return pollUntil(ctx, timeout, func() (bool, string, error) {
		out, err := rc.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
		if err != nil {
			if strings.Contains(err.Error(), "DBInstanceNotFound") {
				return true, "", nil
			}
			return false, "", err
		}
		if len(out.DBInstances) == 0 {
			return true, "", nil
		}
		return false, "instance " + aws.ToString(out.DBInstances[0].DBInstanceStatus), nil
	})
}

// pollUntil runs check every drillPollEvery until it reports done, errors, or timeout.
// A status-change is logged so long waits show progress rather than silence.
func pollUntil(ctx context.Context, timeout time.Duration, check func() (bool, string, error)) error {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		done, status, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if status != "" && status != last {
			logStep("dr-drill: waiting (%s)", status)
			last = status
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s (last: %s)", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(drillPollEvery):
		}
	}
}

func combineErrs(a, b error) error {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return fmt.Errorf("%w; additionally: %v", a, b)
}

func logStep(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, ">> [harness %s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
