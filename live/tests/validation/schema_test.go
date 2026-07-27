package validation

import (
	"strings"
	"testing"
)

func TestParseSchemaOutputIgnoresPreamble(t *testing.T) {
	// Leading noise is the realistic case: the app image's profile and db_helpers.sh's
	// `set -x` can both write before our first marker.
	out := strings.Join([]string{
		"+ cd /home/ifixit/Code",
		"some shell noise",
		"---WANT---",
		"users",
		"password_requirements_revisions",
		"",
		"---GOT---",
		"users",
	}, "\n")

	want, got := parseSchemaOutput(out)
	if len(want) != 2 || want[0] != "users" || want[1] != "password_requirements_revisions" {
		t.Errorf("want = %v, expected the two declared tables and no preamble", want)
	}
	if len(got) != 1 || got[0] != "users" {
		t.Errorf("got = %v, expected just [users]", got)
	}
}

func TestParseSchemaOutputEmptyDatabase(t *testing.T) {
	// An absent database yields a WANT list and no GOT rows. That must parse as empty
	// rather than erroring, so the caller can report it as the truncation it is.
	want, got := parseSchemaOutput("---WANT---\nusers\n---GOT---\n")
	if len(want) != 1 {
		t.Errorf("want = %v, expected 1 table", want)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, expected none", got)
	}
}

func TestMissingFromIsCaseInsensitive(t *testing.T) {
	// information_schema casing does not always match the dump's; a case difference is
	// not a missing table and must not be reported as one.
	if m := missingFrom([]string{"Users"}, []string{"users"}); len(m) != 0 {
		t.Errorf("missingFrom = %v, expected no missing tables for a case difference", m)
	}
}

func TestMissingFromReportsTruncation(t *testing.T) {
	// The real failure shape: the import died early, so only the first few tables exist.
	want := []string{"a", "b", "password_requirements_revisions", "z"}
	missing := missingFrom(want, []string{"a", "b"})
	if len(missing) != 2 {
		t.Fatalf("missingFrom = %v, expected 2 missing", missing)
	}
	if missing[0] != "password_requirements_revisions" || missing[1] != "z" {
		t.Errorf("missingFrom = %v, expected sorted missing names", missing)
	}
}

func TestMissingFromExtraTablesAreNotFailures(t *testing.T) {
	// Migrations add tables the dump predates. Only absence matters.
	if m := missingFrom([]string{"a"}, []string{"a", "added_later"}); len(m) != 0 {
		t.Errorf("missingFrom = %v, expected extras to be ignored", m)
	}
}

func TestSampleTruncatesWithCount(t *testing.T) {
	s := sample([]string{"a", "b", "c", "d"}, 2)
	if len(s) != 3 || s[2] != "… (+2 more)" {
		t.Errorf("sample = %v, expected 2 names plus a remainder count", s)
	}
	if full := sample([]string{"a"}, 2); len(full) != 1 {
		t.Errorf("sample = %v, expected a short list to pass through unchanged", full)
	}
}

func TestGreenfieldSchemasCoverTheGuideDatabases(t *testing.T) {
	// The guide DBs are where the 8.4 FK truncation landed; a refactor that drops them
	// would leave this check green against the exact bug it exists for.
	seen := map[string]bool{}
	for _, s := range greenfieldSchemas() {
		if s.dump == "" || s.database == "" {
			t.Errorf("incomplete expectation: %+v", s)
		}
		seen[s.database] = true
	}
	// onprem_guide is the tenant the FQDN serves and where the 8.4 truncation landed;
	// dropping it would leave this check green against the exact bug it exists for.
	if !seen["onprem_guide"] {
		t.Error("greenfieldSchemas is missing onprem_guide")
	}
	// metrics is never created by the image's bootstrap and dozuki_guide is not loaded
	// from the guide dump — asserting either produces a false shortfall on every run.
	for _, db := range []string{"metrics", "dozuki_guide"} {
		if seen[db] {
			t.Errorf("greenfieldSchemas must not assert %s: the image's bootstrap does not load a dump into it whole", db)
		}
	}
}
