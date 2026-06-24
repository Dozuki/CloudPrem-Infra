package main

import (
	"time"

	"github.com/Dozuki/CloudPrem-Infra/live/tests/harness"
)

// deleteAfterFromTTL returns now()+ttl as an RFC3339 UTC string. Only used when a
// manifest is first created; thereafter the stored value is reused.
func deleteAfterFromTTL(ttlHours int) string {
	return time.Now().Add(time.Duration(ttlHours) * time.Hour).UTC().Format(time.RFC3339)
}

// resolveRefs resolves auto:latest / auto:latest-1 against the repo's tags and
// blanks fromRef for the fresh scenario.
func resolveRefs(repoDir string, m *harness.Matrix, fromRef, toRef, scenario string) (string, string) {
	harness.FetchOrigin(repoDir)
	tags, _ := harness.NewestTags(repoDir)
	if fromRef == "" {
		fromRef = m.Defaults.FromRef
	}
	if toRef == "" {
		toRef = m.Defaults.ToRef
	}
	fr, _ := harness.ResolveRef(fromRef, tags)
	tr, _ := harness.ResolveRef(toRef, tags)
	if scenario == "fresh" {
		fr = ""
	}
	return fr, tr
}
