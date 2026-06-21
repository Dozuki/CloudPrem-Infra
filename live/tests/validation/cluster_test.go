package validation

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func dep(name string, ready, desired int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dozuki"},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func TestEvaluateWorkloads_criticalNotReadyIsError(t *testing.T) {
	cs := fake.NewSimpleClientset(
		dep("dozuki-app-deployment", 0, 1), // critical, not ready
		dep("dozuki-memcached", 0, 1),      // advisory, not ready
	)
	advisory, matched, ready, err := evaluateWorkloads(context.Background(), cs, "dozuki", []string{"dozuki-app*"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !matched {
		t.Fatal("expected matched=true (critical workload present)")
	}
	if ready {
		t.Fatal("expected ready=false (critical not ready)")
	}
	_ = advisory
}

func TestEvaluateWorkloads_advisoryNotReadyIsAdvisoryOnly(t *testing.T) {
	cs := fake.NewSimpleClientset(
		dep("dozuki-app-deployment", 1, 1), // critical, ready
		dep("dozuki-memcached", 0, 1),      // advisory, not ready
	)
	advisory, matched, ready, err := evaluateWorkloads(context.Background(), cs, "dozuki", []string{"dozuki-app*"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !matched {
		t.Fatal("expected matched=true (critical workload present)")
	}
	if !ready {
		t.Fatal("expected ready=true (all critical ready)")
	}
	if len(advisory) != 1 || advisory[0] != "dozuki-memcached" {
		t.Fatalf("advisory = %v, want [dozuki-memcached]", advisory)
	}
}

func TestEvaluateWorkloads_criticalAbsentWaitsNotErrors(t *testing.T) {
	cs := fake.NewSimpleClientset(dep("dozuki-memcached", 1, 1))
	_, matched, ready, err := evaluateWorkloads(context.Background(), cs, "dozuki", []string{"dozuki-app*"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if matched {
		t.Fatal("expected matched=false (critical workload absent)")
	}
	if ready {
		t.Fatal("expected ready=false (critical workload absent)")
	}
}
