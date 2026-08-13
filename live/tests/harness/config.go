package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// harnessOnlyKeys are feature_flags keys consumed by the harness itself and
// must NOT be written into env.hcl as terraform inputs.
var harnessOnlyKeys = map[string]bool{
	"restore_drill": true,
	"app_pass":      true,
}

type Defaults struct {
	FromRef           string   `yaml:"from_ref"`
	ToRef             string   `yaml:"to_ref"`
	Region            string   `yaml:"region"`
	DRRegion          string   `yaml:"dr_region"`
	EnvPath           string   `yaml:"env_path"`
	CriticalWorkloads []string `yaml:"critical_workloads"`
	ReaperTTLHours    int      `yaml:"reaper_ttl_hours"`
}

type Config struct {
	Name         string                 `yaml:"name"`
	Env          string                 `yaml:"env"`
	FeatureFlags map[string]interface{} `yaml:"feature_flags"`
	// EnvPath / Region override the matrix defaults for configs that deploy outside
	// the primary region - the recovery config stands its stack up in the DR region.
	EnvPath string `yaml:"env_path"`
	Region  string `yaml:"region"`
	// BaselineVersions / TargetVersions are optional per-config, per-side version-var
	// overrides (app_image_flavor, chart_version, image_tag, ...). They win over
	// version_defaults and versions[ref] for whichever side a caller is rendering -
	// see Side, Matrix.MergedInputs and Matrix.EffectiveVersionVar. A config that sets
	// neither (every pre-existing config) merges exactly as it did before these
	// existed: both maps are nil, so the extra merge layer is a no-op.
	BaselineVersions map[string]interface{} `yaml:"baseline_versions"`
	TargetVersions   map[string]interface{} `yaml:"target_versions"`
}

// Side names which half of an upgrade a ref/inputs render represents. It is always
// supplied explicitly by the caller (PhaseParams.prepareWorktreeSide's callers in
// phases.go) and never inferred from comparing a ref string to FromRef/ToRef - that
// comparison breaks the moment FromRef == ToRef (a docs-only bump, or a flavor flip
// applied in place), which is exactly the case AppliedSide (manifest.go) exists to
// disambiguate durably.
type Side string

const (
	SideBaseline Side = "baseline"
	SideTarget   Side = "target"
)

// sideVersions returns c's version-override map for side, or nil if side is neither
// known value (there are only two; nil merges as a no-op).
func (c Config) sideVersions(side Side) map[string]interface{} {
	switch side {
	case SideBaseline:
		return c.BaselineVersions
	case SideTarget:
		return c.TargetVersions
	default:
		return nil
	}
}

// EnvPathOr / RegionOr resolve a config's overrides against the matrix defaults.
func (c Config) EnvPathOr(def string) string {
	if c.EnvPath != "" {
		return c.EnvPath
	}
	return def
}

func (c Config) RegionOr(def string) string {
	if c.Region != "" {
		return c.Region
	}
	return def
}

type Matrix struct {
	Defaults Defaults `yaml:"defaults"`
	// VersionDefaults are version vars (image_tag, chart_version, …) applied to
	// EVERY ref. A ref's entry in Versions overrides matching keys. This lets a
	// ref with no explicit Versions entry (e.g. a freshly tagged release, or
	// auto:latest) still resolve — most refs share the same images/charts, so
	// you set them once here instead of per ref.
	VersionDefaults map[string]interface{}            `yaml:"version_defaults"`
	Versions        map[string]map[string]interface{} `yaml:"versions"`
	Configs         []Config                          `yaml:"configs"`
}

func LoadMatrix(path string) (*Matrix, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read matrix: %w", err)
	}
	var m Matrix
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse matrix: %w", err)
	}
	return &m, nil
}

// Salted returns a copy of c with a run-derived suffix appended to the
// "customer" feature flag, so two runs of the same config never collide on
// the IAM/Vault/k8s resource names derived from it. The FeatureFlags map is
// deep-copied first - Matrix.Config returns the matrix's own map by
// reference, and mutating it in place would corrupt every other holder of
// this config. A no-op (unchanged copy) when runID is empty, customer is
// absent/non-string/empty, or customer is already at the 10-char terraform
// cap. Deterministic in (runID, c.Name): every phase of a run, however many
// separate pods it spans, must independently recompute the identical salted
// customer or a later phase renders a different identifier than an earlier
// one applied.
func (c Config) Salted(runID string) Config {
	out := c
	flags := make(map[string]interface{}, len(c.FeatureFlags))
	for k, v := range c.FeatureFlags {
		flags[k] = v
	}
	out.FeatureFlags = flags

	customer, ok := flags["customer"].(string)
	if !ok || customer == "" || runID == "" {
		return out
	}
	n := 10 - len(customer)
	if n > 4 {
		n = 4
	}
	if n <= 0 {
		return out
	}
	sum := sha256.Sum256([]byte(runID + "/" + c.Name))
	flags["customer"] = customer + hex.EncodeToString(sum[:])[:n]
	return out
}

func (m *Matrix) Config(name string) (Config, error) {
	for _, c := range m.Configs {
		if c.Name == name {
			return c, nil
		}
	}
	return Config{}, fmt.Errorf("config %q not found in matrix", name)
}

// MergedInputs resolves the full terraform-input map for (c, ref, side). Merge order
// (later layers win): feature_flags -> version_defaults -> versions[ref] ->
// c.BaselineVersions or c.TargetVersions per side. side is required and must be
// supplied explicitly by the caller - see Side. Callers that also seed runtime
// ExtraInputs (recovery's adopted-bucket/snapshot values) must merge those AFTER this
// return value, not before - see PhaseParams.prepareWorktreeSide.
func (m *Matrix) MergedInputs(c Config, ref string, side Side) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range c.FeatureFlags {
		if !harnessOnlyKeys[k] {
			out[k] = v
		}
	}
	// version_defaults first, then the ref-specific entry overrides them.
	for k, v := range m.VersionDefaults {
		out[k] = v
	}
	for k, v := range m.Versions[ref] {
		out[k] = v
	}
	// The config's per-side override map wins over everything above. nil on every
	// config that predates this field, so this loop is a no-op for them.
	for k, v := range c.sideVersions(side) {
		out[k] = v
	}
	out["environment"] = c.Env
	return out
}

// EffectiveVersionVar resolves a single version-var key's precedence (feature_flags ->
// version_defaults -> versions[ref] -> the config's side map). It is NOT a drop-in
// substitute for MergedInputs on every key: MergedInputs filters feature_flags through
// harnessOnlyKeys (so a harness-only key like restore_drill never reaches this layer)
// and always writes out["environment"] last, and this function does neither. It is
// valid only for genuine version-var keys (app_image_flavor, chart_version, image_tag,
// ...) - never for a harnessOnlyKeys entry or "environment".
func (m *Matrix) EffectiveVersionVar(c Config, ref string, side Side, key string) interface{} {
	var val interface{}
	if v, ok := c.FeatureFlags[key]; ok {
		val = v
	}
	if v, ok := m.VersionDefaults[key]; ok {
		val = v
	}
	if rv, ok := m.Versions[ref]; ok {
		if v, ok := rv[key]; ok {
			val = v
		}
	}
	if v, ok := c.sideVersions(side)[key]; ok {
		val = v
	}
	return val
}

// HarnessFlag returns the boolean value of a harness-only feature flag for
// this config (e.g. "restore_drill"). Returns false if absent or non-bool.
func (c Config) HarnessFlag(name string) bool {
	if v, ok := c.FeatureFlags[name]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// VersionProfileExists reports whether a ref can be resolved: either it has an
// explicit Versions entry, or version_defaults supplies a base for any ref.
func (m *Matrix) VersionProfileExists(ref string) bool {
	if _, ok := m.Versions[ref]; ok {
		return true
	}
	return len(m.VersionDefaults) > 0
}
