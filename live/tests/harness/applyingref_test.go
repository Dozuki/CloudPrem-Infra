package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// hftj2Manifest is the real smoke1bc2 manifest as it stood after the run was superseded
// mid-apply: scenario upgrade, baseline v9.6.0, target 4ef62211, and applied_ref EMPTY
// because Provision only wrote it after an apply that never returned.
func hftj2Manifest() *RunManifest {
	return &RunManifest{
		Scenario: "upgrade", ConfigName: "bi_ha",
		FromRef: "v9.6.0", ToRef: "4ef62211bde058785c139c22b8f172ae513b8db5",
		DeleteAfter: "2026-08-18T01:20:08Z",
	}
}

// Without ApplyingRef, every ref resolution for a killed run falls through to ToRef -
// so the teardown of a BASELINE-built stack runs TARGET code. That is what happened to
// harness-bi-ha-hftj2 (bd Lodestar-1xm.36).
func TestKilledMidApplyResolvesToTheRefThatWasApplying(t *testing.T) {
	rm := hftj2Manifest()
	rm.ApplyingRef, rm.ApplyingSide = "v9.6.0", string(SideBaseline)

	if got := TeardownRepoRef(rm); got != "v9.6.0" {
		t.Errorf("TeardownRepoRef = %q, want the baseline ref the apply was running with", got)
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide: %v", err)
	}
	if ref != "v9.6.0" || side != SideBaseline {
		t.Errorf("teardown would destroy against (%s, %s), want (v9.6.0, baseline)", ref, side)
	}
}

// A completed apply writes both fields with the same ref, and resolution follows it.
// (The two can only disagree while an apply is unfinished - that is what makes
// "ApplyingRef != AppliedRef" a safe test for "an apply did not finish".)
func TestCompletedApplyLeavesBothFieldsAgreeing(t *testing.T) {
	rm := hftj2Manifest()
	rm.ApplyingRef, rm.ApplyingSide = rm.ToRef, string(SideTarget)
	rm.AppliedRef, rm.AppliedSide = rm.ToRef, string(SideTarget)

	if got := TeardownRepoRef(rm); got != rm.ToRef {
		t.Errorf("TeardownRepoRef = %q, want the completed apply's ref", got)
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide: %v", err)
	}
	if ref != rm.ToRef || side != SideTarget {
		t.Errorf("teardown resolved (%s, %s), want the completed apply's (ref, side)", ref, side)
	}
}

// A manifest predating both fields keeps its old behaviour.
func TestPreApplyingRefManifestStillFallsBackToToRef(t *testing.T) {
	rm := hftj2Manifest()
	if got := TeardownRepoRef(rm); got != rm.ToRef {
		t.Errorf("TeardownRepoRef = %q, want the ToRef fallback for a manifest with neither field", got)
	}
}

// Validate's precondition must keep meaning "the target apply SUCCEEDED". An upgrade
// that started and died leaves ApplyingRef == ToRef; if that satisfied the precondition,
// Validate would render the target side against a half-applied stack.
func TestApplyingRefDoesNotSatisfyValidatePrecondition(t *testing.T) {
	rm := hftj2Manifest()
	rm.ApplyingRef, rm.ApplyingSide = rm.ToRef, string(SideTarget)
	if err := validatePreconditions(rm); err == nil {
		t.Error("validatePreconditions accepted a manifest where the target apply only STARTED; it must require a completed apply")
	}
}

// The inverse failure codex flagged: baseline provision COMPLETED, then the target
// upgrade was killed. AppliedRef names the baseline, ApplyingRef the target - and the
// target apply is what last touched the infrastructure, so teardown must use it.
func TestKilledMidUpgradeResolvesToTheTargetNotTheCompletedBaseline(t *testing.T) {
	rm := hftj2Manifest()
	rm.AppliedRef, rm.AppliedSide = "v9.6.0", string(SideBaseline) // provision finished
	rm.ApplyingRef, rm.ApplyingSide = rm.ToRef, string(SideTarget) // upgrade started, killed

	if got := TeardownRepoRef(rm); got != rm.ToRef {
		t.Errorf("TeardownRepoRef = %q, want the target ref the killed upgrade was applying", got)
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide: %v", err)
	}
	if ref != rm.ToRef || side != SideTarget {
		t.Errorf("teardown would destroy against (%s, %s), want the target the upgrade was mid-way through", ref, side)
	}
}

// applyingWriteOrder is the placement rule, checked structurally because the two things
// it sits between are what make it correct: AFTER prepareWorktreeSide (a ref whose
// worktree cannot be prepared has created nothing, and recording it would point every
// later teardown at a preparation that fails identically) and BEFORE any Apply (from the
// first Create* call the live stack corresponds to that ref, killed or not).
func TestApplyingRefIsWrittenBetweenWorktreePrepAndApply(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "phases.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "Provision" && fn.Name.Name != "Upgrade") {
			continue
		}
		var prep, write, apply int
		ast.Inspect(fn, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if sel, isSel := lhs.(*ast.SelectorExpr); isSel && sel.Sel.Name == "ApplyingRef" && write == 0 {
						write = fset.Position(node.Pos()).Line
					}
				}
			case *ast.CallExpr:
				sel, isSel := node.Fun.(*ast.SelectorExpr)
				if !isSel {
					return true
				}
				if strings.HasPrefix(sel.Sel.Name, "prepareWorktree") && prep == 0 {
					prep = fset.Position(node.Pos()).Line
				}
				if strings.HasPrefix(sel.Sel.Name, "Apply") && apply == 0 {
					apply = fset.Position(node.Pos()).Line
				}
			}
			return true
		})
		if prep == 0 || write == 0 || apply == 0 {
			t.Fatalf("%s: could not locate all three of worktree prep (%d), ApplyingRef write (%d), apply (%d)", fn.Name.Name, prep, write, apply)
		}
		if !(prep < write && write < apply) {
			t.Errorf("%s: ApplyingRef is written at line %d; it must sit after the worktree prep (line %d) and before the apply (line %d)", fn.Name.Name, write, prep, apply)
		}
		checked++
	}
	if checked != 2 {
		t.Fatalf("checked %d phases, want both Provision and Upgrade", checked)
	}
}

// FromRef == ToRef is a real configuration (a docs-only bump, or a flavor flip applied
// in place). A mid-upgrade kill there leaves the refs equal and only the SIDES differ,
// so a ref-only comparison would resolve the run to the stale baseline side and render
// the wrong version-override map at destroy.
func TestSameRefUpgradeKilledMidApplyResolvesToTheTargetSide(t *testing.T) {
	rm := &RunManifest{Scenario: "upgrade", ConfigName: "min_default", FromRef: "v9.7.0", ToRef: "v9.7.0"}
	rm.AppliedRef, rm.AppliedSide = "v9.7.0", string(SideBaseline) // provision finished
	rm.ApplyingRef, rm.ApplyingSide = "v9.7.0", string(SideTarget) // upgrade started, killed

	if !rm.applyUnfinished() {
		t.Fatal("applyUnfinished = false for a same-ref upgrade killed mid-apply; the refs are equal, only the sides differ")
	}
	ref, side, err := teardownRefAndSide(rm)
	if err != nil {
		t.Fatalf("teardownRefAndSide: %v", err)
	}
	if ref != "v9.7.0" || side != SideTarget {
		t.Errorf("teardown resolved (%s, %s), want (v9.7.0, target)", ref, side)
	}
}
