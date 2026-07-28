package validation

import "testing"

func TestDsnHost(t *testing.T) {
	// The real shape: key@host/project.
	h, err := dsnHost("https://abc123@o4507.ingest.us.sentry.io/451")
	if err != nil || h != "o4507.ingest.us.sentry.io" {
		t.Errorf("dsnHost = %q, %v; want the ingest host", h, err)
	}
}

func TestDsnHostEmptyMeansNothingToSilence(t *testing.T) {
	// An unset DSN is a valid state (stack without sentry), not an error.
	for _, dsn := range []string{"", "   "} {
		if h, err := dsnHost(dsn); err != nil || h != "" {
			t.Errorf("dsnHost(%q) = %q, %v; want empty, nil", dsn, h, err)
		}
	}
}

func TestDsnHostMalformedIsAnError(t *testing.T) {
	// A garbage DSN must error rather than silently silencing nothing - the caller
	// would otherwise believe Sentry is muted when it is not.
	if _, err := dsnHost("not a dsn"); err == nil {
		t.Error("dsnHost accepted a DSN with no host")
	}
}

func TestSentryEmittingDeploymentsAreReleaseScoped(t *testing.T) {
	for _, d := range sentryEmittingDeployments("dozuki") {
		if d[:7] != "dozuki-" {
			t.Errorf("deployment %q not scoped to the release", d)
		}
	}
}
