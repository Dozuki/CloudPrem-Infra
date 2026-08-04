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

func TestScrubKeepsDiagnosticSurvivors(t *testing.T) {
	survivors := []string{
		"8f347c78fb11039a9737c30e956fa16e5f57cd78", // 40-hex git SHA
		"arn:aws:dms:us-east-1:000000000000:replication-config:REPLICATIONCONFIGID",
		"dozuki-argo-artifacts-010601635461",
		"smokef9b3-bi-reader",
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
		"dozuki-argo-artifacts-010601635461/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log",
		"dozuki-argo-artifacts-010601635461/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log",
		"dozuki-argo-artifacts-010601635461/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log",
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
	n := len(stub.requested)
	stub.mu.Unlock()
	if n != 3 {
		t.Errorf("requested %d objects, want 3 (the per-child node cap)", n)
	}
	for _, r := range stub.requested {
		if strings.Contains(r, "retry-a") {
			t.Errorf("Retry node was fetched: %v", stub.requested)
		}
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

func TestBuildOutputIsDeterministic(t *testing.T) {
	list := loadWorkflowList(t, "workflows-list.json")
	logs := map[string]string{
		"dozuki-argo-artifacts-010601635461/harness-min-default-mdgxp/harness-min-default-mdgxp-run-1651265260/main.log": readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-010601635461/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-3742410289/main.log":             readTestdata(t, "min-default-upgrade.log"),
		"dozuki-argo-artifacts-010601635461/harness-bi-ha-8dps9/harness-bi-ha-8dps9-run-945485330/main.log":              readTestdata(t, "bi-ha-destroy-tail.log"),
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
