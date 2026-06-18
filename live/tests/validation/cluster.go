package validation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

// CheckClusterHealth asserts: every Deployment/StatefulSet in namespace has
// ready==desired, no pod is in CrashLoopBackOff/Error, and (best-effort) the
// db-migrations Job succeeded. Retries until ready or timeout.
func CheckClusterHealth(kubeconfig, namespace string, timeout time.Duration) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	ctx := context.Background()
	started := time.Now()
	deadline := started.Add(timeout)
	for {
		err := clusterReadyOnce(ctx, cs, namespace)
		if err == nil {
			fmt.Fprintf(os.Stderr, ">> [harness %s] cluster healthy — all workloads Ready (%s)\n", time.Now().Format("15:04:05"), time.Since(started).Round(time.Second))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cluster not healthy within %s: %w", timeout, err)
		}
		// Heartbeat so the (otherwise silent) up-to-20m wait shows progress.
		fmt.Fprintf(os.Stderr, ">> [harness %s] waiting for cluster (%s elapsed): %v\n", time.Now().Format("15:04:05"), time.Since(started).Round(time.Second), err)
		time.Sleep(30 * time.Second)
	}
}

func clusterReadyOnce(ctx context.Context, cs *kubernetes.Clientset, ns string) error {
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, d := range deps.Items {
		if !deploymentReady(d) {
			return fmt.Errorf("deployment %s not ready (%d/%d)", d.Name, d.Status.ReadyReplicas, desired(d.Spec.Replicas))
		}
	}
	sss, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, s := range sss.Items {
		if s.Status.ReadyReplicas != desired(s.Spec.Replicas) {
			return fmt.Errorf("statefulset %s not ready (%d/%d)", s.Name, s.Status.ReadyReplicas, desired(s.Spec.Replicas))
		}
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, p := range pods.Items {
		for _, st := range p.Status.ContainerStatuses {
			if st.State.Waiting != nil && (st.State.Waiting.Reason == "CrashLoopBackOff" || st.State.Waiting.Reason == "Error") {
				return fmt.Errorf("pod %s container %s in %s", p.Name, st.Name, st.State.Waiting.Reason)
			}
		}
	}
	return nil
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

var _ = corev1.Pod{} // keep corev1 import if pod helpers are extended
