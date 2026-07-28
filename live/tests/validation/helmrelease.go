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
// answer on the HelmRelease's status conditions, which beats waiting on a timer in both
// directions: we wait exactly as long as a slow-but-healthy install needs, and we fail
// in seconds on a broken one instead of burning the whole budget to reach the same
// conclusion. Timer-based waiting can only ever report "still not ready", never why.

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
// failure Flux recorded. timeout bounds only the wait for a verdict — it is a backstop
// for a controller that never reconciles at all, not a readiness estimate.
func AwaitHelmReleaseReady(kubeconfig, namespace, name string, timeout time.Duration) error {
	dc, err := dynamicFor(kubeconfig)
	if err != nil {
		return err
	}
	ctx := context.Background()
	started := time.Now()
	deadline := started.Add(timeout)
	lastMsg := ""

	for {
		hr, _, gerr := findHelmRelease(ctx, dc, namespace, name)
		if gerr != nil {
			if !errors.IsNotFound(gerr) {
				return fmt.Errorf("get HelmRelease %s/%s: %w", namespace, name, gerr)
			}
			// Not created yet — keep waiting rather than failing.
		} else {
			ready, reason, msg := condition(hr, "Ready")
			stalled, sReason, sMsg := condition(hr, "Stalled")

			if ready == "True" {
				fmt.Fprintf(os.Stderr, ">> [harness %s] HelmRelease %s Ready (%s) — %s\n",
					time.Now().Format("15:04:05"), name, time.Since(started).Round(time.Second), reason)
				return nil
			}

			// Terminal failures. CPI sets remediation.retries = 0, so an install/upgrade
			// failure is final: waiting longer cannot change it, and the message names the
			// actual cause (a failed hook, an unready workload, a values error).
			if stalled == "True" {
				return fmt.Errorf("HelmRelease %s stalled (%s): %s", name, sReason, firstLine(sMsg))
			}
			if ready == "False" && isTerminalReason(reason) {
				return fmt.Errorf("HelmRelease %s failed (%s): %s", name, reason, firstLine(msg))
			}

			if m := firstLine(msg); m != "" && m != lastMsg {
				fmt.Fprintf(os.Stderr, ">> [harness %s] HelmRelease %s: %s — %s\n",
					time.Now().Format("15:04:05"), name, reason, m)
				lastMsg = m
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("HelmRelease %s produced no verdict within %s (last: %s)", name, timeout, lastMsg)
		}
		time.Sleep(20 * time.Second)
	}
}

// isTerminalReason lists the helm-controller reasons that will not resolve on their own
// under remediation.retries = 0. Anything else (Progressing, ArtifactFailed while the
// source is still pulling, ...) is treated as in-flight and waited out.
func isTerminalReason(reason string) bool {
	switch reason {
	case "InstallFailed", "UpgradeFailed", "TestFailed", "RollbackFailed", "UninstallFailed", "ChartPullFailed":
		return true
	}
	return false
}

func condition(hr *unstructured.Unstructured, condType string) (status, reason, message string) {
	conds, found, err := unstructured.NestedSlice(hr.Object, "status", "conditions")
	if err != nil || !found {
		return "", "", ""
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
		return status, reason, message
	}
	return "", "", ""
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
