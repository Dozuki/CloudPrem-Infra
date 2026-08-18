package harness

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The defect this file guards (Lodestar-1xm.36.3): a teardown that recomputes
// deleteAfter as now+TTL re-arms the ResourceReaper clock on resources it failed to
// destroy, so a retry-looping teardown can hold its own residue alive indefinitely.
// The rule is: resources that already exist keep the clock recorded in the manifest;
// only a phase that CREATES resources may start a new one.
//
// The 2026-08-18 smoke1bc2 investigation found the harness already obeys this - the
// live +24h tag that prompted the issue was written by the ResourceReaper's own
// approve action (resource-reaper-interactions, deleteAfter = its run timestamp in
// Python isoformat, "+00:00" rather than the harness's "Z"), not by any teardown. These
// tests exist so the property stays true rather than being true by luck.

// findFunc returns the FuncDecl named name (optionally on receiver recv) in dir.
func findFunc(t *testing.T, dir, name string) (*token.FileSet, *ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Name.Name == name && fn.Body != nil {
					return fset, fn
				}
			}
		}
	}
	t.Fatalf("function %q not found in %s", name, dir)
	return nil, nil
}

// exprText renders an expression back to source text for exact comparison.
func exprText(fset *token.FileSet, e ast.Expr) string {
	start := fset.Position(e.Pos())
	end := fset.Position(e.End())
	b, err := os.ReadFile(start.Filename)
	if err != nil {
		return ""
	}
	return string(b[start.Offset:end.Offset])
}

// Teardown must thread the manifest's recorded value into the env.hcl render and
// nothing else. prepareWorktreeSide's 4th parameter IS the delete_after that lands in
// the generated env.hcl, which becomes the provider default_tags deleteAfter on every
// resource the destroy touches.
func TestTeardownPassesRecordedDeleteAfterToTheRender(t *testing.T) {
	fset, fn := findFunc(t, ".", "Teardown")
	found := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "prepareWorktree") {
			return true
		}
		found++
		if len(call.Args) < 4 {
			t.Fatalf("%s call in Teardown has %d args, want at least 4", sel.Sel.Name, len(call.Args))
		}
		if got := exprText(fset, call.Args[3]); got != "rm.DeleteAfter" {
			t.Errorf("Teardown passes deleteAfter = %q to %s, want the manifest's recorded rm.DeleteAfter", got, sel.Sel.Name)
		}
		return true
	})
	if found == 0 {
		t.Fatal("no prepareWorktree* call found in Teardown; this guard would pass vacuously")
	}
}

// A fresh clock can only ever come from time arithmetic. Teardown containing any is
// the shape of the defect, whatever it is later used for, so ban it outright rather
// than trying to decide at review time whether a particular Add() is harmless.
func TestTeardownComputesNoFreshTimestamp(t *testing.T) {
	for _, name := range []string{"Teardown", "ResetLogical"} {
		fset, fn := findFunc(t, ".", name)
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, isIdent := sel.X.(*ast.Ident)
			if isIdent && pkg.Name == "time" && (sel.Sel.Name == "Now" || sel.Sel.Name == "Since") {
				t.Errorf("%s calls time.%s at %s; a teardown must reuse the manifest's recorded DeleteAfter, never compute a fresh one",
					name, sel.Sel.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
}

// The one legitimate fresh computation lives on the CREATE path. deleteAfterFromTTL is
// the only function that mints one, and it must stay wired to provision alone - if a
// teardown or resume branch ever calls it, this fails.
func TestDeleteAfterFromTTLIsOnlyCalledByProvision(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../cmd/harness", nil, 0)
	if err != nil {
		t.Fatalf("parse cmd/harness: %v", err)
	}
	calls := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "deleteAfterFromTTL" {
					return true
				}
				calls++
				// Walk out to the enclosing case clause and demand it is "provision".
				if !inProvisionCase(file, call.Pos()) {
					t.Errorf("deleteAfterFromTTL called at %s outside the provision branch; only a phase that CREATES resources may mint a new clock",
						fset.Position(call.Pos()))
				}
				return true
			})
		}
	}
	if calls == 0 {
		t.Fatal("no deleteAfterFromTTL call found in cmd/harness; this guard would pass vacuously")
	}
}

// inProvisionCase reports whether pos sits inside a `case "provision":` clause.
func inProvisionCase(file *ast.File, pos token.Pos) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		cc, ok := n.(*ast.CaseClause)
		if !ok || pos < cc.Pos() || pos > cc.End() {
			return true
		}
		for _, e := range cc.List {
			if lit, ok := e.(*ast.BasicLit); ok && lit.Value == `"provision"` {
				found = true
			}
		}
		return true
	})
	return found
}

// Behavioral half of the same property: run the real Teardown against a seeded
// manifest and prove the value it persists back is byte-identical. Teardown writes the
// manifest (the keep-on-failure record) BEFORE it reaches the worktree, so the destroy
// failing for want of a git repo is expected and does not weaken the assertion.
func TestTeardownDoesNotMutateRecordedDeleteAfter(t *testing.T) {
	ctx := context.Background()
	const recorded = "2026-08-18T01:20:08Z"
	m := &Matrix{Configs: []Config{{Name: "min_default", Env: "min"}}}
	store := NewMemStore()
	p := PhaseParams{Matrix: m, Store: store, ConfigName: "min_default", RunID: "run1", RepoDir: t.TempDir()}
	seed := &RunManifest{
		Scenario: "fresh", ConfigName: "min_default", ToRef: "v9.6.0", AppliedRef: "v9.6.0",
		AppliedSide: string(SideTarget), DeleteAfter: recorded,
	}
	if err := store.Save(ctx, p.statePrefix(m.Configs[0]), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Expected to fail: RepoDir is not a git repo, so the worktree step errors. The
	// manifest write under test already happened by then.
	_ = p.Teardown(ctx, false, false)

	got, ok, err := store.Load(ctx, p.statePrefix(m.Configs[0]))
	if err != nil || !ok {
		t.Fatalf("reload manifest: ok=%v err=%v", ok, err)
	}
	if got.DeleteAfter != recorded {
		t.Errorf("teardown rewrote delete_after: got %q, want %q (byte-identical)", got.DeleteAfter, recorded)
	}
}

// The carve-out the plan calls for - "a resource newly CREATED during teardown gets a
// fresh now+TTL tag" - has no live case, and this is what keeps it that way. A final
// RDS snapshot is the only resource a harness destroy can create, terraform only makes
// one when skip_final_snapshot is false, and that is !protect_resources. The physical
// module DEFAULTS protect_resources to true, so a config that simply omits the flag
// would start minting snapshots stamped with an already-expired recorded clock and the
// reaper would collect them on its next pass.
func TestAllMatrixConfigsDisableProtectResources(t *testing.T) {
	m, err := LoadMatrix("../matrix.yaml")
	if err != nil {
		t.Fatalf("LoadMatrix: %v", err)
	}
	if len(m.Configs) == 0 {
		t.Fatal("matrix has no configs; the guard below would pass vacuously")
	}
	for _, c := range m.Configs {
		v, ok := c.FeatureFlags["protect_resources"]
		if !ok {
			t.Errorf("config %q does not set protect_resources; terraform/physical defaults it to true, so teardown would take a final snapshot tagged with the run's already-expired deleteAfter", c.Name)
			continue
		}
		if v != false {
			t.Errorf("config %q has protect_resources = %v, want false", c.Name, v)
		}
	}
}

// Guard the guard: matrix.yaml must actually parse as the file the test above reads,
// so a rename cannot turn the invariant into a vacuous pass.
func TestMatrixFileIsWhereTheInvariantsLook(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "matrix.yaml"))
	if err != nil {
		t.Fatalf("read matrix.yaml: %v", err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("matrix.yaml is not valid yaml: %v", err)
	}
	if _, ok := doc["configs"]; !ok {
		t.Fatal("matrix.yaml has no top-level configs key")
	}
}

// AC6's other half (Lodestar-1xm.36.5): "a teardown submitted without repo-ref uses
// AppliedRef". The Go side is TestTeardownRepoRef; this is the wiring - every Argo entry
// point must DEFAULT repo-ref to the sentinel the entrypoint acts on. A default of
// "master" is what made the frozen-ref rule a thing a human had to remember, and it is
// indistinguishable at the pod from someone deliberately asking for master.
func TestArgoTemplatesDefaultRepoRefToAuto(t *testing.T) {
	files := []string{
		"../argo/00-phase-templates.yaml",
		"../argo/10-scenario.yaml",
		"../argo/20-matrix.yaml",
	}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) != "- name: repo-ref" {
				continue
			}
			// The default is the next `value:` line, skipping the comment block.
			val := ""
			for j := i + 1; j < len(lines) && j < i+12; j++ {
				s := strings.TrimSpace(lines[j])
				if strings.HasPrefix(s, "#") || s == "" {
					continue
				}
				if strings.HasPrefix(s, "value:") {
					val = strings.Trim(strings.TrimSpace(strings.TrimPrefix(s, "value:")), `'"`)
				}
				break
			}
			// A pass-through to the workflow parameter is not a default; only the
			// declaration sites carry one.
			if strings.Contains(val, "{{") {
				continue
			}
			checked++
			if val != "auto" {
				t.Errorf("%s:%d declares repo-ref with default %q, want \"auto\" so teardown can resolve it from the run manifest", f, i+1, val)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no repo-ref declaration found in the argo templates; this guard would pass vacuously")
	}
}
