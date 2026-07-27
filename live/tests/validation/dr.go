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

// SourceDRBucketPairs matches each source guide bucket with its DR counterpart BY KIND.
//
// The previous code paired them by array index — guideBuckets[0] against
// drBucketNames[0] — with a comment conceding the orderings were not guaranteed to
// align and calling one pair "representative". They do not align: guideBuckets[0] is the
// image bucket, while dr_s3_bucket_names iterates to "doc" first. So the canary was
// written to the IMAGE source bucket and awaited in the DOC DR bucket, which cannot ever
// replicate. The check could only fail, and did, the first time a run reached it.
//
// Both outputs are keyed by the same kinds (image/obj/pdf/doc), so pair on those and the
// question becomes answerable.
func SourceDRBucketPairs(outs StackOutputs) []BucketPair {
	var pairs []BucketPair
	for _, kind := range []string{"image", "obj", "pdf", "doc"} {
		src, dst := outs.GuideBucketByKind[kind], outs.DRBucketByKind[kind]
		if src != "" && dst != "" {
			pairs = append(pairs, BucketPair{Kind: kind, Source: src, DR: dst})
		}
	}
	return pairs
}

// BucketPair is one source bucket and the DR bucket it replicates into.
type BucketPair struct {
	Kind   string
	Source string
	DR     string
}

// AssertS3ReplicationFlow puts a canary in the source bucket and polls its DR counterpart.
//
// The canary is ALWAYS removed, from both buckets, including on the failure path — and by
// version, not with a plain DeleteObject. Both details are load-bearing on a versioned
// bucket: a leftover canary version leaves the bucket non-empty, the DR buckets are
// created with force_destroy = false, and terraform then cannot delete them:
//
//	deleting S3 Bucket (smoke-full-image-dr-...): api error BucketNotEmpty
//
// which failed teardown and stranded five buckets. A plain DeleteObject would not have
// helped: on a versioned bucket it only adds a delete marker, leaving both the version and
// the marker behind.
func AssertS3ReplicationFlow(ctx context.Context, srcRegion, drRegion, srcBucket, drBucket, runID string) error {
	src, err := s3client(ctx, srcRegion)
	if err != nil {
		return err
	}
	dst, err := s3client(ctx, drRegion)
	if err != nil {
		return err
	}
	key := "_harness/dr-canary-" + runID

	defer func() {
		purgeAllVersions(ctx, src, srcBucket, key)
		purgeAllVersions(ctx, dst, drBucket, key)
	}()

	body := strings.NewReader("")
	if _, err := src.PutObject(ctx, &s3.PutObjectInput{Bucket: &srcBucket, Key: &key, Body: body, ServerSideEncryption: s3types.ServerSideEncryptionAwsKms}); err != nil {
		return err
	}

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := dst.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &drBucket, Key: &key}); err == nil {
			return nil
		}
		time.Sleep(20 * time.Second)
	}
	return fmt.Errorf("canary %s not replicated from %s to %s within 10m", key, srcBucket, drBucket)
}

// AssertS3ReplicationFlowAll canaries EVERY source/DR pair, not one "representative" one.
//
// All canaries are written up front and then polled together against a single shared
// deadline, so covering four pairs costs the same wall-clock as covering one. The old code
// checked a single pair specifically because it could not pair them correctly; now that it
// can, there is no reason to leave three of the four replication rules unverified.
func AssertS3ReplicationFlowAll(ctx context.Context, srcRegion, drRegion string, pairs []BucketPair, runID string) error {
	if len(pairs) == 0 {
		return fmt.Errorf("no source/DR bucket pairs could be matched by kind — " +
			"guide bucket and dr_s3_bucket_names outputs do not line up")
	}
	src, err := s3client(ctx, srcRegion)
	if err != nil {
		return err
	}
	dst, err := s3client(ctx, drRegion)
	if err != nil {
		return err
	}
	key := "_harness/dr-canary-" + runID

	defer func() {
		for _, pr := range pairs {
			purgeAllVersions(ctx, src, pr.Source, key)
			purgeAllVersions(ctx, dst, pr.DR, key)
		}
	}()

	for _, pr := range pairs {
		body := strings.NewReader("")
		if _, err := src.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(pr.Source), Key: &key, Body: body,
			ServerSideEncryption: s3types.ServerSideEncryptionAwsKms,
		}); err != nil {
			return fmt.Errorf("put canary in %s (%s): %w", pr.Source, pr.Kind, err)
		}
	}

	pending := append([]BucketPair(nil), pairs...)
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var still []BucketPair
		for _, pr := range pending {
			if _, err := dst.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(pr.DR), Key: &key}); err != nil {
				still = append(still, pr)
			}
		}
		if len(still) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			var names []string
			for _, pr := range still {
				names = append(names, fmt.Sprintf("%s (%s -> %s)", pr.Kind, pr.Source, pr.DR))
			}
			return fmt.Errorf("canary %s not replicated within 10m for: %s", key, strings.Join(names, ", "))
		}
		pending = still
		time.Sleep(20 * time.Second)
	}
}

// purgeAllVersions deletes every version and delete-marker of key. Best-effort: this runs
// in a defer on the cleanup path, and failing to tidy a canary must not mask the real
// result of the check.
func purgeAllVersions(ctx context.Context, c *s3.Client, bucket, key string) {
	out, err := c.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucket), Prefix: aws.String(key),
	})
	if err != nil {
		return
	}
	for _, v := range out.Versions {
		if v.Key != nil && *v.Key == key {
			_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: v.Key, VersionId: v.VersionId})
		}
	}
	for _, m := range out.DeleteMarkers {
		if m.Key != nil && *m.Key == key {
			_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: m.Key, VersionId: m.VersionId})
		}
	}
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
