package harness

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeS3GetObject answers GetObject with a fixed body and Content-Range, ignoring
// the requested range - enough to drive S3LogTail.Tail through both the
// partial-read and whole-object-read paths.
type fakeS3GetObject struct {
	body         string
	contentRange *string
}

func (f *fakeS3GetObject) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{
		Body:         io.NopCloser(strings.NewReader(f.body)),
		ContentRange: f.contentRange,
	}, nil
}

func TestS3LogTailWholeObjectKeepsFirstLine(t *testing.T) {
	// A suffix range against an object smaller than tailRangeBytes comes back as
	// the whole object - Content-Range still reports Partial Content, but its start
	// offset is 0. Line 1 is intact and must survive.
	body := "Error: the only line, and it is the one that matters\n"
	fake := &fakeS3GetObject{body: body, contentRange: aws.String("bytes 0-53/54")}
	tail := NewS3LogTail(fake)

	got, err := tail.Tail(context.Background(), "bucket", "key")
	if err != nil {
		t.Fatalf("Tail() error = %v", err)
	}
	if !strings.Contains(got, "Error: the only line") {
		t.Errorf("Tail() = %q, want it to keep line 1 of a whole-object read", got)
	}
}

func TestS3LogTailShortLogWithErrorOnLineOne(t *testing.T) {
	body := "Error: boom on the very first line\nsecond line, harmless\n"
	fake := &fakeS3GetObject{body: body, contentRange: aws.String("bytes 0-57/58")}
	tail := NewS3LogTail(fake)

	got, err := tail.Tail(context.Background(), "bucket", "key")
	if err != nil {
		t.Fatalf("Tail() error = %v", err)
	}
	if !strings.Contains(got, "Error: boom on the very first line") {
		t.Errorf("Tail() = %q, want the line-1 error preserved for a whole-object read", got)
	}
}

func TestS3LogTailSingleLineNoTrailingNewline(t *testing.T) {
	body := "Error: no newline at all"
	fake := &fakeS3GetObject{body: body, contentRange: aws.String("bytes 0-24/25")}
	tail := NewS3LogTail(fake)

	got, err := tail.Tail(context.Background(), "bucket", "key")
	if err != nil {
		t.Fatalf("Tail() error = %v", err)
	}
	if got != body {
		t.Errorf("Tail() = %q, want %q (a single line with no newline must not come back empty)", got, body)
	}
}

func TestS3LogTailPartialReadDropsFirstLine(t *testing.T) {
	// A genuine partial read (start offset > 0) starts mid-line - the fragment
	// before the first real newline must be dropped.
	body := "-mid-file-fragment\nError: the real second line\n"
	fake := &fakeS3GetObject{body: body, contentRange: aws.String("bytes 262144-300000/1000000")}
	tail := NewS3LogTail(fake)

	got, err := tail.Tail(context.Background(), "bucket", "key")
	if err != nil {
		t.Fatalf("Tail() error = %v", err)
	}
	if strings.Contains(got, "mid-file-fragment") {
		t.Errorf("Tail() = %q, want the partial first line dropped", got)
	}
	if !strings.Contains(got, "Error: the real second line") {
		t.Errorf("Tail() = %q, want the real second line preserved", got)
	}
}

func TestS3LogTailNoContentRangeKeepsFirstLine(t *testing.T) {
	// No Content-Range header at all (e.g. a 200 OK, not 206) means the whole
	// object came back - treat it the same as a start-offset-0 partial read.
	body := "Error: whole object, no range header\n"
	fake := &fakeS3GetObject{body: body, contentRange: nil}
	tail := NewS3LogTail(fake)

	got, err := tail.Tail(context.Background(), "bucket", "key")
	if err != nil {
		t.Fatalf("Tail() error = %v", err)
	}
	if !strings.Contains(got, "Error: whole object") {
		t.Errorf("Tail() = %q, want line 1 preserved when there is no Content-Range", got)
	}
}
