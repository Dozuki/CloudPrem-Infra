package validation

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const sentinelKey = "_harness/continuity-sentinel.txt"

func s3client(ctx context.Context, region string) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(cfg), nil
}

// strptr returns a pointer to the given string (used in place of aws.String
// to avoid a package-name collision with the aws-sdk-go-v2/aws import alias
// used in other files in this package).
func strptr(s string) *string { return &s }

// WriteSentinel puts a unique marker object into the bucket (pre-upgrade).
func WriteSentinel(ctx context.Context, region, bucket, runID string) error {
	c, err := s3client(ctx, region)
	if err != nil {
		return err
	}
	body := []byte("harness-continuity:" + runID)
	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: strptr(sentinelKey), Body: bytes.NewReader(body)})
	return err
}

// VerifySentinel confirms the marker survived the upgrade (post-upgrade).
func VerifySentinel(ctx context.Context, region, bucket, runID string) error {
	c, err := s3client(ctx, region)
	if err != nil {
		return err
	}
	out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: strptr(sentinelKey)})
	if err != nil {
		return fmt.Errorf("sentinel missing after upgrade: %w", err)
	}
	defer out.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(out.Body)
	if got := buf.String(); got != "harness-continuity:"+runID {
		return fmt.Errorf("sentinel content = %q, want run %s", got, runID)
	}
	return nil
}
