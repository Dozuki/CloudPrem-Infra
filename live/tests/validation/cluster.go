package validation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Kubeconfig writes a kubeconfig for the EKS cluster using the AWS CLI and
// returns its path. Caller removes the file.
func Kubeconfig(clusterName, region, profile, dir string) (string, error) {
	path := dir + "/kubeconfig"
	cmd := exec.Command("aws", "eks", "update-kubeconfig",
		"--name", clusterName, "--region", region, "--profile", profile,
		"--kubeconfig", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("update-kubeconfig: %v: %s", err, out)
	}
	return path, nil
}

func clientFor(kubeconfig string) (*kubernetes.Clientset, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// matchesAny reports whether name matches any glob pattern (filepath.Match).
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

// CheckClusterHealth blocks (up to timeout) until every workload whose name matches
// the critical set reports ready==desired, then returns the names of NON-critical
// workloads that are not ready (advisory; never an error). A critical workload absent
// from the cluster is an error.
func CheckClusterHealth(kubeconfig, namespace string, critical []string, timeout time.Duration) ([]string, error) {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	started := time.Now()
	deadline := started.Add(timeout)
	for {
		advisory, matched, ready, err := evaluateWorkloads(ctx, cs, namespace, critical)
		if err != nil {
			return nil, err
		}
		if ready {
			fmt.Fprintf(os.Stderr, ">> [harness %s] critical workloads Ready (%s)\n", time.Now().Format("15:04:05"), time.Since(started).Round(time.Second))
			return advisory, nil
		}
		if time.Now().After(deadline) {
			if len(critical) > 0 && !matched {
				return nil, fmt.Errorf("no workload matched the critical set %v within %s (expected the release to deploy one)", critical, timeout)
			}
			return nil, fmt.Errorf("critical workloads not ready within %s", timeout)
		}
		fmt.Fprintf(os.Stderr, ">> [harness %s] waiting for critical workloads (%s elapsed)\n", time.Now().Format("15:04:05"), time.Since(started).Round(time.Second))
		time.Sleep(30 * time.Second)
	}
}

// evaluateWorkloads inspects current Deployments+StatefulSets and reports: the
// not-ready non-critical (advisory) workloads, whether any critical pattern matched
// a workload (matched), and whether the critical set is satisfied (ready). It only
// errors on an API list failure — "no critical match" is surfaced by the caller at
// timeout, so a not-yet-listed critical workload is waited for rather than failed.
func evaluateWorkloads(ctx context.Context, cs kubernetes.Interface, ns string, critical []string) (advisory []string, matched bool, ready bool, err error) {
	type wl struct {
		name           string
		ready, desired int32
	}
	var all []wl
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false, false, err
	}
	for _, d := range deps.Items {
		all = append(all, wl{d.Name, d.Status.ReadyReplicas, desired(d.Spec.Replicas)})
	}
	sss, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false, false, err
	}
	for _, s := range sss.Items {
		all = append(all, wl{s.Name, s.Status.ReadyReplicas, desired(s.Spec.Replicas)})
	}

	allCriticalReady := true
	for _, w := range all {
		isReady := w.ready == w.desired
		if matchesAny(w.name, critical) {
			matched = true
			if !isReady {
				allCriticalReady = false
			}
		} else if !isReady {
			advisory = append(advisory, w.name)
		}
	}
	if len(critical) == 0 {
		ready = true // no critical gate configured
	} else {
		ready = matched && allCriticalReady
	}
	return advisory, matched, ready, nil
}

func deploymentReady(d appsv1.Deployment) bool {
	return d.Status.ReadyReplicas == desired(d.Spec.Replicas)
}

func desired(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

// JobSucceeded checks that a named Job completed successfully.
func JobSucceeded(kubeconfig, namespace, name string) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	j, err := cs.BatchV1().Jobs(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if j.Status.Succeeded < 1 {
		return fmt.Errorf("job %s has not succeeded (succeeded=%d)", name, j.Status.Succeeded)
	}
	return nil
}
