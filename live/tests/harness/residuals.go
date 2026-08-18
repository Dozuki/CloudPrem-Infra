package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Residual detection: the net under a terraform apply or destroy that did not finish
// telling the truth (Lodestar-1xm.36.1 / .36.2).
//
// The failure this exists for, in full: harness-bi-ha-hftj2 was superseded and its
// provision pod took SIGTERM at 2026-08-17T01:34:29Z with
// module.aurora[0].aws_rds_cluster_instance.this["writer"] and
// module.eks_cluster.aws_eks_cluster.this[0] both mid-create. tofu died before writing
// either to state, so AWS kept an Aurora writer and an EKS cluster that terraform had
// no address for. Every teardown afterwards destroyed what state knew about, then
// failed on DeleteDBCluster ("still contains DB instances in a non-deleting state") and
// DeleteSecurityGroup / DeleteSubnet ("DependencyViolation") - the dependencies being
// exactly the two resources terraform could not see. The retries re-failed identically
// and nothing ever named what was in the way.
//
// This cannot prevent that: it cannot run inside a process that is being killed. It is
// a detection net at the RUN BOUNDARIES - after an apply completes, and after a destroy
// finishes or fails - and its whole job is to turn "retries exhausted" into a named
// list of resources a human can act on.
//
// Two structured signals, no stderr scraping:
//
//	tag-reconcile   live resources carrying this run's Customer + deleteAfter tags whose
//	                physical id appears nowhere in `terragrunt show -json`. This is the
//	                signal that catches the smoke1bc2 class, because an orphan is by
//	                definition invisible to every state-based check.
//	state-remnant   addresses still present in `terragrunt state list` after a destroy
//	                returned non-zero. What remains in state after a destroy IS what
//	                blocked it, stated in terraform's own terms.
//
// Deliberately NOT the destroy's own -json diagnostic stream. Getting it would mean
// running the destroy in machine-readable UI mode, and lastErrorLine / wrapTGError /
// evidence.go's ExtractError all parse the human "Error:" lines that mode removes -
// which is what builds the Slack failure card. The two signals above are structured
// without touching the destroy invocation.

// Residual is one thing that outlived the phase that should have accounted for it.
type Residual struct {
	// Source is which signal found it: "tag-reconcile" or "state-remnant".
	Source string `json:"source"`
	// ARN is set by tag-reconcile (the live resource), Address by state-remnant (the
	// terraform address). Exactly one is populated; both are identities a human can
	// paste into a CLI, which is the point of the whole report.
	ARN     string `json:"arn,omitempty"`
	Address string `json:"address,omitempty"`
	// Type is "<service>:<resourceType>" for an ARN, or the terraform resource type
	// for an address.
	Type string `json:"type"`
	// Blocking separates "terraform lost this" from "this is here for some other
	// reason". Only a blocking residual fails a run - see classifyResidual for the
	// three reasons a hit is reported but not enforced.
	Blocking bool `json:"blocking"`
	// Why records the classification reason for a non-blocking hit, so the report
	// explains itself instead of leaving a reader to guess why a named resource did
	// not fail anything.
	Why string `json:"why,omitempty"`
}

// ResidualReport is one boundary check. It lands in the run manifest, which outlives
// the Workflow CR (Argo TTLs a failed one after three days), so the identities survive
// the pod, the workflow and the retry that produced them.
type ResidualReport struct {
	Phase      string     `json:"phase"`                   // "provision" or "teardown"
	CheckedAt  string     `json:"checked_at"`              // RFC3339 UTC
	DestroyErr string     `json:"destroy_error,omitempty"` // error class, when a destroy failed
	Residuals  []Residual `json:"residuals,omitempty"`
	// Incomplete records that a signal could not be gathered (no tagging client, a
	// `show -json` that would not run). A report that silently dropped a signal reads
	// identical to a clean one, and this file exists because a clean-looking report on
	// a leaking stack is the expensive failure.
	Incomplete []string `json:"incomplete,omitempty"`
}

// Blocking returns the residuals that should fail the run.
func (r *ResidualReport) Blocking() []Residual {
	if r == nil {
		return nil
	}
	var out []Residual
	for _, res := range r.Residuals {
		if res.Blocking {
			out = append(out, res)
		}
	}
	return out
}

// Clean reports whether the check found nothing AND gathered everything it meant to.
// An incomplete check is never clean: it did not look, so it cannot say.
func (r *ResidualReport) Clean() bool {
	return r != nil && len(r.Residuals) == 0 && len(r.Incomplete) == 0
}

// Summary is the one-line human form used in the phase error and the pod log.
func (r *ResidualReport) Summary() string {
	if r == nil {
		return "no residual check ran"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d residual(s) after %s (%d blocking)", len(r.Residuals), r.Phase, len(r.Blocking()))
	for _, res := range r.Residuals {
		id := res.ARN
		if id == "" {
			id = res.Address
		}
		mark := "blocking"
		if !res.Blocking {
			mark = "informational: " + res.Why
		}
		fmt.Fprintf(&b, "\n  - [%s] %s %s (%s)", res.Source, res.Type, id, mark)
	}
	for _, inc := range r.Incomplete {
		fmt.Fprintf(&b, "\n  - [incomplete] %s", inc)
	}
	return b.String()
}

// stateIndex is what terraform state accounts for, indexed two ways.
//
// The two-index split is the fix for a defect that made the first version of this
// check miss the very orphan it was written for. A flat pool of bare ids matched a
// live ARN by its trailing segment alone, and CloudPrem names the Aurora cluster and
// the EKS cluster identically (both are local.identifier, "smoke1bc2-bi"). So state's
// aws_rds_cluster.id accounted for the out-of-state EKS cluster's ARN tail, and the
// leak reported clean. AWS ids are unique only within a service; ARNs are unique
// globally. Index accordingly.
type stateIndex struct {
	arns   map[string]struct{}            // full ARNs: safe to match globally
	byType map[string]map[string]struct{} // "<service>:<type>" -> bare ids
	// types is every AWS type this state was ABLE to speak for. A live ARN of a type
	// absent here cannot be judged - state says nothing about that type at all - and
	// is reported unclassified rather than as a leak.
	types map[string]struct{}
}

func newStateIndex() *stateIndex {
	return &stateIndex{
		arns:   map[string]struct{}{},
		byType: map[string]map[string]struct{}{},
		types:  map[string]struct{}{},
	}
}

func (s *stateIndex) add(awsType, id string) {
	if awsType == "" || id == "" {
		return
	}
	if _, ok := s.byType[awsType]; !ok {
		s.byType[awsType] = map[string]struct{}{}
	}
	s.byType[awsType][id] = struct{}{}
	s.types[awsType] = struct{}{}
}

// tfTypeToAWSType maps a terraform resource type to the "<service>:<resourceType>"
// arnResourceType derives from that resource's ARN. Only types the CloudPrem physical
// and logical layers actually create need an entry: an unmapped type simply means a
// live ARN of that shape is reported unclassified instead of blocking, which is the
// safe direction. Grepped from terraform/physical and terraform/logical.
var tfTypeToAWSType = map[string]string{
	"aws_eks_cluster":                   "eks:cluster",
	"aws_eks_node_group":                "eks:nodegroup",
	"aws_eks_addon":                     "eks:addon",
	"aws_eks_pod_identity_association":  "eks:podidentityassociation",
	"aws_rds_cluster":                   "rds:cluster",
	"aws_rds_cluster_instance":          "rds:db",
	"aws_db_instance":                   "rds:db",
	"aws_db_parameter_group":            "rds:pg",
	"aws_rds_cluster_parameter_group":   "rds:cluster-pg",
	"aws_db_subnet_group":               "rds:subgrp",
	"aws_rds_global_cluster":            "rds:global-cluster",
	"aws_vpc":                           "ec2:vpc",
	"aws_subnet":                        "ec2:subnet",
	"aws_security_group":                "ec2:security-group",
	"aws_route_table":                   "ec2:route-table",
	"aws_network_acl":                   "ec2:network-acl",
	"aws_internet_gateway":              "ec2:internet-gateway",
	"aws_nat_gateway":                   "ec2:natgateway",
	"aws_eip":                           "ec2:elastic-ip",
	"aws_vpc_endpoint":                  "ec2:vpc-endpoint",
	"aws_ebs_volume":                    "ec2:volume",
	"aws_kms_key":                       "kms:key",
	"aws_kms_replica_key":               "kms:key",
	"aws_kms_external_key":              "kms:key",
	"aws_s3_bucket":                     "s3",
	"aws_iam_role":                      "iam:role",
	"aws_iam_policy":                    "iam:policy",
	"aws_cloudwatch_log_group":          "logs:log-group",
	"aws_lb":                            "elasticloadbalancing:loadbalancer",
	"aws_lb_target_group":               "elasticloadbalancing:targetgroup",
	"aws_dms_replication_instance":      "dms:rep",
	"aws_dms_endpoint":                  "dms:endpoint",
	"aws_dms_replication_task":          "dms:task",
	"aws_dms_replication_subnet_group":  "dms:subgrp",
	"aws_dms_certificate":               "dms:cert",
	"aws_msk_cluster":                   "kafka:cluster",
	"aws_secretsmanager_secret":         "secretsmanager:secret",
	"aws_elasticache_replication_group": "elasticache:replicationgroup",
	"aws_efs_file_system":               "elasticfilesystem:file-system",
	"aws_opensearch_domain":             "es:domain",
	"aws_sqs_queue":                     "sqs",
	"aws_sns_topic":                     "sns",
}

// primaryARNFields names the attribute holding a resource's OWN ARN, for the types
// where the provider does not call it "arn". Kept as a strict allowlist rather than
// harvesting every *_arn attribute: role_arn, kms_key_arn and friends identify
// DEPENDENCIES, and indexing those would mark an unrelated live resource as accounted
// for - the same class of false negative the type split above fixes.
//
// aws_eks_pod_identity_association is the one that matters today: the provider stores
// association_arn and sets id to the bare association id, while the live ARN's resource
// part is "<cluster>/<association-id>". CloudPrem creates six of them (five in
// terraform/physical/eks.tf, one in terraform/logical/flux.tf), so without this every
// provision would report six residuals that terraform demonstrably owns.
var primaryARNFields = map[string][]string{
	"aws_eks_pod_identity_association": {"association_arn"},
	"aws_rds_cluster":                  {"arn", "cluster_identifier"},
	"aws_msk_cluster":                  {"arn", "arn_kafka"},
}

// serviceCreatedTypes are types AWS itself creates from a terraform-managed parent and
// tags by propagation, so "not in state" is their NORMAL condition, not a leak.
//
// Snapshots are the concrete case: every RDS cluster and instance in this repo sets
// copy_tags_to_snapshot = true (terraform/physical/{rds.tf:203,aurora.tf:192,
// dr_aurora.tf:163,bi.tf:551}), so each automated backup carries the run's Customer and
// deleteAfter and appears in the tagging index while belonging to no terraform address.
// Enforcing on those would fail a healthy nightly the first time a backup window opened
// mid-provision.
var serviceCreatedTypes = map[string]bool{
	"rds:snapshot":                      true,
	"rds:cluster-snapshot":              true,
	"ec2:snapshot":                      true,
	"ec2:volume":                        true,
	"ec2:network-interface":             true,
	"ec2:image":                         true,
	"ec2:instance":                      true, // EKS Auto Mode nodes; the cluster owns their lifecycle
	"elasticloadbalancing:loadbalancer": true, // created by the in-cluster LB controller
	"elasticloadbalancing:targetgroup":  true,
	"logs:log-group":                    true, // the CloudWatch agent creates these lazily
}

// terraformManagedTypes is the set of AWS types CloudPrem's own terraform creates,
// derived from tfTypeToAWSType so the two can never drift. It is the audited
// "terraform is supposed to own this" list, and it is what enforcement keys on.
//
// Deliberately NOT "did this run's state happen to contain one". That was the first
// version's rule and it is empty exactly when it matters: after a successful destroy
// there is no state left, so every surviving orphan would classify as unjudgeable and
// nothing could ever fail. Whether terraform OWNS a type is a property of the code, not
// of one run's state at one moment.
var terraformManagedTypes = func() map[string]bool {
	m := map[string]bool{}
	for _, awsType := range tfTypeToAWSType {
		m[awsType] = true
	}
	return m
}()

// classifyResidual decides whether a hit fails the run. Three reasons it does not:
//
//	service-created  AWS makes and tags these from a terraform-managed parent, so "not
//	                 in state" is their normal condition. Every RDS cluster here sets
//	                 copy_tags_to_snapshot, so automated backups carry the run's tags.
//	index-lag        the Resource Groups Tagging API is known to keep returning these
//	                 after deletion (janitor.go's insufficientAloneTypes, measured: 5 of
//	                 23 tagged security groups were already InvalidGroup.NotFound).
//	unmanaged        CloudPrem's terraform never creates this type, so its presence
//	                 says nothing about terraform having lost anything.
//
// Order matters: service-created and index-lag are checked FIRST, because several of
// those types (ec2:volume, ec2:security-group) are also terraform-managed elsewhere in
// the stack and would otherwise be enforced on.
func classifyResidual(awsType string) (blocking bool, why string) {
	switch {
	case serviceCreatedTypes[awsType]:
		return false, "service-created type; AWS makes and tags these from a managed parent"
	case insufficientAloneTypes[awsType]:
		return false, "type is known to linger in the tagging index after deletion"
	case terraformManagedTypes[awsType]:
		return true, ""
	}
	return false, "unclassified: CloudPrem terraform does not manage this resource type"
}

// indexState reads `terragrunt show -json` into a type-aware index. It walks
// child_modules recursively and keys every resource by its terraform type, which the
// first version of this code discarded.
func indexState(b []byte, idx *stateIndex) error {
	var doc struct {
		Values struct {
			RootModule json.RawMessage `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return fmt.Errorf("parse show -json: %w", err)
	}
	if len(doc.Values.RootModule) == 0 {
		// An empty state shows no root_module. That is a real answer (terraform owns
		// nothing), not a parse failure.
		return nil
	}
	var walk func(raw json.RawMessage) error
	walk = func(raw json.RawMessage) error {
		var mod struct {
			Resources []struct {
				Type   string                 `json:"type"`
				Values map[string]interface{} `json:"values"`
			} `json:"resources"`
			ChildModules []json.RawMessage `json:"child_modules"`
		}
		if err := json.Unmarshal(raw, &mod); err != nil {
			return err
		}
		for _, r := range mod.Resources {
			awsType := tfTypeToAWSType[r.Type]
			fields := append([]string{"arn"}, primaryARNFields[r.Type]...)
			for _, key := range fields {
				if s, ok := r.Values[key].(string); ok && strings.HasPrefix(s, "arn:") {
					idx.arns[s] = struct{}{}
					// An ARN also teaches the index this type exists, even when the
					// terraform type is unmapped - the ARN itself names the AWS type.
					if _, t := arnResourceType(s); t != "" {
						idx.types[t] = struct{}{}
						idx.add(t, arnResourceID(s))
					}
				}
			}
			if awsType == "" {
				continue
			}
			// The type is known: record its bare id under that type, and record that
			// state can speak for the type even when this resource has no usable id.
			idx.types[awsType] = struct{}{}
			for _, key := range []string{"id", "identifier", "cluster_identifier", "name", "bucket"} {
				if s, ok := r.Values[key].(string); ok && s != "" && !strings.HasPrefix(s, "arn:") {
					idx.add(awsType, s)
				}
			}
		}
		for _, child := range mod.ChildModules {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(doc.Values.RootModule); err != nil {
		return fmt.Errorf("walk show -json modules: %w", err)
	}
	return nil
}

// arnIsAccountedFor reports whether state knows about this ARN. Full-ARN match first
// (globally unique), then a bare-id match restricted to the ARN's OWN AWS type.
func arnIsAccountedFor(arn string, idx *stateIndex) bool {
	if idx == nil {
		return false
	}
	if _, ok := idx.arns[arn]; ok {
		return true
	}
	_, awsType := arnResourceType(arn)
	if awsType == "" {
		return false
	}
	ids, ok := idx.byType[awsType]
	if !ok {
		return false
	}
	tail := arnResourceID(arn)
	if tail == "" {
		// S3 and the other "resource part names the resource directly" ARNs, where
		// arnResourceID has no separator to split on.
		if parts := strings.SplitN(arn, ":", 6); len(parts) == 6 {
			tail = parts[5]
		}
	}
	if tail == "" {
		return false
	}
	if _, ok := ids[tail]; ok {
		return true
	}
	// Log-group ARNs come back from the tagging API with a trailing ":*" that the
	// terraform id does not carry.
	if trimmed := strings.TrimSuffix(tail, ":*"); trimmed != tail {
		if _, ok := ids[trimmed]; ok {
			return true
		}
	}
	// IAM ids drop the path: arn .../role/service-role/name has id "name".
	if i := strings.LastIndex(tail, "/"); i >= 0 {
		if _, ok := ids[tail[i+1:]]; ok {
			return true
		}
	}
	return false
}

// reconcileTagged diffs the live tagged ARNs against what state accounts for. Sorted so
// the report is stable across runs and diffable between retries.
func reconcileTagged(arns []string, idx *stateIndex) []Residual {
	var out []Residual
	seen := map[string]bool{}
	for _, arn := range arns {
		if arn == "" || seen[arn] || arnIsAccountedFor(arn, idx) {
			continue
		}
		seen[arn] = true
		_, resourceType := arnResourceType(arn)
		if resourceType == "" {
			resourceType = "unknown"
		}
		blocking, why := classifyResidual(resourceType)
		out = append(out, Residual{Source: "tag-reconcile", ARN: arn, Type: resourceType, Blocking: blocking, Why: why})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ARN < out[j].ARN })
	return out
}

// tfAddressType pulls the resource type out of a terraform address:
// module.vpc[0].aws_subnet.private[0] -> aws_subnet.
var tfAddressType = regexp.MustCompile(`(?:^|\.)((?:aws|null|random|kubernetes|helm|vault|time|tls)_[a-z0-9_]+)\.`)

// stateRemnants turns surviving `terragrunt state list` addresses into residuals. Only
// meaningful after a destroy: before one, everything in state is supposed to be there.
func stateRemnants(addresses []string) []Residual {
	var out []Residual
	for _, addr := range addresses {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		typ := "unknown"
		if m := tfAddressType.FindStringSubmatch(addr); len(m) == 2 {
			typ = m[1]
		}
		out = append(out, Residual{Source: "state-remnant", Address: addr, Type: typ})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// awsErrorClassRE picks the AWS API error code out of a provider error. These are the
// identities that separate "retry will fix it" from "something out of state is in the
// way": DependencyViolation and InvalidDBClusterStateFault are what smoke1bc2 spent two
// days re-emitting with nothing naming the dependency.
var awsErrorClassRE = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]*(?:Violation|Fault|Exception|InUse|NotFound|Denied|Conflict|StateException))\b`)

// destroyErrorClass extracts the error classes from a destroy failure, deduplicated and
// sorted. Supplementary colour on top of the two structured signals - never the thing
// the report is built from.
func destroyErrorClass(err error) string {
	if err == nil {
		return ""
	}
	found := map[string]bool{}
	for _, m := range awsErrorClassRE.FindAllStringSubmatch(err.Error(), -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		return truncate(err.Error(), 200)
	}
	classes := make([]string, 0, len(found))
	for c := range found {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	return strings.Join(classes, ",")
}

// ShowJSON returns `terragrunt show -json` for one module. Errors are the caller's to
// record as an incomplete signal, never to fail a phase on: a module whose state is
// already gone cannot be shown, and that is the SUCCESS case for a teardown.
func (o TGOptions) ShowJSON(module string) ([]byte, error) {
	cmd := exec.Command("terragrunt", "show", "-json", "--non-interactive")
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terragrunt show -json %s: %w", module, err)
	}
	return out, nil
}

// StateList returns the addresses `terragrunt state list` reports for one module.
func (o TGOptions) StateList(module string) ([]string, error) {
	cmd := exec.Command("terragrunt", "state", "list", "--non-interactive")
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terragrunt state list %s: %w", module, err)
	}
	var addrs []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			addrs = append(addrs, s)
		}
	}
	return addrs, nil
}

// residualModules is the layer order a check walks. logical first so its addresses read
// before physical's in the report, matching the destroy order.
var residualModules = []string{"logical", "physical"}

// checkResiduals builds the boundary report. customer is the SALTED customer value the
// apply actually used (rm.AppliedCustomer), because that is what the tags say; regions
// is the primary + DR pair, because residue lands in either.
//
// Never returns an error. Everything that goes wrong becomes an Incomplete entry, so
// the caller has exactly one thing to reason about: Clean() or not.
func (p PhaseParams) checkResiduals(ctx context.Context, tg TGOptions, phase, customer string, regions []string, destroyErr error) *ResidualReport {
	rep := &ResidualReport{
		Phase:      phase,
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		DestroyErr: destroyErrorClass(destroyErr),
	}

	// Signal 1: what does state account for.
	idx := newStateIndex()
	shown := 0
	for _, mod := range residualModules {
		out, err := tg.ShowJSON(mod)
		if err != nil {
			continue
		}
		if perr := indexState(out, idx); perr != nil {
			rep.Incomplete = append(rep.Incomplete, fmt.Sprintf("%s: %v", mod, perr))
			continue
		}
		shown++
	}

	// Signal 2: what survived a failed destroy, in terraform's own terms.
	if destroyErr != nil {
		for _, mod := range residualModules {
			addrs, err := tg.StateList(mod)
			if err != nil {
				continue
			}
			rep.Residuals = append(rep.Residuals, stateRemnants(addrs)...)
		}
	}

	// Signal 3: what is live and tagged but accounted for nowhere.
	switch {
	case customer == "":
		rep.Incomplete = append(rep.Incomplete, "no applied customer recorded; cannot enumerate this run's tagged resources")
	case len(p.Tags) == 0:
		rep.Incomplete = append(rep.Incomplete, "no tagging client configured; live resources were not enumerated")
	default:
		tagged, err := countTaggedDetailed(ctx, p.Tags, regions, customer)
		if err != nil {
			rep.Incomplete = append(rep.Incomplete, fmt.Sprintf("tag query: %v", err))
			break
		}
		// A provision that cannot show its own state has nothing to diff against, and
		// every tagged resource would read as a residual. A TEARDOWN that cannot is
		// the opposite: an empty state is exactly what a successful destroy leaves, so
		// it is the correct baseline and anything still tagged really is an orphan.
		// Getting this backwards made the post-teardown check unable to fail at all,
		// which is the one moment it exists for.
		if shown == 0 && phase != "teardown" {
			rep.Incomplete = append(rep.Incomplete, "terragrunt show -json returned nothing for any module; the tag reconcile has no state to diff against")
			break
		}
		rep.Residuals = append(rep.Residuals, reconcileTagged(tagged.arns, idx)...)
	}
	return rep
}

// recordResiduals persists the report on the manifest and prints it. Best-effort on the
// save: the report is worth more in the pod log than nowhere, and a failed save must not
// turn a detection into a second failure mode.
func (p PhaseParams) recordResiduals(ctx context.Context, cfg Config, rm *RunManifest, rep *ResidualReport) {
	rm.Residuals = rep
	if err := p.Store.Save(ctx, p.statePrefix(cfg), rm); err != nil {
		fmt.Fprintf(os.Stderr, ">> [harness] WARNING: could not record the residual report to the manifest: %v\n", err)
	}
}
