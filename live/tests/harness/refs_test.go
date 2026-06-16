package harness

import "testing"

func TestResolveRef(t *testing.T) {
	tags := []string{"v6.0", "v5.3", "v5.2"}
	cases := map[string]string{
		"auto:latest":   "v6.0",
		"auto:latest-1": "v5.3",
		"v6.1-release":  "v6.1-release",
	}
	for in, want := range cases {
		got, err := ResolveRef(in, tags)
		if err != nil {
			t.Fatalf("ResolveRef(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ResolveRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveRefNotEnoughTags(t *testing.T) {
	if _, err := ResolveRef("auto:latest-1", []string{"v6.0"}); err == nil {
		t.Errorf("expected error when fewer than 2 tags for auto:latest-1")
	}
}
