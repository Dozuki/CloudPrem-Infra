package validation

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
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

// AssertUpgraded verifies the release is deployed, its revision increased vs
// baselineRevision, and (when wantChartVersion != "") the chart version matches.
func AssertUpgraded(kubeconfig, namespace, name string, baselineRevision int, wantChartVersion string) error {
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
		if rev <= baselineRevision {
			return fmt.Errorf("release %s revision=%d not greater than baseline=%d", name, rev, baselineRevision)
		}
		if wantChartVersion != "" {
			want := name + "-" + wantChartVersion
			if r.Chart != want {
				return fmt.Errorf("release chart=%q, want %q", r.Chart, want)
			}
		}
		return nil
	}
	return fmt.Errorf("release %s not found", name)
}
