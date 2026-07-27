package validation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// AssertDRExistence: DR-region buckets exist + are versioned, and the database's
// cross-region DR artifact is actually present.
//
// Which artifact that is depends on db_engine, and the check must not assume:
//
//	aurora (the default) — an Aurora Global Database with a headless secondary cluster
//	                       in the DR region. There is no replicated instance backup.
//	rds                  — cross-region automated backup replication.
//
// This check only knew the rds shape, and looked it up by a `db_identifier` physical
// output that does not exist — so it read "", searched for a backup belonging to nothing,
// and failed every aurora run with the empty-identifier message
//
//	no replicated automated backup for  in us-west-2
//
// while the actual Aurora DR (global cluster + headless secondary, both present in the
// outputs) went entirely unverified. Engine is inferred from which output is populated
// rather than threaded through, so a stack that emits neither is a hard failure instead of
// a silent pass — the failure mode that hid this.
func AssertDRExistence(ctx context.Context, drRegion string, drBuckets []string, outs StackOutputs) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return err
	}
	sc := s3.NewFromConfig(cfg)
	for _, b := range drBuckets {
		v, err := sc.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &b})
		if err != nil {
			return fmt.Errorf("DR bucket %s: %w", b, err)
		}
		if v.Status != s3types.BucketVersioningStatusEnabled {
			return fmt.Errorf("DR bucket %s versioning=%s, want Enabled", b, v.Status)
		}
	}

	rc := rds.NewFromConfig(cfg)

	switch {
	case outs.AuroraDRGlobalClusterID != "":
		return assertAuroraGlobalDR(ctx, rc, drRegion, outs)
	case outs.DRBackupReplicationARN != "":
		return assertRDSBackupReplication(ctx, rc, drRegion, outs.DRBackupReplicationARN)
	default:
		return fmt.Errorf("DR is enabled but the stack emitted no DR database artifact "+
			"(aurora_dr_global_cluster_id and dr_rds_backup_replication_arn are both empty) — "+
			"nothing to verify in %s", drRegion)
	}
}

// assertAuroraGlobalDR verifies the global cluster exists and actually has a member in the
// DR region. Existence of the global cluster alone is not enough: adoption stamps the
// PRIMARY into it immediately, so a global cluster with only the primary as a member looks
// healthy while providing no DR at all.
func assertAuroraGlobalDR(ctx context.Context, rc *rds.Client, drRegion string, outs StackOutputs) error {
	id := outs.AuroraDRGlobalClusterID
	out, err := rc.DescribeGlobalClusters(ctx, &rds.DescribeGlobalClustersInput{
		GlobalClusterIdentifier: aws.String(id),
	})
	if err != nil {
		return fmt.Errorf("describe global cluster %s: %w", id, err)
	}
	for _, g := range out.GlobalClusters {
		if g.GlobalClusterIdentifier == nil || *g.GlobalClusterIdentifier != id {
			continue
		}
		for _, m := range g.GlobalClusterMembers {
			if m.DBClusterArn == nil {
				continue
			}
			// Members carry their region in the ARN; a member in drRegion is the
			// secondary. IsWriter is the primary, which lives in the source region.
			if strings.Contains(*m.DBClusterArn, ":"+drRegion+":") {
				return nil
			}
		}
		return fmt.Errorf("Aurora global cluster %s has no member in %s (members: %d) — "+
			"the DR secondary did not join", id, drRegion, len(g.GlobalClusterMembers))
	}
	return fmt.Errorf("Aurora global cluster %s not found", id)
}

// assertRDSBackupReplication verifies a replicated automated backup exists in the DR
// region, matched on the source instance ARN the physical layer reports.
func assertRDSBackupReplication(ctx context.Context, rc *rds.Client, drRegion, sourceARN string) error {
	ab, err := rc.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{})
	if err != nil {
		return err
	}
	for _, b := range ab.DBInstanceAutomatedBackups {
		if b.DBInstanceArn != nil && *b.DBInstanceArn == sourceARN {
			return nil
		}
	}
	return fmt.Errorf("no replicated automated backup for %s in %s", sourceARN, drRegion)
}

// AssertS3ReplicationFlow: put a canary in the source bucket, poll the DR bucket.
func AssertS3ReplicationFlow(ctx context.Context, srcRegion, drRegion, srcBucket, drBucket, runID string) error {
	src, err := s3client(ctx, srcRegion)
	if err != nil {
		return err
	}
	key := "_harness/dr-canary-" + runID
	body := strings.NewReader("")
	if _, err := src.PutObject(ctx, &s3.PutObjectInput{Bucket: &srcBucket, Key: &key, Body: body, ServerSideEncryption: s3types.ServerSideEncryptionAwsKms}); err != nil {
		return err
	}
	dst, err := s3client(ctx, drRegion)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := dst.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &drBucket, Key: &key}); err == nil {
			_, _ = src.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &srcBucket, Key: &key})
			_, _ = dst.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &drBucket, Key: &key})
			return nil
		}
		time.Sleep(20 * time.Second)
	}
	return fmt.Errorf("canary %s not replicated to %s within 10m", key, drBucket)
}

// RestoreDrill (full only): restore the replicated automated backup to a throwaway
// instance in the DR region, wait available, then delete. Proves restorability.
func RestoreDrill(ctx context.Context, drRegion, sourceDBIdentifier, runID string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return err
	}
	rc := rds.NewFromConfig(cfg)
	ab, err := rc.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{})
	if err != nil {
		return err
	}
	var srcARN string
	for _, b := range ab.DBInstanceAutomatedBackups {
		if b.DBInstanceIdentifier != nil && *b.DBInstanceIdentifier == sourceDBIdentifier && b.DBInstanceAutomatedBackupsArn != nil {
			srcARN = *b.DBInstanceAutomatedBackupsArn
		}
	}
	if srcARN == "" {
		return fmt.Errorf("no automated-backup ARN for %s in %s", sourceDBIdentifier, drRegion)
	}
	target := fmt.Sprintf("%s-drilltest-%s", sourceDBIdentifier, runID)
	_, err = rc.RestoreDBInstanceToPointInTime(ctx, &rds.RestoreDBInstanceToPointInTimeInput{
		SourceDBInstanceAutomatedBackupsArn: &srcARN,
		TargetDBInstanceIdentifier:          &target,
		DBInstanceClass:                     aws.String("db.t3.medium"),
		UseLatestRestorableTime:             aws.Bool(true),
		MultiAZ:                             aws.Bool(false),
		PubliclyAccessible:                  aws.Bool(false),
	})
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	// Always delete the throwaway instance.
	defer func() {
		_, _ = rc.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: &target, SkipFinalSnapshot: aws.Bool(true), DeleteAutomatedBackups: aws.Bool(true),
		})
	}()
	w := rds.NewDBInstanceAvailableWaiter(rc)
	if err := w.Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: &target}, 30*time.Minute); err != nil {
		return fmt.Errorf("restored instance not available: %w", err)
	}
	return nil // reaching "available" from the replicated backup proves restorability
}
