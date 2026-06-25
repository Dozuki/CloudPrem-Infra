package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3API is the minimal S3 surface the manifest store needs (so tests inject a fake).
type S3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3Store persists the manifest in the harness state bucket under the run prefix.
type S3Store struct {
	client S3API
	bucket string
}

func NewS3Store(client S3API, bucket string) *S3Store { return &S3Store{client: client, bucket: bucket} }

func (s *S3Store) key(statePrefix string) string { return statePrefix + ManifestObjectName }

func (s *S3Store) Load(ctx context.Context, statePrefix string) (*RunManifest, bool, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(statePrefix)),
	})
	if err != nil {
		var ae smithy.APIError
		if errors.As(err, &ae) && (ae.ErrorCode() == "NoSuchKey" || ae.ErrorCode() == "NotFound") {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	var rm RunManifest
	if err := json.Unmarshal(b, &rm); err != nil {
		return nil, false, err
	}
	return &rm, true, nil
}

func (s *S3Store) Save(ctx context.Context, statePrefix string, m *RunManifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(s.key(statePrefix)),
		Body: bytes.NewReader(b), ContentType: aws.String("application/json"),
	})
	return err
}
