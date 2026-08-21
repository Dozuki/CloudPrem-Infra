package validation

import (
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

// condition's observedGeneration return is what keeps a stale Released=False from being
// blamed on a newer spec attempt. Getting it wrong is silent in both directions: decoding
// a real generation as absent suppresses every failure report, and reporting 0 where there
// is no data would attribute an old error to a new generation. So pin the decode
// explicitly, including the "absent" case, which must come back as -1 so the caller can
// route it to the unscoped bucket rather than claiming it for the current generation.
func TestConditionObservedGeneration(t *testing.T) {
	hrWith := func(cond map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []interface{}{cond},
			},
		}}
	}

	cases := []struct {
		name    string
		cond    map[string]interface{}
		wantGen int64
	}{
		{
			name:    "int64 as the dynamic client decodes it",
			cond:    map[string]interface{}{"type": "Released", "status": "False", "reason": "UpgradeFailed", "message": "hook failed", "observedGeneration": int64(7)},
			wantGen: 7,
		},
		{
			name:    "float64 from a different decode path",
			cond:    map[string]interface{}{"type": "Released", "status": "False", "reason": "UpgradeFailed", "observedGeneration": float64(7)},
			wantGen: 7,
		},
		{
			name:    "absent observedGeneration reads as unknown, not zero",
			cond:    map[string]interface{}{"type": "Released", "status": "False", "reason": "UpgradeFailed"},
			wantGen: -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason, _, gen := condition(hrWith(tc.cond), "Released")
			if status != "False" || reason != "UpgradeFailed" {
				t.Fatalf("status/reason = %q/%q, want False/UpgradeFailed", status, reason)
			}
			if gen != tc.wantGen {
				t.Fatalf("observedGeneration = %d, want %d", gen, tc.wantGen)
			}
		})
	}

	t.Run("missing condition returns unknown generation", func(t *testing.T) {
		status, _, _, gen := condition(hrWith(map[string]interface{}{"type": "Ready", "status": "True"}), "Released")
		if status != "" {
			t.Fatalf("status = %q, want empty for an absent condition", status)
		}
		if gen != -1 {
			t.Fatalf("observedGeneration = %d, want -1 for an absent condition", gen)
		}
	})

	t.Run("no status block at all", func(t *testing.T) {
		status, _, _, gen := condition(&unstructured.Unstructured{Object: map[string]interface{}{}}, "Released")
		if status != "" || gen != -1 {
			t.Fatalf("status/gen = %q/%d, want empty/-1", status, gen)
		}
	})
}

// isTerminalReason must keep UpgradeFailed: it is unreachable on a release running
// RetryOnFailure, but a pre-v9.2.2 baseline still reports a dead upgrade that way and the
// harness runs upgrade tests from those baselines.
func TestIsTerminalReason(t *testing.T) {
	for _, r := range []string{"InstallFailed", "UpgradeFailed", "TestFailed", "RollbackFailed", "UninstallFailed", "ChartPullFailed"} {
		if !isTerminalReason(r) {
			t.Errorf("isTerminalReason(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"Progressing", "ArtifactFailed", "ReconciliationSucceeded", ""} {
		if isTerminalReason(r) {
			t.Errorf("isTerminalReason(%q) = true, want false", r)
		}
	}
}

// --- WS1: live diagnosis fixtures and helpers ---------------------------------------

// hrCond builds a condition map with an explicit observedGeneration, mirroring what the
// dynamic client decodes off a real HelmRelease.
func hrCond(condType, status, reason, message string, observedGeneration int64) map[string]interface{} {
	return map[string]interface{}{
		"type": condType, "status": status, "reason": reason, "message": message,
		"observedGeneration": observedGeneration,
	}
}

// hrCondNoGen builds a condition with no observedGeneration field at all, for the
// "unscoped" (generation unknown) path.
func hrCondNoGen(condType, status, reason, message string) map[string]interface{} {
	return map[string]interface{}{"type": condType, "status": status, "reason": reason, "message": message}
}

func testHR(namespace string, generation int64, conds ...map[string]interface{}) *unstructured.Unstructured {
	items := make([]interface{}, len(conds))
	for i, c := range conds {
		items[i] = c
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]interface{}{
			"name":       "dozuki",
			"namespace":  namespace,
			"generation": generation,
		},
		"status": map[string]interface{}{
			"conditions": items,
		},
	}}
}

func newFakeDC(objs ...*unstructured.Unstructured) dynamic.Interface {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{helmReleaseGVR: "HelmReleaseList"}
	ro := make([]runtime.Object, len(objs))
	for i, o := range objs {
		ro[i] = o
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, ro...)
}

const diagStub = "STUB-DIAGNOSIS"

func stubDiagnose(seen *[]*unstructured.Unstructured) func(*unstructured.Unstructured) string {
	return func(hr *unstructured.Unstructured) string {
		if seen != nil {
			*seen = append(*seen, hr)
		}
		return diagStub
	}
}

func wantDiagSuffix(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "\n-- live diagnosis --\n"+diagStub) {
		t.Fatalf("error does not carry the diagnosis block: %v", err)
	}
}

// --- WS1: the 5 exhaustive AwaitHelmReleaseReady failure branches -------------------

func TestAwaitHelmReleaseReady_StalledCarriesDiagnosis(t *testing.T) {
	hr := testHR(fluxNamespace, 1,
		hrCond("Ready", "False", "Progressing", "still rolling out", 1),
		hrCond("Stalled", "True", "RetriesExceeded", "install retries exhausted", 1),
	)
	err := awaitHelmReleaseReady(newFakeDC(hr), stubDiagnose(nil), "dozuki", "dozuki", time.Hour)
	wantDiagSuffix(t, err)
	if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("expected a stalled verdict: %v", err)
	}
}

func TestAwaitHelmReleaseReady_TerminalReasonCarriesDiagnosis(t *testing.T) {
	hr := testHR(fluxNamespace, 1,
		hrCond("Ready", "False", "InstallFailed", "hook failed", 1),
	)
	err := awaitHelmReleaseReady(newFakeDC(hr), stubDiagnose(nil), "dozuki", "dozuki", time.Hour)
	wantDiagSuffix(t, err)
	if !strings.Contains(err.Error(), "failed (InstallFailed)") {
		t.Fatalf("expected a terminal-reason verdict: %v", err)
	}
}

func TestAwaitHelmReleaseReady_TimeoutWithLastFailureCarriesDiagnosis(t *testing.T) {
	// RetryOnFailure upgrade: Ready never goes False, but Released=False at the current
	// generation records the retryable failure.
	hr := testHR(fluxNamespace, 1,
		hrCond("Ready", "Unknown", "Progressing", "reconciling", 1),
		hrCond("Released", "False", "UpgradeFailed", "hook failed on retry", 1),
	)
	err := awaitHelmReleaseReady(newFakeDC(hr), stubDiagnose(nil), "dozuki", "dozuki", time.Nanosecond)
	wantDiagSuffix(t, err)
	if !strings.Contains(err.Error(), "still failing under the retry strategy") {
		t.Fatalf("expected the lastFailure timeout verdict: %v", err)
	}
}

func TestAwaitHelmReleaseReady_TimeoutWithUnscopedFailureCarriesDiagnosis(t *testing.T) {
	hr := testHR(fluxNamespace, 1,
		hrCond("Ready", "Unknown", "Progressing", "reconciling", 1),
		hrCondNoGen("Released", "False", "UpgradeFailed", "hook failed, generation unknown"),
	)
	err := awaitHelmReleaseReady(newFakeDC(hr), stubDiagnose(nil), "dozuki", "dozuki", time.Nanosecond)
	wantDiagSuffix(t, err)
	if !strings.Contains(err.Error(), "unscoped Released error") {
		t.Fatalf("expected the lastUnscopedFailure timeout verdict: %v", err)
	}
}

func TestAwaitHelmReleaseReady_TimeoutGenericCarriesDiagnosisAndNilHR(t *testing.T) {
	// No CR at all — the "not created yet" path never sets lastHR, so the generic
	// timeout verdict must still carry a diagnosis block, built against a nil hr.
	var seen []*unstructured.Unstructured
	err := awaitHelmReleaseReady(newFakeDC(), stubDiagnose(&seen), "dozuki", "dozuki", time.Nanosecond)
	wantDiagSuffix(t, err)
	if !strings.Contains(err.Error(), "produced no verdict within") {
		t.Fatalf("expected the generic timeout verdict: %v", err)
	}
	if len(seen) != 1 || seen[0] != nil {
		t.Fatalf("expected diagnose to be called once with a nil hr, got %#v", seen)
	}
}

func TestAwaitHelmReleaseReady_TransportErrorExempt(t *testing.T) {
	// The get-HelmRelease transport-error return (a non-NotFound error from
	// findHelmRelease) is explicitly exempt from the diagnosis per spec — verify it
	// stays a bare error with no diagnosis block appended.
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{helmReleaseGVR: "HelmReleaseList"}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	dc.PrependReactor("get", "helmreleases", func(action kubetesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("connection refused")
	})
	err := awaitHelmReleaseReady(dc, stubDiagnose(nil), "dozuki", "dozuki", time.Hour)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "-- live diagnosis --") {
		t.Fatalf("transport-error branch must stay exempt from diagnosis: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected the transport error to surface: %v", err)
	}
}

// --- WS1: diagnoseReleaseWithClient / diagnoseRelease --------------------------------

func TestDiagnoseReleaseWithClient_NilHR(t *testing.T) {
	out := diagnoseReleaseWithClient(k8sfake.NewSimpleClientset(), nil, "dozuki", nil)
	if !strings.Contains(out, "HelmRelease CR absent") {
		t.Fatalf("expected CR-absent line, got: %s", out)
	}
}

func TestDiagnoseReleaseWithClient_EmptyNamespace(t *testing.T) {
	hr := testHR(fluxNamespace, 1, hrCond("Ready", "False", "Progressing", "still rolling out", 1))
	out := diagnoseReleaseWithClient(k8sfake.NewSimpleClientset(), nil, "", hr)
	if !strings.Contains(out, "no app namespace to inspect") {
		t.Fatalf("expected the no-namespace note, got: %s", out)
	}
	if strings.Contains(out, "non-Ready pods") {
		t.Fatalf("must not attempt pod/event/job listing with no namespace: %s", out)
	}
}

func TestDiagnoseReleaseWithClient_ClientError(t *testing.T) {
	out := diagnoseReleaseWithClient(nil, fmt.Errorf("boom"), "dozuki", nil)
	if !strings.Contains(out, "diagnosis unavailable: boom") {
		t.Fatalf("expected the client-error degrade, got: %s", out)
	}
}

func TestDiagnoseReleaseWithClient_PodsEventsJobs(t *testing.T) {
	const ns = "dozuki"
	brokenPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-0", Namespace: ns},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable"},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}},
			},
		},
	}
	readyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: ns},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	completedHookPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate-hook", Namespace: ns},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	warnEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: ns},
		Type:           corev1.EventTypeWarning,
		Reason:         "FailedScheduling",
		Message:        "0/3 nodes are available: insufficient cpu",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "app-0"},
		LastTimestamp:  metav1.NewTime(time.Now().Add(-time.Minute)),
	}
	normalEvent := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: ns},
		Type:           corev1.EventTypeNormal,
		Reason:         "Scheduled",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "app-1"},
		LastTimestamp:  metav1.NewTime(time.Now()),
	}
	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: ns},
		Status:     batchv1.JobStatus{Failed: 1},
	}
	quietJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "seed", Namespace: ns},
		Status:     batchv1.JobStatus{Succeeded: 1},
	}

	cs := k8sfake.NewSimpleClientset(brokenPod, readyPod, completedHookPod, warnEvent, normalEvent, failedJob, quietJob)
	out := diagnoseReleaseWithClient(cs, nil, ns, nil)

	for _, want := range []string{"app-0", "ImagePullBackOff", "unschedulable=Unschedulable", "FailedScheduling", "migrate active=0 failed=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnosis missing %q:\n%s", want, out)
		}
	}
	for _, notWant := range []string{"app-1", "migrate-hook", "Scheduled\n", "seed active"} {
		if strings.Contains(out, notWant) {
			t.Errorf("diagnosis should not mention %q (ready pod / succeeded hook pod / normal event / quiet job):\n%s", notWant, out)
		}
	}
}

func TestCapDiagnosisTruncates(t *testing.T) {
	long := strings.Repeat("x", 5000)
	out := capDiagnosis(long)
	if len(out) > 1501 {
		t.Fatalf("diagnosis block not capped: %d chars", len(out))
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatalf("expected a truncation marker, got suffix %q", out[len(out)-10:])
	}
}

func TestDiagnoseRelease_BadKubeconfigDegrades(t *testing.T) {
	out := diagnoseRelease("/nonexistent/kubeconfig-for-test", "dozuki", nil)
	if !strings.Contains(out, "diagnosis unavailable:") {
		t.Fatalf("expected the bad-kubeconfig degrade, got: %s", out)
	}
}
