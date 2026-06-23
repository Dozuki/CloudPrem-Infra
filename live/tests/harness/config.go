package harness

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// harnessOnlyKeys are feature_flags keys consumed by the harness itself and
// must NOT be written into env.hcl as terraform inputs.
var harnessOnlyKeys = map[string]bool{
	"restore_drill": true,
}

type Defaults struct {
	FromRef           string   `yaml:"from_ref"`
	ToRef             string   `yaml:"to_ref"`
	Region            string   `yaml:"region"`
	DRRegion          string   `yaml:"dr_region"`
	EnvPath           string   `yaml:"env_path"`
	CriticalWorkloads []string `yaml:"critical_workloads"`
}

type Config struct {
	Name         string                 `yaml:"name"`
	Env          string                 `yaml:"env"`
	FeatureFlags map[string]interface{} `yaml:"feature_flags"`
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

func (m *Matrix) Config(name string) (Config, error) {
	for _, c := range m.Configs {
		if c.Name == name {
			return c, nil
		}
	}
	return Config{}, fmt.Errorf("config %q not found in matrix", name)
}

func (m *Matrix) MergedInputs(c Config, ref string) map[string]interface{} {
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
	out["environment"] = c.Env
	return out
}

// VersionVar returns a single version variable for a ref: the ref-specific
// entry wins, otherwise the version_defaults value (nil if neither sets it).
func (m *Matrix) VersionVar(ref, key string) interface{} {
	if rv, ok := m.Versions[ref]; ok {
		if v, ok := rv[key]; ok {
			return v
		}
	}
	return m.VersionDefaults[key]
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
