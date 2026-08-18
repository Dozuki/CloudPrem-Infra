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
}

// ResidualReport is one boundary check. It lands in the run manifest, which outlives
// the Workflow CR (Argo TTLs a failed one after three days), so the identities survive
// the pod, the workflow and the retry that produced them.
type ResidualReport struct {
	Phase      string     `json:"phase"`                 // "provision" or "teardown"
	CheckedAt  string     `json:"checked_at"`            // RFC3339 UTC
	DestroyErr string     `json:"destroy_error,omitempty"` // error class, when a destroy failed
	Residuals  []Residual `json:"residuals,omitempty"`
	// Incomplete records that a signal could not be gathered (no tagging client, a
	// `show -json` that would not run). A report that silently dropped a signal reads
	// identical to a clean one, and this file exists because a clean-looking report on
	// a leaking stack is the expensive failure.
	Incomplete []string `json:"incomplete,omitempty"`
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
	fmt.Fprintf(&b, "%d residual(s) after %s", len(r.Residuals), r.Phase)
	for _, res := range r.Residuals {
		id := res.ARN
		if id == "" {
			id = res.Address
		}
		fmt.Fprintf(&b, "\n  - [%s] %s %s", res.Source, res.Type, id)
	}
	for _, inc := range r.Incomplete {
		fmt.Fprintf(&b, "\n  - [incomplete] %s", inc)
	}
	return b.String()
}

// physicalIDsFromShow collects every physical identifier `terragrunt show -json`
// records, so a live ARN can be matched against what terraform believes it owns.
//
// Matching physical ids, never addresses: an address (module.aurora[0].aws_rds_cluster
// _instance.this["writer"]) and an ARN describe the same resource in two vocabularies
// that share no substring, so comparing them would report every single resource as a
// residual. id and arn are the two attributes terraform stores in AWS's vocabulary.
func physicalIDsFromShow(b []byte) (map[string]struct{}, error) {
	var doc struct {
		Values struct {
			RootModule json.RawMessage `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse show -json: %w", err)
	}
	ids := map[string]struct{}{}
	if len(doc.Values.RootModule) == 0 {
		// An empty state shows no root_module at all. That is a real answer (nothing is
		// owned), not a parse failure - every live tagged resource is then a residual.
		return ids, nil
	}
	var walk func(raw json.RawMessage) error
	walk = func(raw json.RawMessage) error {
		var mod struct {
			Resources []struct {
				Values map[string]interface{} `json:"values"`
			} `json:"resources"`
			ChildModules []json.RawMessage `json:"child_modules"`
		}
		if err := json.Unmarshal(raw, &mod); err != nil {
			return err
		}
		for _, r := range mod.Resources {
			for _, key := range []string{"id", "arn"} {
				if s, ok := r.Values[key].(string); ok && s != "" {
					ids[s] = struct{}{}
				}
			}
			// A few resources carry a list rather than a scalar (aws_subnets.ids,
			// module outputs replayed as arns). Cheap to include; a false "accounted
			// for" is far less costly than a false residual, which fails a run.
			for _, key := range []string{"ids", "arns"} {
				if l, ok := r.Values[key].([]interface{}); ok {
					for _, v := range l {
						if s, ok := v.(string); ok && s != "" {
							ids[s] = struct{}{}
						}
					}
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
		return nil, fmt.Errorf("walk show -json modules: %w", err)
	}
	return ids, nil
}

// arnIsAccountedFor reports whether state knows about this ARN, by the ARN itself or by
// the bare id terraform more often stores (vpc-0..., sg-0..., smoke1bc2-bi-writer).
func arnIsAccountedFor(arn string, ids map[string]struct{}) bool {
	if _, ok := ids[arn]; ok {
		return true
	}
	if tail := arnResourceID(arn); tail != "" {
		if _, ok := ids[tail]; ok {
			return true
		}
	}
	return false
}

// reconcileTagged diffs the live tagged ARNs against what state accounts for. Sorted so
// the report is stable across runs and diffable between retries.
func reconcileTagged(arns []string, ids map[string]struct{}) []Residual {
	var out []Residual
	seen := map[string]bool{}
	for _, arn := range arns {
		if arn == "" || seen[arn] || arnIsAccountedFor(arn, ids) {
			continue
		}
		seen[arn] = true
		_, resourceType := arnResourceType(arn)
		if resourceType == "" {
			resourceType = "unknown"
		}
		out = append(out, Residual{Source: "tag-reconcile", ARN: arn, Type: resourceType})
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
	ids := map[string]struct{}{}
	shown := 0
	for _, mod := range residualModules {
		out, err := tg.ShowJSON(mod)
		if err != nil {
			// Expected on a clean teardown (state gone). Only worth recording as
			// incomplete when we got nothing at all from any module, handled below.
			continue
		}
		modIDs, perr := physicalIDsFromShow(out)
		if perr != nil {
			rep.Incomplete = append(rep.Incomplete, fmt.Sprintf("%s: %v", mod, perr))
			continue
		}
		shown++
		for id := range modIDs {
			ids[id] = struct{}{}
		}
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
		} else {
			if shown == 0 && destroyErr == nil && len(tagged.arns) > 0 {
				// No state to compare against and a successful phase: every tagged
				// resource would look like a residual. Say so rather than emit a
				// report that is technically true and useless.
				rep.Incomplete = append(rep.Incomplete, "terragrunt show -json returned nothing for any module; the tag reconcile has no state to diff against")
			} else {
				rep.Residuals = append(rep.Residuals, reconcileTagged(tagged.arns, ids)...)
			}
		}
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
