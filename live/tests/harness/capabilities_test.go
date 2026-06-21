package harness

import (
	"reflect"
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

func TestSplitCritical(t *testing.T) {
	names := []string{"dozuki-app-deployment", "dozuki-nextjs", "dozuki-memcached", "varnish"}
	critical, advisory := SplitCritical(names, DefaultCriticalWorkloads())
	if !reflect.DeepEqual(critical, []string{"dozuki-app-deployment", "dozuki-nextjs"}) {
		t.Fatalf("critical = %v, want [dozuki-app-deployment dozuki-nextjs]", critical)
	}
	if !reflect.DeepEqual(advisory, []string{"dozuki-memcached", "varnish"}) {
		t.Fatalf("advisory = %v, want [dozuki-memcached varnish]", advisory)
	}
}

func TestSplitCritical_emptyPatternsAllAdvisory(t *testing.T) {
	critical, advisory := SplitCritical([]string{"a", "b"}, nil)
	if len(critical) != 0 || len(advisory) != 2 {
		t.Fatalf("nil patterns: critical=%v advisory=%v, want none critical", critical, advisory)
	}
}
