package validation

// StackOutputs is the subset of physical/logical outputs the validators use.
type StackOutputs struct {
	DozukiURL    string // app NLB URL
	DashboardURL string
	ClusterName  string
	Region       string
}
