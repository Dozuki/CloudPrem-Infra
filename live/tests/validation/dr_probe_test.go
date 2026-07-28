package validation

import "testing"

func TestParseDBSecret(t *testing.T) {
	u, p, err := parseDBSecret(`{"username":"dozuki","password":"s3cret","host":"x"}`)
	if err != nil || u != "dozuki" || p != "s3cret" {
		t.Errorf("parseDBSecret = %q/%q, %v", u, p, err)
	}
	// The alternate key spelling some secrets use.
	if u, _, err := parseDBSecret(`{"user":"alt","password":"pw"}`); err != nil || u != "alt" {
		t.Errorf("alternate spelling: %q, %v", u, err)
	}
	// Missing keys must error with the available keys named, never return empties.
	if _, _, err := parseDBSecret(`{"host":"only"}`); err == nil {
		t.Error("missing creds accepted")
	}
	if _, _, err := parseDBSecret(`not json`); err == nil {
		t.Error("non-JSON accepted")
	}
}

func TestLastNonEmptyLine(t *testing.T) {
	// mysql warning lines must not be mistaken for output.
	out := "mysql: [Warning] Using a password...\n6\n"
	if got := lastNonEmptyLine(out); got != "6" {
		t.Errorf("lastNonEmptyLine = %q, want 6", got)
	}
}
