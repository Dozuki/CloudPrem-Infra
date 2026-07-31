package validation

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type helmRelease struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
	Status   string `json:"status"`
	Chart    string `json:"chart"` // e.g. "dozuki-0.3.0"
}

func helmList(kubeconfig, namespace string) ([]helmRelease, error) {
	cmd := exec.Command("helm", "list", "-n", namespace, "-o", "json", "--kubeconfig", kubeconfig)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("helm list: %w", err)
	}
	var rels []helmRelease
	if err := json.Unmarshal(out, &rels); err != nil {
		return nil, fmt.Errorf("parse helm list: %w", err)
	}
	return rels, nil
}

// ReleaseRevision returns the integer revision of the named release.
func ReleaseRevision(kubeconfig, namespace, name string) (int, error) {
	rels, err := helmList(kubeconfig, namespace)
	if err != nil {
		return 0, err
	}
	for _, r := range rels {
		if r.Name == name {
			return strconv.Atoi(r.Revision)
		}
	}
	return 0, fmt.Errorf("release %s not found", name)
}

// AssertUpgraded verifies the release is deployed, its revision advanced vs
// baselineRevision, and (when wantChartVersion != "") the chart version matches.
// mustAdvance=false relaxes the revision check to "did not go backwards": a ref
// delta with no chart delta (a docs-only PR) is a no-op for Flux, so the revision
// legitimately stays put and only a rollback would be wrong.
func AssertUpgraded(kubeconfig, namespace, name string, baselineRevision int, wantChartVersion string, mustAdvance bool) error {
	rels, err := helmList(kubeconfig, namespace)
	if err != nil {
		return err
	}
	for _, r := range rels {
		if r.Name != name {
			continue
		}
		if r.Status != "deployed" {
			return fmt.Errorf("release %s status=%s, want deployed", name, r.Status)
		}
		rev, err := strconv.Atoi(r.Revision)
		if err != nil {
			return err
		}
		if mustAdvance && rev <= baselineRevision {
			return fmt.Errorf("release %s revision=%d not greater than baseline=%d", name, rev, baselineRevision)
		}
		if rev < baselineRevision {
			return fmt.Errorf("release %s revision=%d went backwards from baseline=%d", name, rev, baselineRevision)
		}
		if wantChartVersion != "" {
			want := name + "-" + wantChartVersion
			if stripBuildMetadata(r.Chart) != stripBuildMetadata(want) {
				return fmt.Errorf("release chart=%q, want %q", r.Chart, want)
			}
		}
		return nil
	}
	return fmt.Errorf("release %s not found", name)
}

// stripBuildMetadata drops a SemVer build-metadata suffix (everything from the first '+').
//
// The published chart carries one: helm reports the deployed release as
// "dozuki-1.26.1+731c2096df83" while the matrix pins "1.26.1". SemVer says build metadata
// is ignored when determining version precedence, so an exact string compare is simply the
// wrong test - it failed an upgrade that had in fact deployed the requested chart.
//
// Only the metadata is dropped. A pre-release suffix ("-rc.1") is part of precedence and
// stays, so a chart that really is a different version still fails.
func stripBuildMetadata(v string) string {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		return v[:i]
	}
	return v
}
