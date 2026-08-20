package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "evidence", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(b)
}

func TestExtractErrorPrefersTofuErrorOverThinVerdict(t *testing.T) {
	got := ExtractError(readTestdata(t, "bi-ha-destroy-tail.log"))
	for _, want := range []string{"InvalidResourceStateFault", "cannot be stopped"} {
		if !strings.Contains(got, want) {
			t.Errorf("ExtractError() = %q, want it to contain %q", got, want)
		}
	}
	for _, notWant := range []string{"Still destroying", "context deadline exceeded"} {
		if strings.Contains(got, notWant) {
			t.Errorf("ExtractError() = %q, must not contain %q (superseded/noise)", got, notWant)
		}
	}
	if got == "teardown failed: destroy: exit status 1" {
		t.Errorf("ExtractError() returned the thin verdict verbatim; the tofu error should have won")
	}
}

func TestExtractErrorKeepsInformativeVerdict(t *testing.T) {
	got := ExtractError(readTestdata(t, "min-default-upgrade.log"))
	want := "upgrade failed: worktree add 8f347c78fb11039a9737c30e956fa16e5f57cd78 (8f347c78fb11039a9737c30e956fa16e5f57cd78): exit status 128"
	if got != want {
		t.Errorf("ExtractError() = %q, want %q", got, want)
	}
}

func TestExtractErrorReturnsEmptyOnNoise(t *testing.T) {
	got := ExtractError(readTestdata(t, "noise-only.log"))
	if got != "" {
		t.Errorf("ExtractError() = %q, want \"\" (falls back to node message)", got)
	}
}

func TestExtractErrorStripsAnsiAndTerragruntPrefix(t *testing.T) {
	got := ExtractError(readTestdata(t, "bi-ha-destroy-tail.log"))
	for _, bad := range []string{"\x1b[", "STDERR", "│"} {
		if strings.Contains(got, bad) {
			t.Errorf("ExtractError() = %q, must not contain raw decoration %q", got, bad)
		}
	}
	if strings.HasPrefix(got, "01:") {
		t.Errorf("ExtractError() = %q, still carries a timestamp prefix", got)
	}
}

func TestExtractErrorWindowIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("Error: the real error, but it is 50000 lines from the tail\n")
	for i := 0; i < 50000; i++ {
		b.WriteString("module.thing: Still destroying... [id=x, 1s elapsed]\n")
	}
	got := ExtractError(b.String())
	if got != "" {
		t.Errorf("ExtractError() = %q, want \"\" - the error is outside the 400-line window", got)
	}
}

func TestScrubRedactsSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"vault token", "VAULT_TOKEN=hvs.CAESIJf8vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3"},
		{"akid", "aws_access_key_id = AKIAIOSFODNN7EXAMPLE"},
		{"asid", "aws_access_key_id = ASIAIOSFODNN7EXAMPLE"},
		{"slack token", "SLACK_TOKEN=xoxb-1234567890-abcdefghij"},
		{"github token", "GH_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"},
		{"jwt", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQ_dQw4w9WgXcQdQw4w9WgXcQ"},
		{"password kv", `password="hunter2hunter"`},
		{"base64 blob", strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVowMTIzNDU2Nzg5", 2)},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIBogIBAAJ...\n-----END RSA PRIVATE KEY-----"},
		{"private key pkcs8", "-----BEGIN PRIVATE KEY-----\nMIIBogIBAAJ...\n-----END PRIVATE KEY-----"},
		{"embedded keyword env var", "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{"url credentials", "mysql://root:SuperSecretPw1@host/db"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scrub(c.input)
			if got == c.input {
				t.Errorf("Scrub(%q) left input unchanged", c.input)
			}
		})
	}
}

func TestScrubRedactsEmbeddedKeywordAndURLCredentials(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		mustNotHave string
		mustHave    string
	}{
		{
			name:        "aws secret access key",
			input:       "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			mustNotHave: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			mustHave:    "AWS_SECRET_ACCESS_KEY=[redacted]",
		},
		{
			name:        "mysql url password",
			input:       "mysql://root:SuperSecretPw1@host/db",
			mustNotHave: "SuperSecretPw1",
			mustHave:    "mysql://root:[redacted]@host/db",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scrub(c.input)
			if strings.Contains(got, c.mustNotHave) {
				t.Errorf("Scrub(%q) = %q, must not contain the raw secret %q", c.input, got, c.mustNotHave)
			}
			if !strings.Contains(got, c.mustHave) {
				t.Errorf("Scrub(%q) = %q, want it to contain %q", c.input, got, c.mustHave)
			}
		})
	}
}

// TestScrubKeywordRuleDoesNotFlattenSpecificLabels guards against the widened
// keyword rule running over its own output: several specific labels above
// ("[redacted-vault-token]", "[redacted-slack-token]", "[redacted-github-token]")
// contain the word "token", which the generic keyword rule also matches on.
func TestScrubKeywordRuleDoesNotFlattenSpecificLabels(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"vault token", "VAULT_TOKEN=hvs.CAESIJf8vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3vN3", "[redacted-vault-token]"},
		{"slack token", "SLACK_TOKEN=xoxb-1234567890-abcdefghij", "[redacted-slack-token]"},
		{"github token", "GH_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "[redacted-github-token]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Scrub(c.input)
			if !strings.Contains(got, c.want) {
				t.Errorf("Scrub(%q) = %q, want the specific label %q preserved (not flattened to a bare [redacted])", c.input, got, c.want)
			}
		})
	}
}

func TestScrubKeepsDiagnosticSurvivors(t *testing.T) {
	survivors := []string{
		"8f347c78fb11039a9737c30e956fa16e5f57cd78", // 40-hex git SHA
		"arn:aws:dms:us-east-1:000000000000:replication-config:REPLICATIONCONFIGID",
		"dozuki-argo-artifacts-000000000000",
		"smokef9b3-bi-reader",
		// KMS diagnostics are the most common real failure shape; a bare "key"
		// alternation redacted all three of these.
		"kms_key_id = arn:aws:kms:us-east-1:000000000000:key/abc-def-1234",
		"Error: creating KMS Key: AccessDeniedException: User is not authorized",
		"keyspace: production",
	}
	for _, s := range survivors {
		t.Run(s, func(t *testing.T) {
			if got := Scrub(s); got != s {
				t.Errorf("Scrub(%q) = %q, want it unchanged", s, got)
			}
		})
	}
}

func loadWorkflowList(t *testing.T, name string) WorkflowList {
	t.Helper()
	var list WorkflowList
	if err := json.Unmarshal([]byte(readTestdata(t, name)), &list); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return list
}

// stubFetcher answers Tail from an in-memory map keyed by "bucket/key", or via a
// custom fn when set - so tests can simulate misses/timeouts without real S3.
type stubFetcher struct {
	logs      map[string]string
	fn        func(ctx context.Context, bucket, key string) (string, error)
	mu        sync.Mutex
	requested []string
}

func (s *stubFetcher) Tail(ctx context.Context, bucket, key string) (string, error) {
	s.mu.Lock()
	s.requested = append(s.requested, bucket+"/"+key)
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(ctx, bucket, key)
	}
	if s.logs != nil {
		if v, ok := s.logs[bucket+"/"+key]; ok {
			return v, nil
		}
	}
	return "", errors.New("stub: no log for " + bucket + "/" + key)
}

func TestBuildSelectsArtifactKeyPerFailedPodNode(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{logs: map[string]string{}}
	_ = Build(context.Background(), list, stub, BuildOptions{})

	stub.mu.Lock()
	got := append([]string(nil), stub.requested...)
	stub.mu.Unlock()
	sort.Strings(got)

	want := []string{
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log",
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log",
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("requested %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("requested[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildCapsAtThreeNodesPerChild(t *testing.T) {
	mkNode := func(id, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: id, FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-many-abcde", Labels: map[string]string{"harness/config": "many"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"a": mkNode("a", "2026-08-04T00:00:01Z"),
				"b": mkNode("b", "2026-08-04T00:00:02Z"),
				"c": mkNode("c", "2026-08-04T00:00:03Z"),
				"d": mkNode("d", "2026-08-04T00:00:04Z"),
				"e": mkNode("e", "2026-08-04T00:00:05Z"),
				// A Retry node with the same displayName/phase must not be fetched.
				"retry-a": {ID: "retry-a", Type: "Retry", Phase: "Failed", DisplayName: "a"},
			},
		},
	}
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return "Error: boom\n", nil
	}}
	_ = Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})

	stub.mu.Lock()
	got := append([]string(nil), stub.requested...)
	stub.mu.Unlock()
	if len(got) != 3 {
		t.Errorf("requested %d objects, want 3 (the per-child node cap)", len(got))
	}
	for _, r := range got {
		if strings.Contains(r, "retry-a") {
			t.Errorf("Retry node was fetched: %v", got)
		}
	}
	// The cap must keep the EARLIEST 3 by FinishedAt (a, b, c), not the latest (c,
	// d, e) - the originating failure is what explains why the run is red, and it
	// must survive however many later nodes pile on top of it.
	for _, want := range []string{"a/main.log", "b/main.log", "c/main.log"} {
		found := false
		for _, r := range got {
			if strings.Contains(r, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("requested %v, want it to include the earliest node %q", got, want)
		}
	}
	for _, notWant := range []string{"d/main.log", "e/main.log"} {
		for _, r := range got {
			if strings.Contains(r, notWant) {
				t.Errorf("requested %v, want the later nodes dropped in favor of the earliest", got)
			}
		}
	}
}

// TestBuildKeepsFirstFailureAcrossRetries covers a run where the last node to fail
// is not the one that explains the run: three attempts of the same retried step, the
// first genuinely different from the later two (which look identical to each
// other - the retry re-hit the same error). The earliest attempt's error must be the
// one that survives into LogExcerpt, and the identical text on attempts 2 and 3 must
// collapse into a single bullet rather than repeating.
func TestBuildKeepsFirstFailureAcrossRetries(t *testing.T) {
	mkNode := func(id, finishedAt string) Node {
		return Node{
			ID: id, Type: "Pod", Phase: "Failed", DisplayName: "upgrade(" + id + ")", FinishedAt: finishedAt,
			Outputs: NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id + "/main.log"}}}},
		}
	}
	wf := Workflow{
		Metadata: WorkflowMetadata{Name: "harness-retry-abcde", Labels: map[string]string{"harness/config": "retry"}},
		Status: WorkflowStatus{
			Phase:                 "Failed",
			ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
			Nodes: map[string]Node{
				"0": mkNode("0", "2026-08-04T00:00:01Z"),
				"1": mkNode("1", "2026-08-04T00:00:02Z"),
				"2": mkNode("2", "2026-08-04T00:00:03Z"),
			},
		},
	}
	logs := map[string]string{
		"b/0/main.log": "Error: worktree add 8f347c78: exit status 128\n",
		"b/1/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
		"b/2/main.log": "Error: context deadline exceeded waiting for helm upgrade\n",
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), WorkflowList{Items: []Workflow{wf}}, stub, BuildOptions{})
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	got := children[0].LogExcerpt
	if !strings.Contains(got, "worktree add 8f347c78") {
		t.Errorf("LogExcerpt = %q, want the first (earliest) attempt's error to survive", got)
	}
	if n := strings.Count(got, "context deadline exceeded"); n != 1 {
		t.Errorf("LogExcerpt = %q, want the identical retry excerpt deduped to 1 occurrence, got %d", got, n)
	}
}

func TestBuildFallsBackToNodeMessage(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return "", errors.New("s3: access denied")
	}}
	children := Build(context.Background(), list, stub, BuildOptions{})

	for _, c := range children {
		if c.LogExcerpt != "" {
			t.Errorf("child %q: LogExcerpt = %q, want \"\" (fetch failed)", c.Name, c.LogExcerpt)
		}
		if c.Config == "bi_ha" || c.Config == "min_default" {
			if !strings.Contains(c.Detail, "main: Error (exit code 1)") {
				t.Errorf("child %q: Detail = %q, want today's node-summary text", c.Name, c.Detail)
			}
		}
	}
}

func TestBuildRespectsWireBudget(t *testing.T) {
	bigErr := "Error: " + strings.Repeat("something failed badly here ", 300)
	var items []Workflow
	for i := 0; i < 20; i++ {
		nodes := map[string]Node{}
		for j := 0; j < 3; j++ {
			id := "n" + string(rune('a'+i)) + string(rune('0'+j))
			nodes[id] = Node{
				ID: id, Type: "Pod", Phase: "Failed", DisplayName: id,
				FinishedAt: "2026-08-04T00:00:00Z",
				Outputs:    NodeOutputs{Artifacts: []NodeArtifact{{Name: "main-logs", S3: &NodeArtifactS3{Key: id}}}},
			}
		}
		items = append(items, Workflow{
			Metadata: WorkflowMetadata{Name: "wf" + string(rune('a'+i)), Labels: map[string]string{"harness/config": "c"}},
			Status: WorkflowStatus{
				Phase:                 "Failed",
				ArtifactRepositoryRef: ArtifactRepositoryRef{ArtifactRepository{S3RepoRef{Bucket: "b"}}},
				Nodes:                 nodes,
			},
		})
	}
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return bigErr + "\n", nil
	}}
	children := Build(context.Background(), WorkflowList{Items: items}, stub, BuildOptions{})
	for _, c := range children {
		if n := utf8.RuneCountInString(c.LogExcerpt); n > maxChildExcerptRunes {
			t.Errorf("child %q: log_excerpt is %d runes, want <= %d", c.Name, n, maxChildExcerptRunes)
		}
		if !utf8.ValidString(c.LogExcerpt) {
			t.Errorf("child %q: log_excerpt is not valid UTF-8", c.Name)
		}
		if c.LogExcerpt != "" && !strings.HasSuffix(c.LogExcerpt, "…") {
			t.Errorf("child %q: log_excerpt = %q, want it truncated with an ellipsis", c.Name, c.LogExcerpt)
		}
	}
}

// TestBuildDMSDiagnosticSurvivesTruncation is the CRITICAL regression for the
// aws-sdk-go-v2 boilerplate strip: it asserts on Build's actual card-visible output
// (ChildEvidence.LogExcerpt), not just ExtractError's return value, since Build
// applies its own truncation on top of ExtractError and that is where the
// diagnostic was getting cut off before the fix.
func TestBuildDMSDiagnosticSurvivesTruncation(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), list, stub, BuildOptions{})

	var biHa *ChildEvidence
	for i := range children {
		if children[i].Config == "bi_ha" {
			biHa = &children[i]
		}
	}
	if biHa == nil {
		t.Fatal("no bi_ha child in Build() output")
	}
	for _, want := range []string{"InvalidResourceStateFault", "cannot be stopped"} {
		if !strings.Contains(biHa.LogExcerpt, want) {
			t.Errorf("bi_ha LogExcerpt = %q, want it to contain %q (the DMS diagnostic must survive both the per-line and per-child truncation)", biHa.LogExcerpt, want)
		}
	}
}

// TestBuildExtractsFailedPhaseFromVerdictLine covers the primary source for
// failed_phase: a "<phase> failed: ..." verdict line in the earliest failed node's
// log. workflows-list.json's bi_ha child has upgrade(0) (2026-08-04T01:06:53Z) as
// its earliest failed node, ahead of destroy(0) (01:30:24Z) - failed_phase must come
// from upgrade's own log, not destroy's, and log_uri must point at upgrade's key.
func TestBuildExtractsFailedPhaseFromVerdictLine(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
	}
	stub := &stubFetcher{logs: logs}
	children := Build(context.Background(), list, stub, BuildOptions{})

	var biHa *ChildEvidence
	for i := range children {
		if children[i].Config == "bi_ha" {
			biHa = &children[i]
		}
	}
	if biHa == nil {
		t.Fatal("no bi_ha child in Build() output")
	}
	if biHa.FailedPhase != "upgrade" {
		t.Errorf("bi_ha FailedPhase = %q, want %q (from upgrade(0)'s own verdict line, not destroy(0)'s)", biHa.FailedPhase, "upgrade")
	}
	wantURI := "s3://dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log"
	if biHa.LogURI != wantURI {
		t.Errorf("bi_ha LogURI = %q, want %q", biHa.LogURI, wantURI)
	}
}

// TestBuildFailedPhaseFallsBackToDisplayName covers the fallback: when the earliest
// node's log carries no "<phase> failed: ..." verdict line (fetch fails here, but a
// verdict-free log hits the same branch), failed_phase comes from the node's
// DisplayName instead. log_uri must still be populated from the known bucket/key
// even though the fetch itself failed.
func TestBuildFailedPhaseFallsBackToDisplayName(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{fn: func(ctx context.Context, bucket, key string) (string, error) {
		return "", errors.New("s3: access denied")
	}}
	children := Build(context.Background(), list, stub, BuildOptions{})

	for _, c := range children {
		if c.Config != "bi_ha" && c.Config != "min_default" {
			continue
		}
		if c.FailedPhase != "upgrade" {
			t.Errorf("child %q: FailedPhase = %q, want %q (DisplayName fallback off %q)", c.Name, c.FailedPhase, "upgrade", "upgrade(0)")
		}
		if c.LogURI == "" {
			t.Errorf("child %q: LogURI is empty, want it populated from the workflow's known bucket/key even though the fetch failed", c.Name)
		}
	}
}

// TestBuildArgoURLRespectsARGOUIBASE covers both ends of the ARGO_UI_BASE contract:
// unset means omit the link entirely, set means build the deep link off it.
func TestBuildArgoURLRespectsARGOUIBASE(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	stub := &stubFetcher{logs: map[string]string{}}

	t.Run("unset", func(t *testing.T) {
		children := Build(context.Background(), list, stub, BuildOptions{})
		for _, c := range children {
			if c.ArgoURL != "" {
				t.Errorf("child %q: ArgoURL = %q, want \"\" when ARGO_UI_BASE is unset", c.Name, c.ArgoURL)
			}
		}
	})

	t.Run("set", func(t *testing.T) {
		t.Setenv("ARGO_UI_BASE", "https://argo.example.internal/")
		children := Build(context.Background(), list, stub, BuildOptions{})
		for _, c := range children {
			want := "https://argo.example.internal/workflows/argo/" + c.Name
			if c.ArgoURL != want {
				t.Errorf("child %q: ArgoURL = %q, want %q", c.Name, c.ArgoURL, want)
			}
		}
	})
}

func TestBuildOutputIsDeterministic(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-000000000000/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-000000000000/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
	}

	run := func() []byte {
		stub := &stubFetcher{logs: logs}
		children := Build(context.Background(), list, stub, BuildOptions{})
		b, err := json.MarshalIndent(children, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}

	first := run()
	for i := 0; i < 5; i++ {
		if got := run(); string(got) != string(first) {
			t.Fatalf("Build() is non-deterministic across runs:\n--- run 0 ---\n%s\n--- run %d ---\n%s", first, i+1, got)
		}
	}

	golden := readTestdata(t, "children.golden.json")
	var gotChildren, wantChildren []ChildEvidence
	if err := json.Unmarshal(first, &gotChildren); err != nil {
		t.Fatalf("unmarshal actual: %v", err)
	}
	if err := json.Unmarshal([]byte(golden), &wantChildren); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	gotJSON, _ := json.Marshal(gotChildren)
	wantJSON, _ := json.Marshal(wantChildren)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("Build() output does not match children.golden.json\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}
