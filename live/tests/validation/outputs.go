package validation

// StackOutputs is the subset of physical/logical outputs the validators use.
type StackOutputs struct {
	DozukiURL    string // app NLB URL
	DashboardURL string
	ClusterName  string
	Region       string

	// DMS
	DMSTaskARN string
	DMSEnabled bool

	// S3 guide buckets (source region) — non-empty entries collected from the
	// four typed bucket outputs (guide_images, guide_objects, guide_pdfs, documents).
	GuideBuckets []string

	// DR
	DRBucketNames []string // values extracted from dr_s3_bucket_names map
	DBIdentifier  string   // primary RDS instance identifier (for DR drill)
}
