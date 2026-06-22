package harness

import (
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
