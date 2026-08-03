package validation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Feature-level in-cluster assertions for the optional stacks (BI; webhooks until the
// feature was removed).
//
// These are deliberately stricter than CheckClusterHealth. That check glob-matches a
// "critical set" and treats a pattern that matches nothing as merely unmatched, so a
// feature whose workloads never got created can still pass. Here the expected
// workloads are named exactly: absent is a failure, which is the whole point when the
// question is "did the feature flag actually produce a working tier".

// BIWorkloads are the BI/dashboards deployments (enable_bi). The grafana database
// bootstrap is asserted separately in AssertBIHealthy, version-aware.
func BIWorkloads() []string { return []string{"dozuki-grafana"} }

// AssertWorkloadsReady requires every named workload to EXIST and have all replicas
// ready before timeout. Waits rather than sampling once: these come up alongside the
// app and a feature tier is often the last thing to settle.
func AssertWorkloadsReady(kubeconfig, namespace, feature string, names []string, timeout time.Duration) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	started := time.Now()
	for {
		state, lerr := workloadReadiness(ctx, cs, namespace)
		if lerr != nil {
			return lerr
		}
		var missing, notReady []string
		for _, n := range names {
			r, ok := state[n]
			if !ok {
				missing = append(missing, n)
			} else if !r {
				notReady = append(notReady, n)
			}
		}
		if len(missing) == 0 && len(notReady) == 0 {
			fmt.Printf(">> [harness] %s: all %d workloads Ready (%s)\n", feature, len(names), time.Since(started).Round(time.Second))
			return nil
		}
		if time.Now().After(deadline) {
			sort.Strings(missing)
			sort.Strings(notReady)
			var b strings.Builder
			fmt.Fprintf(&b, "%s workloads not healthy within %s", feature, timeout)
			if len(missing) > 0 {
				// Absent (not merely unready) means the chart never rendered them —
				// the feature flag did not reach the release at all.
				fmt.Fprintf(&b, "; ABSENT (feature likely not enabled in the release): %s", strings.Join(missing, ", "))
			}
			if len(notReady) > 0 {
				fmt.Fprintf(&b, "; NOT READY: %s", strings.Join(notReady, ", "))
			}
			return fmt.Errorf("%s", b.String())
		}
		fmt.Printf(">> [harness] %s: waiting (missing=%d notReady=%d, %s elapsed)\n",
			feature, len(missing), len(notReady), time.Since(started).Round(time.Second))
		time.Sleep(20 * time.Second)
	}
}

func workloadReadiness(ctx context.Context, cs kubernetes.Interface, ns string) (map[string]bool, error) {
	out := map[string]bool{}
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		out[d.Name] = d.Status.ReadyReplicas == desired(d.Spec.Replicas)
	}
	sss, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range sss.Items {
		out[s.Name] = s.Status.ReadyReplicas == desired(s.Spec.Replicas)
	}
	return out, nil
}

// AssertNoCrashLoops fails if any pod whose name starts with one of prefixes has a
// container restart count above maxRestarts. A pod can report Ready while a sidecar
// restarts in a loop, and for the Kafka consumers a restart loop is the signature of
// a broker they cannot actually reach — which readiness alone would not catch.
func AssertNoCrashLoops(kubeconfig, namespace string, prefixes []string, maxRestarts int32) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	pods, err := cs.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	var bad []string
	for _, p := range pods.Items {
		if !hasPrefixAny(p.Name, prefixes) {
			continue
		}
		for _, cst := range append(append([]corev1.ContainerStatus{}, p.Status.ContainerStatuses...), p.Status.InitContainerStatuses...) {
			if cst.RestartCount > maxRestarts {
				reason := ""
				if cst.LastTerminationState.Terminated != nil {
					reason = " last=" + cst.LastTerminationState.Terminated.Reason
				}
				bad = append(bad, fmt.Sprintf("%s/%s restarts=%d%s", p.Name, cst.Name, cst.RestartCount, reason))
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("containers restarting above threshold %d: %s", maxRestarts, strings.Join(bad, "; "))
	}
	return nil
}

func hasPrefixAny(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// AssertBIHealthy verifies the BI tier: grafana is Ready and the grafana_primary
// database bootstrap ran. Two generations of bootstrap exist:
//   - CPI < 8.12: a TF-managed grafana-db-create Job; assert it completed
//     (proving the app database accepted the CREATE DATABASE).
//   - CPI >= 8.12 with chart >= 2.6.0: the chart's grafana-db-init pre-install/
//     pre-upgrade hook. Its Job deletes itself on success, and its success is
//     already implied by the HelmRelease reaching Ready (a failed hook fails the
//     release, which fails the phase long before validation runs). What is left
//     to assert is that the bootstrap machinery was rendered at all, which the
//     release-owned dozuki-grafana-db Secret proves; without it a misconfigured
//     env (grafanaDbInit disabled where BI is on) would pass silently.
func AssertBIHealthy(kubeconfig, namespace string, timeout time.Duration) error {
	if err := AssertWorkloadsReady(kubeconfig, namespace, "bi", BIWorkloads(), timeout); err != nil {
		return err
	}
	if err := JobSucceeded(kubeconfig, namespace, "grafana-db-create"); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("bi: grafana-db-create job: %w", err)
		}
		if err := SecretExists(kubeconfig, namespace, "dozuki-grafana-db"); err != nil {
			return fmt.Errorf("bi: neither the legacy grafana-db-create job nor the chart's dozuki-grafana-db secret exists; nothing bootstrapped grafana_primary: %w", err)
		}
	}
	return AssertNoCrashLoops(kubeconfig, namespace, []string{"dozuki-grafana"}, 3)
}
