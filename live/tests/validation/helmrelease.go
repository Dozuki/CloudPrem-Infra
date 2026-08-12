package validation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// Since CPI v7.19.0 the dozuki app is delivered by a Flux HelmRelease applied with
// kubectl_manifest, which returns as soon as the CR exists — the terragrunt apply no
// longer blocks on the install the way helm_release.app's wait=true did. So the harness
// has to establish readiness itself.
//
// Ask Flux rather than guessing a duration. helm-controller publishes the authoritative
// answer on the HelmRelease's status conditions, which beats waiting on a timer: we wait
// exactly as long as a slow-but-healthy install needs, and where the failure IS terminal
// (an install, or a pre-v9.2.2 baseline upgrade) we fail in seconds instead of burning
// the whole budget to reach the same conclusion. An upgrade running RetryOnFailure has no
// terminal state and does use the full timeout, but it still reports WHY, which is the
// part timer-based waiting can never do.

// The HelmRelease CR is created in flux-system (terraform/logical/flux.tf), NOT in the
// namespace the release deploys into.
const fluxNamespace = "flux-system"

var helmReleaseGVR = schema.GroupVersionResource{
	Group:    "helm.toolkit.fluxcd.io",
	Version:  "v2",
	Resource: "helmreleases",
}

// HelmReleaseManaged reports whether a Flux HelmRelease of this name exists. Older refs
// install the app through the TF helm provider and have no CR, so callers use this to
// decide whether the Flux-aware wait applies (relevant for upgrade runs from a
// pre-v7.19.0 baseline).
func HelmReleaseManaged(kubeconfig, namespace, name string) bool {
	dc, err := dynamicFor(kubeconfig)
	if err != nil {
		return false
	}
	_, _, err = findHelmRelease(context.Background(), dc, namespace, name)
	return err == nil
}

// findHelmRelease locates the HelmRelease by name, WITHOUT assuming which namespace holds
// it. The CR lives in flux-system while the release it manages targets the app namespace
// (spec.targetNamespace / storageNamespace), so looking only in the app namespace finds
// nothing — which is exactly what happened: HelmReleaseManaged silently returned false on
// every run and the readiness wait never executed once. Fresh runs won the race anyway;
// an upgrade then caught the release mid-flight as "pending-upgrade".
//
// Searched in order: flux-system, the app namespace (older layouts), then cluster-wide.
// The cluster-wide sweep is the backstop that keeps this from breaking again if the CR
// moves; a not-found is still a real answer (no Flux, e.g. a pre-v7.19.0 baseline).
func findHelmRelease(ctx context.Context, dc dynamic.Interface, appNamespace, name string) (*unstructured.Unstructured, string, error) {
	for _, ns := range []string{fluxNamespace, appNamespace} {
		if ns == "" {
			continue
		}
		hr, err := dc.Resource(helmReleaseGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			return hr, ns, nil
		}
		if !errors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return nil, "", err
		}
	}
	list, err := dc.Resource(helmReleaseGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", err
	}
	for i := range list.Items {
		if list.Items[i].GetName() == name {
			return &list.Items[i], list.Items[i].GetNamespace(), nil
		}
	}
	return nil, "", errors.NewNotFound(schema.GroupResource{Group: helmReleaseGVR.Group, Resource: helmReleaseGVR.Resource}, name)
}

// AwaitHelmReleaseReady blocks until Flux reports the HelmRelease Ready, or returns the
// failure Flux recorded. timeout is not a readiness estimate: for a terminal failure it
// is a backstop against a controller that never reconciles at all, and for an upgrade
// retrying forever it is the only thing that ends the wait.
//
// Since CPI v9.2.2 the upgrade path runs spec.upgrade.strategy RetryOnFailure with no
// remediation block, so a failed UPGRADE is no longer terminal: it never sets Stalled
// and never drives Ready to False, it parks at Ready=Unknown and retries every
// retryInterval. Ready is therefore a lying signal during a retry loop. The real cause
// is on the Released condition, so we read that too: a Released=False upgrade is
// reported as it happens and recorded, but not fast-failed, because the next retry may
// genuinely fix it. Install is unchanged (retries = 0), and a first-install failure
// still resolves to the install config forever, so the Stalled / Ready=False fast-fail
// below is still the path that catches it.
func AwaitHelmReleaseReady(kubeconfig, namespace, name string, timeout time.Duration) error {
	dc, err := dynamicFor(kubeconfig)
	if err != nil {
		return err
	}
	ctx := context.Background()
	started := time.Now()
	deadline := started.Add(timeout)
	lastMsg := ""
	lastFailure := ""
	lastUnscopedFailure := ""
	lastGen := int64(-1)

	for {
		hr, _, gerr := findHelmRelease(ctx, dc, namespace, name)
		if gerr != nil {
			if !errors.IsNotFound(gerr) {
				return fmt.Errorf("get HelmRelease %s/%s: %w", namespace, name, gerr)
			}
			// Not created yet — keep waiting rather than failing.
		} else {
			ready, reason, msg, _ := condition(hr, "Ready")
			stalled, sReason, sMsg, _ := condition(hr, "Stalled")
			released, rReason, rMsg, rGen := condition(hr, "Released")

			// A new generation means a new spec attempt, so anything recorded about the
			// previous one no longer describes what we are waiting for.
			if gen := hr.GetGeneration(); gen != lastGen {
				lastGen = gen
				lastFailure = ""
			}

			if ready == "True" {
				fmt.Fprintf(os.Stderr, ">> [harness %s] HelmRelease %s Ready (%s) — %s\n",
					time.Now().Format("15:04:05"), name, time.Since(started).Round(time.Second), reason)
				return nil
			}

			// Terminal failures. Install still runs remediation.retries = 0, and a release
			// with no successful history stays on the install config forever, so these two
			// checks remain final: waiting longer cannot change the answer, and the message
			// names the actual cause (a failed hook, an unready workload, a values error).
			// They also still catch a pre-v9.2.2 baseline, which has no retry strategy and
			// does drive Ready to False on a failed upgrade.
			if stalled == "True" {
				return fmt.Errorf("HelmRelease %s stalled (%s): %s", name, sReason, firstLine(sMsg))
			}
			if ready == "False" && isTerminalReason(reason) {
				return fmt.Errorf("HelmRelease %s failed (%s): %s", name, reason, firstLine(msg))
			}

			// Retryable but reported. Under RetryOnFailure the only honest record of a
			// failed upgrade is Released=False, so surface it the moment it changes and
			// keep it for the timeout message, but do not fast-fail on it: the next retry
			// may succeed.
			//
			// Only a condition whose observedGeneration matches the generation we are
			// waiting on can be attributed to it. Flux sets that field, so this is the
			// normal path. If it is ever missing we cannot tell whether the error belongs
			// to this attempt or a previous one, and neither silently claiming it nor
			// silently dropping it is honest — dropping it would restore the original
			// empty verdict. Keep it apart and say what it is when reporting.
			if rReason != "" && released == "False" {
				f := fmt.Sprintf("%s: %s", rReason, firstLine(rMsg))
				switch {
				case rGen == lastGen && f != lastFailure:
					lastFailure = f
					fmt.Fprintf(os.Stderr, ">> [harness %s] HelmRelease %s release failed, retrying: %s\n",
						time.Now().Format("15:04:05"), name, f)
				case rGen == -1 && f != lastUnscopedFailure:
					lastUnscopedFailure = f
					fmt.Fprintf(os.Stderr, ">> [harness %s] HelmRelease %s release failed (generation unknown): %s\n",
						time.Now().Format("15:04:05"), name, f)
				}
			}
			if released == "True" && rGen == lastGen {
				lastFailure = ""
				lastUnscopedFailure = ""
			}

			if m := firstLine(msg); m != "" && m != lastMsg {
				fmt.Fprintf(os.Stderr, ">> [harness %s] HelmRelease %s: %s — %s\n",
					time.Now().Format("15:04:05"), name, reason, m)
				lastMsg = m
			}
		}

		if time.Now().After(deadline) {
			switch {
			case lastFailure != "":
				return fmt.Errorf("HelmRelease %s still failing under the retry strategy after %s (last release error %s)", name, timeout, lastFailure)
			case lastUnscopedFailure != "":
				return fmt.Errorf("HelmRelease %s produced no verdict within %s; an unscoped Released error was seen and may predate the current generation (%s)", name, timeout, lastUnscopedFailure)
			}
			return fmt.Errorf("HelmRelease %s produced no verdict within %s (last: %s)", name, timeout, lastMsg)
		}
		time.Sleep(20 * time.Second)
	}
}

// isTerminalReason lists the helm-controller reasons that will not resolve on their own
// once they reach the Ready condition as False. Anything else (Progressing,
// ArtifactFailed while the source is still pulling, ...) is treated as in-flight and
// waited out. UpgradeFailed stays in this set on purpose: it is unreachable on a release
// running RetryOnFailure (which is why the Released check above exists), but it is still
// how a pre-v9.2.2 baseline reports a dead upgrade, and the harness runs upgrade tests
// from those baselines.
func isTerminalReason(reason string) bool {
	switch reason {
	case "InstallFailed", "UpgradeFailed", "TestFailed", "RollbackFailed", "UninstallFailed", "ChartPullFailed":
		return true
	}
	return false
}

// condition returns the named condition's status, reason and message, plus the generation
// the controller had observed when it wrote it. That last value is what tells a caller
// whether the condition describes the spec currently being waited on or a previous one;
// it is -1 when the condition is absent or carries no observedGeneration.
func condition(hr *unstructured.Unstructured, condType string) (status, reason, message string, observedGeneration int64) {
	conds, found, err := unstructured.NestedSlice(hr.Object, "status", "conditions")
	if err != nil || !found {
		return "", "", "", -1
	}
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if s, _ := cm["type"].(string); s != condType {
			continue
		}
		status, _ = cm["status"].(string)
		reason, _ = cm["reason"].(string)
		message, _ = cm["message"].(string)
		// The dynamic client decodes JSON integers as int64, but tolerate float64 so a
		// different decode path cannot silently turn this into "generation unknown".
		observedGeneration = -1
		switch g := cm["observedGeneration"].(type) {
		case int64:
			observedGeneration = g
		case float64:
			observedGeneration = int64(g)
		}
		return status, reason, message, observedGeneration
	}
	return "", "", "", -1
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func dynamicFor(kubeconfig string) (dynamic.Interface, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}
