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
	// ...and keyed by kind (image/obj/pdf/doc), so a source bucket can be paired with
	// its DR counterpart instead of by array position.
	GuideBucketByKind map[string]string

	// DR. Which of these is populated tells you the engine, and therefore which DR
	// artifact exists at all — the two engines do cross-region DR completely
	// differently, so a validator that assumes one silently checks nothing on the other.
	DRBucketNames  []string          // values extracted from dr_s3_bucket_names map
	DRBucketByKind map[string]string // same map, keys preserved (image/obj/pdf/doc)

	// aurora (the default db_engine): DR is an Aurora Global Database with a headless
	// secondary cluster in the DR region.
	AuroraDRGlobalClusterID string // aurora_dr_global_cluster_id
	AuroraDRSecondaryHost   string // aurora_dr_secondary_endpoint

	// rds: DR is cross-region automated backup replication.
	DRBackupReplicationARN string // dr_rds_backup_replication_arn

	// The stack's primary DB credentials secret (Secrets Manager). The DR drill's
	// in-VPC probe reads it: users replicate with the cluster, so the primary's
	// credentials are valid on the promoted secondary.
	PrimaryDBSecretARN string // primary_db_secret
}
