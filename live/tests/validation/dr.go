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

// AssertDRExistence: DR-region buckets exist + versioned; replicated RDS backup present.
func AssertDRExistence(ctx context.Context, drRegion string, drBuckets []string, sourceDBIdentifier string) error {
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
	ab, err := rc.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{})
	if err != nil {
		return err
	}
	for _, b := range ab.DBInstanceAutomatedBackups {
		if b.DBInstanceIdentifier != nil && *b.DBInstanceIdentifier == sourceDBIdentifier {
			return nil
		}
	}
	return fmt.Errorf("no replicated automated backup for %s in %s", sourceDBIdentifier, drRegion)
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
