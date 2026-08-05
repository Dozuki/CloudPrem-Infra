package recovery

import (
	"strings"
	"testing"
)

func inputsFixture() Inputs {
	return Inputs{
		SnapshotARN: "arn:aws:rds:us-west-2:076248559428:cluster-snapshot:harness-recovery-x",
		Buckets: map[string]string{
			"image": "smokesrc-min-image-dr-abc",
			"obj":   "smokesrc-min-obj-dr-abc",
			"pdf":   "smokesrc-min-pdf-dr-abc",
			"doc":   "smokesrc-min-doc-dr-abc",
		},
		S3KMSKeyARN: "arn:aws:kms:us-west-2:076248559428:key/1111",
	}
}

func TestEnvInputs(t *testing.T) {
	env, err := inputsFixture().EnvInputs()
	if err != nil {
		t.Fatal(err)
	}
	if env["aurora_snapshot_identifier"] == "" || env["s3_kms_key_id"] == "" {
		t.Error("snapshot/kms inputs missing")
	}
	if env["enable_dr"] != false {
		t.Error("the rebuild must not enable DR (failback is a deliberate later step)")
	}
	buckets, ok := env["s3_existing_buckets"].([]interface{})
	if !ok || len(buckets) != 4 {
		t.Fatalf("s3_existing_buckets should be 4 objects, got %#v", env["s3_existing_buckets"])
	}
	// Deterministic ordering (sorted by kind) so env.hcl renders reproducibly.
	first, _ := buckets[0].(map[string]interface{})
	if first["type"] != "doc" {
		t.Errorf("expected sorted kinds starting with doc, got %v", first["type"])
	}
}

func TestInputsValidate(t *testing.T) {
	i := inputsFixture()
	i.SnapshotARN = ""
	if _, err := i.EnvInputs(); err == nil {
		t.Error("missing snapshot accepted")
	}
	i = inputsFixture()
	i.S3KMSKeyARN = ""
	if _, err := i.EnvInputs(); err == nil {
		t.Error("missing KMS key accepted (existing-bucket adoption requires it)")
	}
	i = inputsFixture()
	delete(i.Buckets, "pdf")
	if _, err := i.EnvInputs(); err == nil {
		t.Error("missing bucket kind accepted")
	}
}

func TestRenderInfraLiveEnvHCL(t *testing.T) {
	out, err := RenderInfraLiveEnvHCL(ScaffoldParams{
		StackName:     "acme-prod",
		PrimaryRegion: "us-east-1",
		DRRegion:      "us-west-2",
		InfraVersion:  "v8.0.3",
		Inputs:        inputsFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`environment   = "acme-prod"`,
		`infra_version = "v8.0.3"`,
		`aurora_snapshot_identifier = "arn:aws:rds:us-west-2:`,
		`{ type = "doc", bucket_name = "smokesrc-min-doc-dr-abc" }`,
		"enable_dr = false",
		"KEEP aurora_snapshot_identifier",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scaffold missing %q\n%s", want, out)
		}
	}
}

func TestSanitizeSnapshotID(t *testing.T) {
	cases := map[string]string{
		"harness-recovery-local-1785290702-recover-recover_source": "harness-recovery-local-1785290702-recover-recover-source",
		"smokerec-min-dr-rebuild":                                  "smokerec-min-dr-rebuild",
		"Weird__Name--x-":                                          "weird-name-x",
		"123abc":                                                   "s-123abc",
		"___":                                                      "s",
	}
	for in, want := range cases {
		if got := SanitizeSnapshotID(in); got != want {
			t.Errorf("SanitizeSnapshotID(%q) = %q, want %q", in, got, want)
		}
	}
	long := SanitizeSnapshotID("a" + strings.Repeat("-b", 60))
	if len(long) > 63 || strings.HasSuffix(long, "-") {
		t.Errorf("long id not clamped cleanly: %q (len %d)", long, len(long))
	}
}
