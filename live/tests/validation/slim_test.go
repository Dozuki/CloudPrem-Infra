package validation

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testRepo         = "123456789012.dkr.ecr.us-east-1.amazonaws.com/dozuki"
	testImageTag     = "9.2.1"
	testBeanstalkTag = "3.1.0"
	testNextjsTag    = "1.4.0"
)

func readyPod(name, image string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: image}},
		},
	}
}

func notReadyPod(name, image string) corev1.Pod {
	p := readyPod(name, image)
	p.Status.Conditions[0].Status = corev1.ConditionFalse
	return p
}

func failedPod(name, image string) corev1.Pod {
	p := readyPod(name, image)
	p.Status.Phase = corev1.PodFailed
	p.Status.Conditions = nil
	return p
}

func slimPodSet() []corev1.Pod {
	return []corev1.Pod{
		readyPod("dozuki-app-abc123", testRepo+"/monolith-app:"+testImageTag),
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
}

func TestAssertSlimImages_Positive(t *testing.T) {
	if err := assertSlimImages(slimPodSet(), testRepo, testImageTag, testBeanstalkTag, testNextjsTag); err != nil {
		t.Fatalf("expected pass on a correct slim pod set, got: %v", err)
	}
}

func TestAssertSlimImages_WrongTagFails(t *testing.T) {
	pods := []corev1.Pod{
		readyPod("dozuki-app-abc123", testRepo+"/monolith-app:0.9.0-legacy"),
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag)
	if err == nil {
		t.Fatal("expected failure when the app pod runs the wrong tag, got nil (a no-op implementation would pass this)")
	}
	if !strings.Contains(err.Error(), "no Ready pod runs "+testRepo+"/monolith-app:"+testImageTag) {
		t.Errorf("error does not mention the missing expected monolith-app tag: %v", err)
	}
}

func TestAssertSlimImages_ZeroMonolithPodsFails(t *testing.T) {
	pods := []corev1.Pod{
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag)
	if err == nil {
		t.Fatal("expected failure when no monolith-app pod exists at all (assertion 1)")
	}
}

func TestAssertSlimImages_StrayMonolithPodOnDifferentTagFails(t *testing.T) {
	pods := []corev1.Pod{
		readyPod("dozuki-app-abc123", testRepo+"/monolith-app:"+testImageTag),         // correct
		readyPod("dozuki-app-legacy-leftover", testRepo+"/monolith-app:0.9.0-legacy"), // stray
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag)
	if err == nil {
		t.Fatal("expected failure: a stray monolith-app pod on a different tag alongside a correct one must fail assertion 2")
	}
	if !strings.Contains(err.Error(), "different tag") {
		t.Errorf("error does not mention the assertion-2 stray-tag failure: %v", err)
	}
}

func TestAssertSlimImages_IgnoresInitContainers(t *testing.T) {
	pod := readyPod("dozuki-app-abc123", testRepo+"/monolith-app:"+testImageTag)
	pod.Spec.InitContainers = []corev1.Container{
		{Name: "migrate", Image: testRepo + "/monolith-app:0.9.0-legacy"},
	}
	pods := []corev1.Pod{
		pod,
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	if err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag); err != nil {
		t.Fatalf("init container image on a different tag must be ignored, got: %v", err)
	}
}

func TestAssertSlimImages_IgnoresFailedAndEvictedPods(t *testing.T) {
	evicted := failedPod("dozuki-app-evicted", testRepo+"/monolith-app:0.9.0-legacy")
	evicted.Status.Reason = "Evicted"
	pods := []corev1.Pod{
		readyPod("dozuki-app-abc123", testRepo+"/monolith-app:"+testImageTag),
		evicted,
		failedPod("dozuki-app-crashed", testRepo+"/monolith-app:0.8.0-legacy"),
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	if err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag); err != nil {
		t.Fatalf("Failed/Evicted pods must be excluded entirely, got: %v", err)
	}
}

func TestAssertSlimImages_IgnoresTerminatingPods(t *testing.T) {
	// A pod on its way out can still report phase Running with a stale Ready=True
	// condition for a few seconds after deletion is requested. It must not count as
	// evidence the assertion passes - a leftover pod from an unrelated prior state is
	// exactly the case eligiblePod's DeletionTimestamp check exists to exclude.
	terminating := readyPod("dozuki-app-abc123", testRepo+"/monolith-app:"+testImageTag)
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	pods := []corev1.Pod{
		terminating,
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag)
	if err == nil {
		t.Fatal("expected failure: the only monolith-app pod is terminating, so assertion 1 must fail")
	}
}

func TestAssertSlimImages_NotReadyDoesNotSatisfyPositiveAssertions(t *testing.T) {
	pods := []corev1.Pod{
		notReadyPod("dozuki-app-abc123", testRepo+"/monolith-app:"+testImageTag),
		readyPod("dozuki-beanstalkd-deployment-0", testRepo+"/beanstalkd:"+testBeanstalkTag),
		readyPod("dozuki-nextjs-def456", testRepo+"/web-nextjs:"+testNextjsTag),
	}
	err := assertSlimImages(pods, testRepo, testImageTag, testBeanstalkTag, testNextjsTag)
	if err == nil {
		t.Fatal("expected failure: the only monolith-app pod is not Ready, so assertion 1 must fail")
	}
}

func TestAssertSlimImages_EmptyVersionVarsNameTheField(t *testing.T) {
	cases := []struct {
		name                                       string
		imageRepository, imageTag, beanTag, njsTag string
		wantInError                                string
	}{
		{"empty image_repository", "", testImageTag, testBeanstalkTag, testNextjsTag, "image_repository"},
		{"empty image_tag", testRepo, "", testBeanstalkTag, testNextjsTag, "image_tag"},
		{"empty beanstalkd_tag", testRepo, testImageTag, "", testNextjsTag, "beanstalkd_tag"},
		{"empty nextjs_tag", testRepo, testImageTag, testBeanstalkTag, "", "nextjs_tag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := assertSlimImages(slimPodSet(), c.imageRepository, c.imageTag, c.beanTag, c.njsTag)
			if err == nil {
				t.Fatalf("expected an error for %s, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.wantInError) {
				t.Errorf("error %q does not name the empty field %q", err.Error(), c.wantInError)
			}
		})
	}
}

func TestSlimFlipApplies(t *testing.T) {
	cases := map[string]bool{
		"slim":   true,
		"":       false,
		"legacy": false,
		"Slim":   false,
		"slim ":  false,
	}
	for flavor, want := range cases {
		if got := SlimFlipApplies(flavor); got != want {
			t.Errorf("SlimFlipApplies(%q) = %v, want %v", flavor, got, want)
		}
	}
}
