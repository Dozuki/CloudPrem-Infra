package harness

import (
	"path/filepath"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
)

// Capabilities describes what a deployed release supports, so validation runs only
// the checks that apply. Infra flags are derived per ref from its Terraform outputs
// (which readOutputs already extracts presence-tolerantly: a missing/renamed output
// yields an empty value → the capability is absent). HasLogging is set by the caller
// from the cluster (see validation.LoggingEnabled).
type Capabilities struct {
	HasDR           bool
	HasDMS          bool
	HasGuideBuckets bool
	HasLogging      bool
}

// DetectCapabilities derives infra capabilities from already-read outputs.
func DetectCapabilities(o validation.StackOutputs) Capabilities {
	return Capabilities{
		HasDR:           len(o.DRBucketNames) > 0,
		HasDMS:          o.DMSTaskARN != "",
		HasGuideBuckets: len(o.GuideBuckets) > 0,
	}
}

// DefaultCriticalWorkloads is the fallback critical-set when matrix defaults omit it:
// the app and nextjs deployments that back the served endpoint.
func DefaultCriticalWorkloads() []string { return []string{"dozuki-app*", "*nextjs*"} }

// SplitCritical partitions workload names into critical (matching any glob pattern)
// and advisory (the rest), preserving input order.
func SplitCritical(names, patterns []string) (critical, advisory []string) {
	for _, n := range names {
		if matchesAny(n, patterns) {
			critical = append(critical, n)
		} else {
			advisory = append(advisory, n)
		}
	}
	return critical, advisory
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}
