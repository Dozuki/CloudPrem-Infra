// Package recovery is the seed of the P3 rebuild orchestrator: the code path that
// turns "the primary region is gone" into a running stack in the DR region.
//
// The decided recovery model (failover-automation design, 2026-07-28) has exactly one
// path for any database loss: promote the Aurora DR secondary, snapshot it, and stand
// up a NEW stack in the DR region seeded from that snapshot plus the replicated *-dr-*
// S3 buckets. No cross-region repoint exists - the primary app has no route into the
// air-gapped DR VPC, so the rebuild IS the recovery.
//
// This package computes the terraform inputs for that rebuild and renders the
// infra-live env.hcl scaffold. It is consumed by BOTH the harness's recovery scenario
// (which drills the rebuild against a real promoted cluster) and the cpi-dr CLI (which
// scaffolds the real thing at 2am) - the same "the tool you run in an incident is the
// tool the drill runs" property the promotion drill established.
package recovery

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// Inputs is everything the DR-region rebuild needs beyond a normal fresh deploy.
type Inputs struct {
	// SnapshotARN is a manual cluster snapshot of the PROMOTED cluster (see
	// SnapshotCluster). Seeding from a snapshot rather than adopting the promoted
	// cluster keeps the rebuilt stack fully terraform-owned - the promoted cluster is
	// wreckage with a drifted identity, not something to import.
	SnapshotARN string
	// Buckets maps content kind (image/obj/pdf/doc) to the DR replica bucket name -
	// the dr_s3_bucket_names output of the lost stack.
	Buckets map[string]string
	// S3KMSKeyARN is the DR-region KMS key the replica buckets are encrypted with -
	// the dr_s3_kms_key_arn output. s3.tf's existing-bucket path requires it.
	S3KMSKeyARN string
}

// Validate rejects inputs that would render a rebuild that cannot apply.
func (i Inputs) Validate() error {
	if i.SnapshotARN == "" {
		return fmt.Errorf("recovery: snapshot ARN is required (SnapshotCluster the promoted cluster first)")
	}
	if i.S3KMSKeyARN == "" {
		return fmt.Errorf("recovery: the DR S3 KMS key ARN is required (dr_s3_kms_key_arn output); s3_existing_buckets cannot be read without it")
	}
	for _, kind := range []string{"image", "obj", "pdf", "doc"} {
		if i.Buckets[kind] == "" {
			return fmt.Errorf("recovery: missing DR bucket for kind %q (dr_s3_bucket_names output)", kind)
		}
	}
	return nil
}

// EnvInputs returns the terraform inputs that turn a standard env into the DR-region
// rebuild: seed the database from the promoted cluster's snapshot, adopt the replicated
// buckets instead of creating empty ones, and keep DR itself OFF - the rebuilt stack IS
// the surviving copy; re-enabling DR back toward the original region is the deliberate
// failback step (P4), not something to configure while recovering.
func (i Inputs) EnvInputs() (map[string]interface{}, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	kinds := make([]string, 0, len(i.Buckets))
	for k := range i.Buckets {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	buckets := make([]interface{}, 0, len(kinds))
	for _, kind := range kinds {
		buckets = append(buckets, map[string]interface{}{
			"type":        kind,
			"bucket_name": i.Buckets[kind],
		})
	}
	return map[string]interface{}{
		"db_engine":                  "aurora",
		"aurora_snapshot_identifier": i.SnapshotARN,
		"s3_existing_buckets":        buckets,
		"s3_kms_key_id":              i.S3KMSKeyARN,
		"enable_dr":                  false,
	}, nil
}

// CheckEndpointServiceReachable answers, in one API call, the question the rebuild
// otherwise answers 45 minutes into a terraform apply: can a stack in this region
// create an endpoint to that PrivateLink service?
//
// A VPC endpoint service is regional and only accepts consumers from the regions its
// owner listed in supported_regions. Describing it from the consumer's region fails
// with the SAME InvalidServiceName that CreateVpcEndpoint fails with, so this is a
// faithful preflight rather than an approximation of one. Same-region consumers
// describe their own service and pass trivially.
//
// The first recovery-rebuild drill lost most of an hour to this: the DR region was
// missing from the shared Vault service's supported_regions, and the rebuild died in
// the physical layer partway through a recovery - the worst possible moment to learn
// it. An empty serviceName skips the check (nothing to reach).
func CheckEndpointServiceReachable(ctx context.Context, consumerRegion, serviceName string) error {
	if serviceName == "" {
		return nil
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(consumerRegion))
	if err != nil {
		return err
	}
	_, err = ec2.NewFromConfig(cfg).DescribeVpcEndpointServices(ctx, &ec2.DescribeVpcEndpointServicesInput{
		ServiceNames: []string{serviceName},
	})
	if err == nil {
		return nil
	}
	return fmt.Errorf("recovery: endpoint service %s is not reachable from %s, so the rebuild's physical layer "+
		"cannot create its VPC endpoint there (%w).\n"+
		"If this is the Vault PrivateLink service, add %q to supported_regions on the owning "+
		"vault-privatelink-service stack and let it apply, then retry",
		serviceName, consumerRegion, err, consumerRegion)
}

// SanitizeSnapshotID maps an arbitrary derived string (run id, stack name) onto RDS
// cluster-snapshot identifier rules: 1-63 chars, letters/digits/hyphens only, starts
// with a letter, no trailing hyphen, no "--". Cycle 36 failed CreateDBClusterSnapshot
// instantly because the harness run id carried the config name recover_source and the
// underscore is illegal - sanitize at every point a snapshot id is derived.
func SanitizeSnapshotID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")
	if out == "" {
		out = "s"
	} else if out[0] >= '0' && out[0] <= '9' {
		out = "s-" + out
	}
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	return out
}

// SnapshotCluster takes a manual cluster snapshot of the promoted cluster and waits
// for it to become available. Idempotent: if the snapshot id already exists (a prior
// attempt died mid-wait), the existing snapshot is awaited instead of erroring.
func SnapshotCluster(ctx context.Context, region, clusterID, snapshotID string, timeout time.Duration) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", err
	}
	rc := rds.NewFromConfig(cfg)
	var arn string
	out, err := rc.CreateDBClusterSnapshot(ctx, &rds.CreateDBClusterSnapshotInput{
		DBClusterIdentifier:         aws.String(clusterID),
		DBClusterSnapshotIdentifier: aws.String(snapshotID),
	})
	switch {
	case err == nil:
		arn = aws.ToString(out.DBClusterSnapshot.DBClusterSnapshotArn)
	case strings.Contains(err.Error(), "DBClusterSnapshotAlreadyExists"):
		logf("recovery: snapshot %s already exists - resuming its wait", snapshotID)
	default:
		return "", fmt.Errorf("recovery: create cluster snapshot: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		ds, err := rc.DescribeDBClusterSnapshots(ctx, &rds.DescribeDBClusterSnapshotsInput{
			DBClusterSnapshotIdentifier: aws.String(snapshotID),
		})
		if err != nil || len(ds.DBClusterSnapshots) == 0 {
			return "", fmt.Errorf("recovery: describe snapshot %s: %w", snapshotID, err)
		}
		s := ds.DBClusterSnapshots[0]
		arn = aws.ToString(s.DBClusterSnapshotArn)
		switch st := aws.ToString(s.Status); st {
		case "available":
			return arn, nil
		case "failed":
			return "", fmt.Errorf("recovery: snapshot %s reached terminal status failed", snapshotID)
		default:
			pct := int32(0)
			if s.PercentProgress != nil {
				pct = *s.PercentProgress
			}
			logf("recovery: snapshot %s %s (%d%%)", snapshotID, st, pct)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("recovery: snapshot %s not available after %s", snapshotID, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}
}

// DeleteClusterSnapshot removes a snapshot created by SnapshotCluster. NotFound is
// success - deletion is cleanup, not an assertion.
func DeleteClusterSnapshot(ctx context.Context, region, snapshotID string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	if _, err := rds.NewFromConfig(cfg).DeleteDBClusterSnapshot(ctx, &rds.DeleteDBClusterSnapshotInput{
		DBClusterSnapshotIdentifier: aws.String(snapshotID),
	}); err != nil && !strings.Contains(err.Error(), "NotFound") {
		return fmt.Errorf("recovery: delete snapshot %s: %w", snapshotID, err)
	}
	return nil
}

// ScaffoldParams describes the infra-live unit RenderInfraLiveEnvHCL generates for a
// REAL stack's rebuild (the harness renders its env.hcl through the same EnvInputs, but
// merged into its own matrix config instead of this scaffold).
type ScaffoldParams struct {
	StackName     string // the lost stack's identity, e.g. "acme-prod"
	PrimaryRegion string // the region that was lost
	DRRegion      string // where the rebuild lands
	InfraVersion  string // the env.hcl infra_version pin the lost stack ran
	Inputs        Inputs
}

// RenderInfraLiveEnvHCL renders the env.hcl for the DR-region infra-live unit: the
// P3 scaffold. The output is a complete locals block meant to be diffed against the
// lost stack's env.hcl by the operator - it deliberately carries only the recovery
// deltas plus provenance comments, because the rest of the lost stack's env.hcl
// (sizing, feature flags, vault wiring) must be copied over unchanged and reviewed
// in the PR, not guessed by tooling.
func RenderInfraLiveEnvHCL(p ScaffoldParams) (string, error) {
	env, err := p.Inputs.EnvInputs()
	if err != nil {
		return "", err
	}
	kinds := []string{"doc", "image", "obj", "pdf"}
	var b strings.Builder
	w := func(format string, a ...interface{}) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# DR-region rebuild of %s: %s -> %s.", p.StackName, p.PrimaryRegion, p.DRRegion)
	w("# Generated by cpi-dr; merge the lost stack's remaining env.hcl locals (sizing,")
	w("# feature flags, vault wiring) into this block and land it as the new unit under")
	w("# the DR region. Spacelift applies it like any other stack.")
	w("#")
	w("# KEEP aurora_snapshot_identifier and s3_existing_buckets in the unit permanently:")
	w("# clearing a snapshot identifier after use makes AWS replace the database.")
	w("locals {")
	w("  environment   = %q", p.StackName)
	w("  infra_version = %q # same pin the lost stack ran - recovery is not an upgrade", p.InfraVersion)
	w("")
	w("  db_engine                  = %q", env["db_engine"])
	w("  aurora_snapshot_identifier = %q # promoted-cluster snapshot", env["aurora_snapshot_identifier"])
	w("")
	w("  # The replicated content, adopted in place (never recreated empty).")
	w("  s3_kms_key_id = %q", env["s3_kms_key_id"])
	w("  s3_existing_buckets = [")
	for _, kind := range kinds {
		w("    { type = %q, bucket_name = %q },", kind, p.Inputs.Buckets[kind])
	}
	w("  ]")
	w("")
	w("  # The rebuilt stack IS the surviving copy. Re-enabling DR (pointed back at")
	w("  # %s) is the failback step, taken deliberately after the incident.", p.PrimaryRegion)
	w("  enable_dr = false")
	w("}")
	return b.String(), nil
}

func logf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, ">> [harness %s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}
