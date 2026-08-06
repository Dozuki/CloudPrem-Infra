package validation

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// The probe zip must be fully self-contained from embedded assets: handler, PyMySQL and
// the right partition's CA bundle, with no runtime pip or network involved.
func TestBuildProbeZip(t *testing.T) {
	for _, region := range []string{"us-west-2", "us-gov-east-1"} {
		b, err := buildProbeZip(region)
		if err != nil {
			t.Fatalf("%s: %v", region, err)
		}
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("%s: not a zip: %v", region, err)
		}
		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		for _, want := range []string{"handler.py", "rds-ca.pem", "pymysql/__init__.py"} {
			if !names[want] {
				t.Errorf("%s: zip missing %s", region, want)
			}
		}
		for name := range names {
			if strings.Contains(name, "global-bundle") {
				t.Errorf("%s: raw bundle %s leaked into the zip", region, name)
			}
		}
		ca, err := zr.Open("rds-ca.pem")
		if err != nil {
			t.Fatalf("%s: open rds-ca.pem: %v", region, err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(ca); err != nil {
			t.Fatalf("%s: read rds-ca.pem: %v", region, err)
		}
		if !strings.Contains(buf.String(), "BEGIN CERTIFICATE") {
			t.Errorf("%s: rds-ca.pem is not a PEM bundle", region)
		}
	}
	// The two partitions must actually get DIFFERENT bundles.
	com, _ := buildProbeZip("us-west-2")
	gov, _ := buildProbeZip("us-gov-east-1")
	if bytes.Equal(com, gov) {
		t.Error("commercial and gov zips are identical - partition CA selection is not working")
	}
}

func TestSwapARNRegion(t *testing.T) {
	in := "arn:aws:secretsmanager:us-east-1:076000000000:secret:local-database-AbCdEf"
	want := "arn:aws:secretsmanager:us-west-2:076000000000:secret:local-database-AbCdEf"
	if got := swapARNRegion(in, "us-west-2"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := swapARNRegion("not-an-arn", "us-west-2"); got != "" {
		t.Errorf("malformed ARN should return empty, got %q", got)
	}
}

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
