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
	Defaults Defaults                          `yaml:"defaults"`
	Versions map[string]map[string]interface{} `yaml:"versions"`
	Configs  []Config                          `yaml:"configs"`
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
	for k, v := range m.Versions[ref] {
		out[k] = v
	}
	out["environment"] = c.Env
	return out
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

func (m *Matrix) VersionProfileExists(ref string) bool {
	_, ok := m.Versions[ref]
	return ok
}
