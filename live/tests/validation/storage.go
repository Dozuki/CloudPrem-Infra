package validation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Regression guards from the AZ/PVC adversarial review (2026-07-28). Two invariants that
// were true only by accident, each one silent when it breaks:
//
// The default StorageClass must exist and bind WaitForFirstConsumer. Auto Mode creates no
// default of its own - on the live cluster there was a real window between cluster birth
// and the logical layer's ebs-gp3 where the only class was a dead in-tree gp2 and nothing
// was default. Every PVC in the stack omits storageClassName, so if the default vanished
// (or someone flipped it to Immediate, the classic pre-bind stranding cause), claims
// would either pend unbound forever or bind to a random AZ before scheduling. Nothing
// else in the run asks this question; a deploy can go green without it.
//
// No pod that mounts a PVC may sit on a spot node. The volume pins the pod to one AZ for
// life; spot is the capacity that evaporates AZ-wide. The combination reproduced the old
// spot-fleet stranding incident, and the fix (on-demand pinning via CPI values) is
// exactly the kind of wiring a chart or values refactor can silently drop - the pods
// still schedule, still pass health checks, and the regression only surfaces as an
// unbounded Pending during some future ICE event.

// AssertDefaultStorageClassWFFC verifies exactly one default StorageClass exists and it
// binds WaitForFirstConsumer.
func AssertDefaultStorageClassWFFC(kubeconfig string) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	scs, err := cs.StorageV1().StorageClasses().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	var defaults []storagev1.StorageClass
	for _, sc := range scs.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			defaults = append(defaults, sc)
		}
	}
	switch len(defaults) {
	case 0:
		return fmt.Errorf("no default StorageClass: every PVC in the stack omits storageClassName, so claims would pend unbound")
	case 1:
		// fall through
	default:
		names := make([]string, len(defaults))
		for i, sc := range defaults {
			names[i] = sc.Name
		}
		sort.Strings(names)
		return fmt.Errorf("multiple default StorageClasses (%s): binding depends on apiserver tie-breaking", strings.Join(names, ", "))
	}
	sc := defaults[0]
	if sc.VolumeBindingMode == nil || *sc.VolumeBindingMode != storagev1.VolumeBindingWaitForFirstConsumer {
		mode := "unset (Immediate)"
		if sc.VolumeBindingMode != nil {
			mode = string(*sc.VolumeBindingMode)
		}
		return fmt.Errorf("default StorageClass %s has volumeBindingMode %s, want WaitForFirstConsumer - Immediate binds volumes to an AZ before scheduling, the classic stranding cause", sc.Name, mode)
	}
	return nil
}

// AssertNoPVCPodsOnSpot fails if any pod mounting a PersistentVolumeClaim is running on a
// node whose karpenter.sh/capacity-type is "spot". Checked cluster-wide, not just the app
// namespace - a PVC anywhere carries the same stranding risk.
func AssertNoPVCPodsOnSpot(kubeconfig string) error {
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return err
	}
	ctx := context.Background()

	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	capacityType := map[string]string{}
	for _, n := range nodes.Items {
		capacityType[n.Name] = n.Labels["karpenter.sh/capacity-type"]
	}

	pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var offenders []string
	for _, p := range pods.Items {
		if p.Spec.NodeName == "" || capacityType[p.Spec.NodeName] != "spot" {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				offenders = append(offenders, fmt.Sprintf("%s/%s (pvc %s, node %s)",
					p.Namespace, p.Name, v.PersistentVolumeClaim.ClaimName, p.Spec.NodeName))
				break
			}
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		return fmt.Errorf("PVC-mounting pods on SPOT nodes (volume pins the AZ; spot is the capacity that evaporates AZ-wide): %s",
			strings.Join(offenders, "; "))
	}
	return nil
}
