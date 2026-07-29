// cpi-dr: the Aurora Global Database failover executor (failover-automation design P1).
//
// The command that runs at 2am is the code the weekly drill rehearses: status, prepare
// and promote are thin drivers over validation's shared failover functions
// (dr_failover.go) and the in-VPC probe - the exact code AuroraPromotionDrill exercises
// in every full-config harness run. rebuild renders the P3 DR-region scaffold the same
// way the harness's recovery scenario deploys it.
//
// Doctrine (do not weaken):
//   - automate the execution, never the decision - promote REFUSES while the primary
//     answers, and there is no force flag; a reachable primary takes a PLANNED managed
//     failover (aws rds failover-global-cluster) instead, and forced promotion against
//     a live primary is an audited two-person decision outside this tool.
//   - prepare (reversible) is split from promote (irreversible) so provisioning
//     overlaps diagnosis without making the decision.
//   - every step is idempotent and resumable from observed AWS state.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/recovery"
	"github.com/Dozuki/CloudPrem-Infra/live/tests/validation"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// failoverInstanceSuffix names the instance prepare creates: <cluster>-failover. Fixed
// (not run-scoped like the drill's) so a resumed session finds the same instance.
const failoverInstanceSuffix = "failover"

const usage = `usage: cpi-dr <status|prepare|promote|rebuild> [flags]

  status   --dr-region <r> [--global-cluster <id>]
           Read-only. No id: list global clusters visible from the DR region.
           With id: members, engine posture, prepared instance, next commands.

  prepare  --dr-region <r> --global-cluster <id>
           REVERSIBLE: provision + verify the failover instance on the still-attached
           secondary. Run it the moment an incident MIGHT need a failover - it makes
           no decision and deleting the instance undoes it completely.

  promote  --dr-region <r> --global-cluster <id> [--db-secret-arn <arn>] [--skip-probe]
           IRREVERSIBLE. Preflights the primary: if it answers, this refuses - use a
           planned managed failover instead (aws rds failover-global-cluster). Then
           severs the secondary, waits for the writer, and (with --db-secret-arn, the
           stack's primary_db_secret output) proves connectivity from inside the DR
           VPC. Prints what it deliberately did NOT do.

  rebuild  --dr-region <r> --primary-region <r> --stack-name <s> --infra-version <v>
           --promoted-cluster <id> --s3-kms-key <arn> --bucket kind=name (x4: image,obj,pdf,doc)
           [--snapshot-id <id>] [--vault-endpoint-service <name>]
           P3: snapshot the promoted cluster (idempotent) and render the infra-live
           env.hcl scaffold for the DR-region rebuild to stdout. Pass
           --vault-endpoint-service to be warned NOW if the DR region cannot reach
           the Vault PrivateLink service (the rebuild's physical layer needs it).

Credentials: your normal SSO session (or the cpi-dr-executor role). Never stored keys.`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet(sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		drRegion      = fs.String("dr-region", "", "DR region (e.g. us-west-2)")
		globalCluster = fs.String("global-cluster", "", "global cluster identifier (aurora_dr_global_cluster_id output)")
		dbSecretARN   = fs.String("db-secret-arn", "", "primary_db_secret output ARN (promote's probe reads the DR-region replica)")
		skipProbe     = fs.Bool("skip-probe", false, "skip the in-VPC connectivity probe after promotion")
		primaryRegion = fs.String("primary-region", "", "the lost region (rebuild)")
		stackName     = fs.String("stack-name", "", "the lost stack's identity, e.g. acme-prod (rebuild)")
		infraVersion  = fs.String("infra-version", "", "the lost stack's env.hcl infra_version pin (rebuild)")
		promotedID    = fs.String("promoted-cluster", "", "the promoted cluster identifier (rebuild)")
		s3KMSKey      = fs.String("s3-kms-key", "", "dr_s3_kms_key_arn output (rebuild)")
		snapshotID    = fs.String("snapshot-id", "", "cluster snapshot id to create/reuse (rebuild; default <stack-name>-dr-rebuild)")
		vaultEndpoint = fs.String("vault-endpoint-service", "", "the lost stack's vault_endpoint_service_name (rebuild; checks the DR region can reach it)")
	)
	var buckets bucketFlags
	fs.Var(&buckets, "bucket", "kind=name, repeat for image, obj, pdf, doc (dr_s3_bucket_names output)")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	ctx := context.Background()

	fail := func(err error) int { fmt.Fprintf(stderr, "cpi-dr %s: %v\n", sub, err); return 1 }
	need := func(val, flagName string) bool {
		if val == "" {
			fmt.Fprintf(stderr, "cpi-dr %s: --%s is required\n%s\n", sub, flagName, usage)
			return true
		}
		return false
	}

	switch sub {
	case "status":
		if need(*drRegion, "dr-region") {
			return 2
		}
		if err := status(ctx, stdout, *drRegion, *globalCluster); err != nil {
			return fail(err)
		}
		return 0

	case "prepare":
		if need(*drRegion, "dr-region") || need(*globalCluster, "global-cluster") {
			return 2
		}
		out, err := validation.PrepareDRInstance(ctx, *drRegion, *globalCluster, failoverInstanceSuffix)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, `PREPARED (reversible - no decision has been made):
  cluster:  %s
  instance: %s (%s, ready in %s%s)

Next:
  promote when (and only when) the primary is confirmed gone:
    cpi-dr promote --dr-region %s --global-cluster %s --db-secret-arn <primary_db_secret ARN>
  or stand down by deleting the instance:
    aws rds delete-db-instance --region %s --db-instance-identifier %s --skip-final-snapshot
`, out.ClusterID, out.InstanceID, out.Class, out.Duration, resumed(out.Resumed), *drRegion, *globalCluster, *drRegion, out.InstanceID)
		return 0

	case "promote":
		if need(*drRegion, "dr-region") || need(*globalCluster, "global-cluster") {
			return 2
		}
		return promote(ctx, stdout, stderr, *drRegion, *globalCluster, *dbSecretARN, *skipProbe)

	case "rebuild":
		for _, req := range []struct{ v, n string }{
			{*drRegion, "dr-region"}, {*primaryRegion, "primary-region"}, {*stackName, "stack-name"},
			{*infraVersion, "infra-version"}, {*promotedID, "promoted-cluster"}, {*s3KMSKey, "s3-kms-key"},
		} {
			if need(req.v, req.n) {
				return 2
			}
		}
		// Warn, do not fail: rendering the scaffold is harmless and the operator may
		// want it in hand while someone else fixes the Vault service. But they must
		// learn it here, not from a physical-layer apply failure after the PR lands.
		if *vaultEndpoint != "" {
			if verr := recovery.CheckEndpointServiceReachable(ctx, *drRegion, *vaultEndpoint); verr != nil {
				fmt.Fprintf(stderr, "\nWARNING: %v\nThe rebuild's physical layer WILL fail until that is fixed.\n\n", verr)
			}
		}
		snapID := recovery.SanitizeSnapshotID(*snapshotID)
		if *snapshotID == "" {
			snapID = recovery.SanitizeSnapshotID(*stackName + "-dr-rebuild")
		}
		fmt.Fprintf(stderr, "snapshotting %s -> %s in %s (idempotent; this is the slow part)\n", *promotedID, snapID, *drRegion)
		snapARN, err := recovery.SnapshotCluster(ctx, *drRegion, *promotedID, snapID, 60*time.Minute)
		if err != nil {
			return fail(err)
		}
		scaffold, err := recovery.RenderInfraLiveEnvHCL(recovery.ScaffoldParams{
			StackName:     *stackName,
			PrimaryRegion: *primaryRegion,
			DRRegion:      *drRegion,
			InfraVersion:  *infraVersion,
			Inputs: recovery.Inputs{
				SnapshotARN: snapARN,
				Buckets:     buckets.m,
				S3KMSKeyARN: *s3KMSKey,
			},
		})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, scaffold)
		fmt.Fprintf(stderr, "\nLand this as infra-live's <dr-region>/%s/env.hcl (merge the lost stack's remaining locals), open the PR, let Spacelift apply.\n", *stackName)
		return 0

	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
}

func promote(ctx context.Context, stdout, stderr io.Writer, drRegion, globalCluster, dbSecretARN string, skipProbe bool) int {
	fail := func(err error) int { fmt.Fprintf(stderr, "cpi-dr promote: %v\n", err); return 1 }

	// The preflight IS the point of this tool: a reachable primary means promotion is
	// the wrong instrument, full stop.
	reachable, detail := validation.PrimaryWriterReachable(ctx, globalCluster, drRegion)
	if reachable {
		fmt.Fprintf(stderr, `REFUSING to promote: the primary is alive (%s).

A reachable primary takes a PLANNED managed failover, which keeps replication and
loses nothing:
  aws rds failover-global-cluster --global-cluster-identifier %s \
    --target-db-cluster-identifier <DR member ARN>

There is deliberately no override flag. If you believe the primary is lying, that is
an audited two-person decision made outside this tool.
`, detail, globalCluster)
		return 1
	}
	fmt.Fprintf(stderr, "preflight: %s -> proceeding\n", detail)

	clusterID, err := validation.DRSecondaryClusterID(ctx, drRegion, globalCluster)
	if err != nil {
		// Resume path: membership may already be severed by a prior promote attempt.
		return fail(fmt.Errorf("%w\n(if a prior promote already severed the membership, verify with: cpi-dr status --dr-region %s)", err, drRegion))
	}
	instanceID := clusterID + "-" + failoverInstanceSuffix
	out, err := validation.PromoteSecondary(ctx, drRegion, globalCluster, clusterID, instanceID)
	if err != nil {
		if strings.Contains(err.Error(), "instance never became the writer") {
			return fail(fmt.Errorf("%w\n(did prepare run? cpi-dr prepare --dr-region %s --global-cluster %s)", err, drRegion, globalCluster))
		}
		return fail(err)
	}

	probeNote := "SKIPPED (--skip-probe)"
	if dbSecretARN == "" {
		probeNote = "SKIPPED (no --db-secret-arn given)"
		skipProbe = true
	}
	if !skipProbe {
		primaryRegion := arnRegion(dbSecretARN)
		user, password, cerr := validation.DBCredentials(ctx, primaryRegion, drRegion, dbSecretARN)
		if cerr != nil {
			return fail(fmt.Errorf("promotion SUCCEEDED but the probe could not get credentials: %w\n(writer endpoint: %s)", cerr, out.Endpoint))
		}
		cleanup, perr := validation.ProbeConnectivity(ctx, drRegion, clusterID, out.Endpoint, user, password, "cpi-dr")
		if perr != nil {
			if cleanup != nil {
				_ = cleanup()
			}
			return fail(fmt.Errorf("promotion SUCCEEDED but the in-VPC probe failed: %w\n(writer endpoint: %s)", perr, out.Endpoint))
		}
		probeNote = "VERIFIED (in-VPC SELECT 1 over verified TLS)"
		fmt.Fprintln(stderr, "probe verified; cleaning up its ephemeral footprint (Lambda ENIs release lazily, up to ~20m - safe to Ctrl-C, leftovers are cosmetic and tagged harness-dr-probe-cpi-dr)")
		if cerr := cleanup(); cerr != nil {
			fmt.Fprintf(stderr, "probe cleanup incomplete: %v\n", cerr)
		}
	}

	fmt.Fprintf(stdout, `PROMOTED in %s%s:
  writer:   %s
  endpoint: %s
  probe:    %s

What this did NOT do (deliberately):
  - no application traffic was moved; the promoted cluster sits in the air-gapped DR
    VPC with no app to serve until the rebuild
  - no DNS was changed
  - the S3 replicas, ECR and the rest of the stack are untouched

Next: the DR-region rebuild (the ONLY recovery path - there is no repoint):
  cpi-dr rebuild --dr-region %s --primary-region <lost region> --stack-name <stack> \
    --infra-version <pin> --promoted-cluster %s --s3-kms-key <dr_s3_kms_key_arn> \
    --bucket image=<...> --bucket obj=<...> --bucket pdf=<...> --bucket doc=<...>
`, out.Duration, resumed(out.Resumed), clusterID, out.Endpoint, probeNote, drRegion, clusterID)
	return 0
}

// status prints the read-only picture: safe to run any time, from anywhere.
func status(ctx context.Context, stdout io.Writer, drRegion, globalCluster string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(drRegion))
	if err != nil {
		return err
	}
	rc := rds.NewFromConfig(cfg)
	in := &rds.DescribeGlobalClustersInput{}
	if globalCluster != "" {
		in.GlobalClusterIdentifier = aws.String(globalCluster)
	}
	gcs, err := rc.DescribeGlobalClusters(ctx, in)
	if err != nil {
		return err
	}
	if len(gcs.GlobalClusters) == 0 {
		fmt.Fprintf(stdout, "no global clusters visible from %s\n", drRegion)
		return nil
	}
	for _, g := range gcs.GlobalClusters {
		gid := aws.ToString(g.GlobalClusterIdentifier)
		fmt.Fprintf(stdout, "global cluster %s (%s %s, status %s)\n",
			gid, aws.ToString(g.Engine), aws.ToString(g.EngineVersion), aws.ToString(g.Status))
		var drMember string
		for _, m := range g.GlobalClusterMembers {
			arn := aws.ToString(m.DBClusterArn)
			role := "SECONDARY"
			if aws.ToBool(m.IsWriter) {
				role = "WRITER"
			}
			fmt.Fprintf(stdout, "  %-9s %s\n", role, arn)
			if !aws.ToBool(m.IsWriter) && strings.Contains(arn, ":"+drRegion+":") {
				parts := strings.Split(arn, ":")
				drMember = parts[len(parts)-1]
			}
		}
		if drMember == "" {
			fmt.Fprintf(stdout, "  no secondary in %s - nothing to promote here\n", drRegion)
			continue
		}
		dbc, derr := rc.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(drMember)})
		if derr != nil || len(dbc.DBClusters) == 0 {
			fmt.Fprintf(stdout, "  (could not describe %s: %v)\n", drMember, derr)
			continue
		}
		c := dbc.DBClusters[0]
		scaling := "MISSING - prepare will fall back to a provisioned class (stack predates CPI #352)"
		if c.ServerlessV2ScalingConfiguration != nil {
			scaling = "present (db.serverless failover)"
		}
		fmt.Fprintf(stdout, "  DR secondary %s: status %s, serverless v2 scaling %s\n",
			drMember, aws.ToString(c.Status), scaling)
		prepared := false
		for _, m := range c.DBClusterMembers {
			id := aws.ToString(m.DBInstanceIdentifier)
			if strings.HasSuffix(id, "-"+failoverInstanceSuffix) {
				prepared = true
				fmt.Fprintf(stdout, "  PREPARED instance %s (writer=%t)\n", id, aws.ToBool(m.IsClusterWriter))
			}
		}
		reachable, detail := validation.PrimaryWriterReachable(ctx, gid, drRegion)
		fmt.Fprintf(stdout, "  primary: %s (reachable=%t)\n", detail, reachable)
		switch {
		case !prepared:
			fmt.Fprintf(stdout, "  next: cpi-dr prepare --dr-region %s --global-cluster %s\n", drRegion, gid)
		case reachable:
			fmt.Fprintf(stdout, "  next: prepared and primary is ALIVE - promote will refuse; planned failover is the tool if a switch is wanted\n")
		default:
			fmt.Fprintf(stdout, "  next: cpi-dr promote --dr-region %s --global-cluster %s --db-secret-arn <primary_db_secret>\n", drRegion, gid)
		}
	}
	return nil
}

func resumed(r bool) string {
	if r {
		return ", resumed from a prior run"
	}
	return ""
}

func arnRegion(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) > 3 {
		return parts[3]
	}
	return ""
}

// bucketFlags collects repeated --bucket kind=name flags.
type bucketFlags struct{ m map[string]string }

func (b *bucketFlags) String() string {
	if b.m == nil {
		return ""
	}
	kinds := make([]string, 0, len(b.m))
	for k := range b.m {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = k + "=" + b.m[k]
	}
	return strings.Join(parts, ",")
}

func (b *bucketFlags) Set(v string) error {
	kv := strings.SplitN(v, "=", 2)
	if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
		return fmt.Errorf("want kind=name, got %q", v)
	}
	if b.m == nil {
		b.m = map[string]string{}
	}
	b.m[kv[0]] = kv[1]
	return nil
}
