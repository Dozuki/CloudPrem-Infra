package validation

import "testing"

// The regression this file exists for: pairing by array index put the canary in the image
// bucket and waited for it in the doc bucket, so the check could only ever fail.
func TestSourceDRBucketPairsMatchByKindNotOrder(t *testing.T) {
	outs := StackOutputs{
		GuideBucketByKind: map[string]string{
			"image": "src-image", "obj": "src-obj", "pdf": "src-pdf", "doc": "src-doc",
		},
		DRBucketByKind: map[string]string{
			"doc": "dr-doc", "image": "dr-image", "obj": "dr-obj", "pdf": "dr-pdf",
		},
	}
	pairs := SourceDRBucketPairs(outs)
	if len(pairs) != 4 {
		t.Fatalf("got %d pairs, want 4", len(pairs))
	}
	for _, p := range pairs {
		if p.Source != "src-"+p.Kind || p.DR != "dr-"+p.Kind {
			t.Errorf("kind %s paired %s -> %s, want src-%s -> dr-%s", p.Kind, p.Source, p.DR, p.Kind, p.Kind)
		}
	}
}

// A kind present on only one side is not a pair; emitting it would canary into a bucket
// that has no counterpart and time out for a reason unrelated to replication.
func TestSourceDRBucketPairsSkipsUnmatchedKinds(t *testing.T) {
	outs := StackOutputs{
		GuideBucketByKind: map[string]string{"image": "src-image", "pdf": "src-pdf"},
		DRBucketByKind:    map[string]string{"image": "dr-image"},
	}
	pairs := SourceDRBucketPairs(outs)
	if len(pairs) != 1 || pairs[0].Kind != "image" {
		t.Errorf("got %+v, want only the image pair", pairs)
	}
}

func TestSourceDRBucketPairsEmptyWhenNothingMatches(t *testing.T) {
	if p := SourceDRBucketPairs(StackOutputs{}); len(p) != 0 {
		t.Errorf("got %+v, want none", p)
	}
}
