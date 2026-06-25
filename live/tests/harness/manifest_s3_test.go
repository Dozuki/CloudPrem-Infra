package harness

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type fakeS3 struct{ objs map[string][]byte }

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}
func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	f.objs[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}

func TestS3StoreRoundTripAndMissing(t *testing.T) {
	ctx := context.Background()
	f := &fakeS3{objs: map[string][]byte{}}
	s := NewS3Store(f, "state-bucket")

	if _, ok, err := s.Load(ctx, "run1-min/"); err != nil || ok {
		t.Fatalf("missing load: ok=%v err=%v", ok, err)
	}
	if err := s.Save(ctx, "run1-min/", &RunManifest{ToRef: "v7.1.0", Scenario: "fresh"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := f.objs["run1-min/harness-manifest.json"]; !ok {
		t.Fatalf("expected object at run1-min/harness-manifest.json, have %v", f.objs)
	}
	got, ok, err := s.Load(ctx, "run1-min/")
	if err != nil || !ok || got.ToRef != "v7.1.0" {
		t.Fatalf("load: ok=%v err=%v got=%+v", ok, err, got)
	}
}
