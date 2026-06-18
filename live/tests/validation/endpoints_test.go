package validation

import "testing"

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"example.com":         "https://example.com",
		"https://example.com": "https://example.com",
		"http://example.com":  "http://example.com",
	}
	for in, want := range cases {
		if got := normalizeURL(in); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
