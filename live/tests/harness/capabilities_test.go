package harness

import (
	"testing"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
)

func TestDetectCapabilities_presence(t *testing.T) {
	full := validation.StackOutputs{
		DMSTaskARN:    "arn:aws:dms:...:task/x",
		GuideBuckets:  []string{"b1"},
		DRBucketNames: []string{"dr1"},
	}
	got := DetectCapabilities(full)
	if !got.HasDMS || !got.HasGuideBuckets || !got.HasDR {
		t.Fatalf("full outputs: %+v, want all infra caps true", got)
	}
	// HasLogging is set by the caller from the cluster, not from outputs.
	if got.HasLogging {
		t.Fatalf("HasLogging should default false (set from cluster), got true")
	}

	empty := DetectCapabilities(validation.StackOutputs{})
	if empty.HasDMS || empty.HasGuideBuckets || empty.HasDR {
		t.Fatalf("empty outputs: %+v, want all infra caps false (missing → absent)", empty)
	}
}
