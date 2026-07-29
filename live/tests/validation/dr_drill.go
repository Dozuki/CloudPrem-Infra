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
	// Fallback only - see drillClassFor. Provisioned, supported on every aurora-mysql
	// 3.x/8.x family, used when the promoted cluster predates the Serverless v2 scaling
	// config on the DR secondary (CPI #352).
	drillFallbackInstanceClass = "db.r6g.large"
	drillPromoteTimeout        = 10 * time.Minute
	drillInstanceTimeout       = 25 * time.Minute
	drillCleanupTimeout        = 20 * time.Minute
	drillPollEvery             = 20 * time.Second
)

// DrillParams carries everything the drill needs; phase 2 (the data probe) added enough
// inputs that positional arguments stopped being readable.
type DrillParams struct {
	Kubeconfig      string // app cluster access, for the heartbeat writes via the app pod
	Namespace       string
	PrimaryRegion   string // where the primary cluster + its Secrets Manager secret live
	DRRegion        string
	GlobalClusterID string
	DBSecretARN     string // primary_db_secret output; users replicate, so valid on the promoted side
	RunID           string
}

// DrillResult reports what the drill established, for phases that run AFTER it: the
// promoted cluster's identity (no longer discoverable via the global cluster once
// removed) and how many heartbeats were verified on it (the recovery rebuild judges its
// own data survival against this).
type DrillResult struct {
	PromotedClusterID string
	Heartbeats        int
}

// AuroraPromotionDrill proves DR recovery end to end in the PRODUCTION ordering: an
// instance is provisioned on the still-attached secondary (prepare - reversible),
// heartbeat rows are written to the PRIMARY, the secondary is promoted (the ~2s
// irreversible act), and an ephemeral in-VPC Lambda reads the heartbeats back FROM THE
// PROMOTED CLUSTER - data survival, not just mechanics. The instance is then removed and
// the promoted cluster is left for the stack's own teardown.
func AuroraPromotionDrill(ctx context.Context, dp DrillParams) (DrillResult, error) {
	drRegion, globalClusterID, runID := dp.DRRegion, dp.GlobalClusterID, dp.RunID
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return DrillResult{}, err
	}
	rc := rds.NewFromConfig(cfg)

	// PREPARE first, PROMOTE second - the production ordering, executed through the
	// SAME shared functions the cpi-dr CLI runs (dr_failover.go), so the weekly drill
	// rehearses the actual failover code, not a parallel reimplementation. Prepare
	// discovers the secondary (refusing the writer), picks the class off the cluster
	// (db.serverless once CPI #352 is in, provisioned fallback before it), and
	// provisions the instance on the still-attached secondary.
	prep, perr := PrepareDRInstance(ctx, drRegion, globalClusterID, "drill-"+shortRunID(runID))
	if perr != nil && prep.InstanceID == "" {
		return DrillResult{PromotedClusterID: prep.ClusterID}, perr // nothing created; no cleanup owed
	}
	result := DrillResult{PromotedClusterID: prep.ClusterID}
	instanceID := prep.InstanceID

	// From here on the instance may exist (even when prepare errored mid-wait), so
	// cleanup must run even when verification fails - a leftover instance Terraform
	// never knew about makes the whole stack undestroyable. The error (if any) still
	// wins over cleanup's outcome.
	var probeCleanup func() error
	verifyErr := func() error {
		if perr != nil {
			return perr
		}

		// Sentinels immediately BEFORE promotion: a short train of timestamped rows on
		// the primary. Writing them after prepare completes keeps the newest row seconds
		// from the promotion, which is what makes the RPO evidence sharp. The count read
		// back on the primary is the ground truth the probe's post-promotion count is
		// judged against.
		written, herr := WriteDRHeartbeats(dp.Kubeconfig, dp.Namespace, runID, 6, 10*time.Second)
		if herr != nil {
			return fmt.Errorf("dr-drill: heartbeats: %w", herr)
		}
		primaryCount, cerr := PrimaryHeartbeatCount(dp.Kubeconfig, dp.Namespace, runID)
		if cerr != nil {
			return fmt.Errorf("dr-drill: %w", cerr)
		}
		if primaryCount < written {
			return fmt.Errorf("dr-drill: primary reports %d heartbeats, wrote %d - primary-side loss makes the DR comparison meaningless", primaryCount, written)
		}
		// Give storage-level replication a moment to carry the tail of the train before
		// the link is severed. Global Database lag is typically sub-second; this is
		// margin, not a correctness requirement - the comparison below is the real check.
		time.Sleep(15 * time.Second)

		logStep("dr-drill: %d heartbeats on primary; promoting (the drill's deliberate decision - the CLI preflights this, the drill IS the rehearsal)", primaryCount)
		promoted, proerr := PromoteSecondary(ctx, drRegion, globalClusterID, prep.ClusterID, instanceID)
		if proerr != nil {
			return proerr
		}
		logStep("dr-drill: prepared-failover DB RTO (decision→writable) = %s; prepare had taken %s", promoted.Duration, prep.Duration)
		logStep("dr-drill: standalone writer %s up (endpoint %s) — probing data content from inside the DR VPC", prep.ClusterID, promoted.Endpoint)

		user, password, err := DBCredentials(ctx, dp.PrimaryRegion, drRegion, dp.DBSecretARN)
		if err != nil {
			return err
		}
		res, cleanup, perr := ProbePromotedCluster(ctx, drRegion, prep.ClusterID, promoted.Endpoint, user, password, runID)
		probeCleanup = cleanup
		if perr != nil {
			return perr
		}
		if res.Count != primaryCount {
			return fmt.Errorf("dr-drill: DATA LOSS - promoted cluster has %d/%d heartbeats (newest %s)",
				res.Count, primaryCount, res.MaxWroteAt)
		}
		result.Heartbeats = res.Count
		logStep("dr-drill: RECOVERY + DATA VERIFIED — %d/%d heartbeats present on the promoted cluster (newest %s); RPO evidence: nothing written before promotion was lost",
			res.Count, primaryCount, res.MaxWroteAt)
		return nil
	}()

	// The probe's ENI wait (Lambda releases them lazily) and the instance deletion are
	// both slow and entirely independent - run them together so the drill pays for the
	// longer of the two, not the sum.
	probeDone := make(chan error, 1)
	if probeCleanup != nil {
		go func() { probeDone <- probeCleanup() }()
	} else {
		probeDone <- nil
	}

	logStep("dr-drill: cleaning up instance %s", instanceID)
	if _, err := rc.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		SkipFinalSnapshot:    aws.Bool(true),
	}); err != nil {
		return result, combineErrs(verifyErr, fmt.Errorf("dr-drill: DeleteDBInstance %s FAILED - teardown will wedge on it, delete manually: %w", instanceID, err))
	}
	if err := waitInstanceGone(ctx, rc, instanceID, drillCleanupTimeout); err != nil {
		return result, combineErrs(verifyErr, fmt.Errorf("dr-drill: instance %s not deleted in time - teardown may wedge on it: %w", instanceID, err))
	}
	if perr := <-probeDone; perr != nil {
		return result, combineErrs(verifyErr, perr)
	}
	logStep("dr-drill: instance + probe removed; promoted cluster %s left for the stack's own teardown (destroying it post-promotion is part of the test)", prep.ClusterID)
	return result, verifyErr
}

// drillClassFor picks the failover instance class the way a real failover would: the
// promoted cluster's own ServerlessV2ScalingConfiguration decides. Present -> the
// runbook's db.serverless path is exercised; absent (refs predating CPI #352) -> the
// provisioned fallback that predates the fix.
func drillClassFor(ctx context.Context, rc *rds.Client, clusterID string) (string, error) {
	out, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
	if err != nil || len(out.DBClusters) == 0 {
		return "", fmt.Errorf("dr-drill: describe cluster for class selection: %w", err)
	}
	if out.DBClusters[0].ServerlessV2ScalingConfiguration != nil {
		return "db.serverless", nil
	}
	return drillFallbackInstanceClass, nil
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

// waitInstanceWriter waits until the given instance is the cluster's writer AND
// available - the honest "writable" signal after a promotion, since the pre-created
// reader bounces while it transitions to writer.
func waitInstanceWriter(ctx context.Context, rc *rds.Client, clusterID, instanceID string, timeout time.Duration) error {
	return pollUntil(ctx, timeout, func() (bool, string, error) {
		out, err := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterID)})
		if err != nil || len(out.DBClusters) == 0 {
			return false, "", err
		}
		writer := false
		for _, m := range out.DBClusters[0].DBClusterMembers {
			if aws.ToString(m.DBInstanceIdentifier) == instanceID && aws.ToBool(m.IsClusterWriter) {
				writer = true
			}
		}
		if !writer {
			return false, "instance still reader", nil
		}
		inst, err := rc.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instanceID)})
		if err != nil || len(inst.DBInstances) == 0 {
			return false, "", err
		}
		st := aws.ToString(inst.DBInstances[0].DBInstanceStatus)
		return st == "available", "writer " + st, nil
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
