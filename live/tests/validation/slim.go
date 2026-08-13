package validation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Slim-image flip assertions, run ONLY from the standalone Validate phase - never
// from Provision's validateStack. validateStack's health gate is generic (does the
// release come up); it runs on both a legacy baseline and a slim target, so an
// assertion pinned to slim's exact image layout would fail every legacy run it
// touches. This file's checks are deliberately kept out of that path.
//
// They ask a narrower, harder-to-fake question than "the release is Ready": which
// images are actually running. A stuck rollout or a chart that silently kept
// legacy's monolith-app defaults can leave the cluster fully Ready while serving
// the wrong image entirely - exactly the silent-legacy-fallback case assertion 2
// exists to catch.

// SlimFlipApplies mirrors the exact gate Validate wraps its call to
// AssertSlimFlipComplete in: EffectiveVersionVar(cfg, rm.ToRef, target side,
// "app_image_flavor") == "slim". Exported as its own pure predicate so that gate -
// every other flavor value, including empty/unset - is a no-op, has a hermetic
// test independent of Validate's much heavier integration surface (worktrees,
// manifest store, terragrunt, a live cluster).
func SlimFlipApplies(flavor string) bool {
	return flavor == "slim"
}

// AssertSlimFlipComplete is the production wrapper Validate calls once the generic
// cluster-health gate has already passed: builds a real clientset from the
// kubeconfig validateStack generated, lists every pod in namespace, and asserts the
// four slim-flip conditions. See assertSlimImages for the testable core.
func AssertSlimFlipComplete(ctx context.Context, kubeconfig, namespace, imageRepository, imageTag, beanstalkdTag, nextjsTag string) error {
	if namespace == "" {
		// client-go lists ALL namespaces when given "" - silently defaulting to
		// "dozuki" here would hide that mistake instead of surfacing it.
		return fmt.Errorf("slim-flip guard: namespace is empty — refusing to list pods cluster-wide")
	}
	cs, err := clientFor(kubeconfig)
	if err != nil {
		return fmt.Errorf("slim-flip guard: build client: %w", err)
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("slim-flip guard: list pods: %w", err)
	}
	return assertSlimImages(pods.Items, imageRepository, imageTag, beanstalkdTag, nextjsTag)
}

// eligiblePod reports whether pod counts toward the slim-flip inventory at all:
// phase Running or Succeeded, AND not already terminating. Phase alone excludes
// Failed pods, which is also how an Evicted pod reports (phase Failed, reason
// Evicted) - there is no separate check needed for it. The DeletionTimestamp check
// excludes a pod that is on its way out but hasn't flipped phase yet: a Terminating
// pod can sit at phase Running with a stale Ready=True condition for several seconds,
// and it is exactly the kind of leftover a stray/unrelated pod set (a prior release's
// pod not yet garbage-collected, a manually-run debug pod, ...) would use to make
// this assertion pass on evidence that doesn't describe the current rollout.
func eligiblePod(pod corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	return pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodSucceeded
}

// podReady reports the pod's own PodReady condition - the same signal kubectl's
// READY column reflects - not merely "phase is Running". A pod can be Running with
// containers still failing readiness probes.
func podReady(pod corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// nonInitImages returns pod's spec.containers images only - init containers are
// deliberately excluded, per the assertions' definition of what counts.
func nonInitImages(pod corev1.Pod) []string {
	imgs := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		imgs = append(imgs, c.Image)
	}
	return imgs
}

// assertSlimImages is the testable core of AssertSlimFlipComplete: the four
// slim-flip assertions against an already-collected pod list, no cluster required.
// Tag-string match only - this never resolves or compares runtime imageID digests,
// which is proof-run evidence handled elsewhere, not by this check. No mid-rollout
// assertions are made; only the final state (as of this single snapshot) is
// asserted, so a settled-but-still-catching-up call is expected to have already
// passed the generic cluster-health gate before this runs.
//
//  1. At least 1 Ready pod runs <repo>/monolith-app:<imageTag> (the app Deployment).
//  2. NO pod runs a <repo>/monolith-app: image on any OTHER tag - the
//     silent-legacy-fallback catch: legacy's app path is a different repo
//     entirely, so "zero monolith-app pods at all" must fail assertion 1 rather
//     than vacuously pass this one.
//  3. At least 1 Ready pod runs <repo>/beanstalkd:<beanstalkdTag> (the beanstalkd
//     StatefulSet's sole pod, dozuki-beanstalkd-deployment-0 - "deployment" in the
//     pod name is legacy naming; the workload itself is a StatefulSet).
//  4. At least 1 Ready pod runs <repo>/web-nextjs:<nextjsTag> (the nextjs
//     Deployment).
func assertSlimImages(pods []corev1.Pod, imageRepository, imageTag, beanstalkdTag, nextjsTag string) error {
	// Each of these is a distinct version-var key resolved via EffectiveVersionVar
	// (harness/phases.go Validate) for the target side of rm.ToRef. Naming the exact
	// key here - rather than letting an empty value fall through to a confusing
	// "no Ready pod runs <repo>/monolith-app:" (trailing colon, no tag) message below -
	// is what makes a matrix.yaml gap (a slim config missing one of these keys)
	// diagnosable from the error text alone.
	if imageRepository == "" {
		return fmt.Errorf("slim-flip guard: config resolves no image_repository for the target side — cannot verify the flip")
	}
	if imageTag == "" {
		return fmt.Errorf("slim-flip guard: config resolves no image_tag for the target side — cannot verify the flip")
	}
	if beanstalkdTag == "" {
		return fmt.Errorf("slim-flip guard: config resolves no beanstalkd_tag for the target side — cannot verify the flip")
	}
	if nextjsTag == "" {
		return fmt.Errorf("slim-flip guard: config resolves no nextjs_tag for the target side — cannot verify the flip")
	}

	monolithPrefix := imageRepository + "/monolith-app:"
	wantMonolith := monolithPrefix + imageTag
	wantBeanstalkd := imageRepository + "/beanstalkd:" + beanstalkdTag
	wantNextjs := imageRepository + "/web-nextjs:" + nextjsTag

	var haveMonolith, haveBeanstalkd, haveNextjs bool
	var wrongMonolith []string

	for _, pod := range pods {
		if !eligiblePod(pod) {
			continue
		}
		ready := podReady(pod)
		for _, img := range nonInitImages(pod) {
			switch {
			case img == wantMonolith:
				if ready {
					haveMonolith = true
				}
			case strings.HasPrefix(img, monolithPrefix):
				// Wrong tag on the monolith-app repo - flagged regardless of
				// readiness, per the assertion's definition.
				wrongMonolith = append(wrongMonolith, fmt.Sprintf("%s=%s", pod.Name, img))
			}
			if ready && img == wantBeanstalkd {
				haveBeanstalkd = true
			}
			if ready && img == wantNextjs {
				haveNextjs = true
			}
		}
	}

	var problems []string
	if !haveMonolith {
		problems = append(problems, fmt.Sprintf("no Ready pod runs %s", wantMonolith))
	}
	if len(wrongMonolith) > 0 {
		sort.Strings(wrongMonolith)
		problems = append(problems, fmt.Sprintf("%d pod(s) run a %s image on a different tag (legacy fallback?): %s",
			len(wrongMonolith), monolithPrefix, strings.Join(wrongMonolith, "; ")))
	}
	if !haveBeanstalkd {
		problems = append(problems, fmt.Sprintf("no Ready pod runs %s", wantBeanstalkd))
	}
	if !haveNextjs {
		problems = append(problems, fmt.Sprintf("no Ready pod runs %s", wantNextjs))
	}
	if len(problems) > 0 {
		return fmt.Errorf("slim-flip guard: %s", strings.Join(problems, "; "))
	}
	return nil
}
