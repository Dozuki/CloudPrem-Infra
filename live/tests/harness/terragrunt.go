package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type TGOptions struct {
	WorkingDir   string
	AccountID    string
	Region       string
	Profile      string
	BucketPrefix string
	StatePrefix  string
}

func (o TGOptions) env() []string {
	return append(os.Environ(),
		"TG_AWS_ACCT_ID="+o.AccountID,
		"TG_AWS_REGION="+o.Region,
		"TG_AWS_PROFILE="+o.Profile,
		"TG_BUCKET_PREFIX="+o.BucketPrefix,
		"TG_STATE_PREFIX="+o.StatePrefix,
		"TG_NON_INTERACTIVE=true",
	)
}

func (o TGOptions) exec(args ...string) error {
	cmd := exec.Command("terragrunt", args...)
	cmd.Dir = o.WorkingDir
	cmd.Env = o.env()
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (o TGOptions) Apply() error {
	return o.exec("run-all", "apply", "--terragrunt-non-interactive", "-auto-approve")
}

func (o TGOptions) Destroy() error {
	return o.exec("run-all", "destroy", "--terragrunt-non-interactive", "-auto-approve")
}

func (o TGOptions) Output(module, name string) (string, error) {
	cmd := exec.Command("terragrunt", "output", "-raw", name)
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("terragrunt output %s/%s: %w", module, name, err)
	}
	return string(out), nil
}

func (o TGOptions) OutputJSON(module string) (map[string]interface{}, error) {
	cmd := exec.Command("terragrunt", "output", "-json")
	cmd.Dir = filepath.Join(o.WorkingDir, module)
	cmd.Env = o.env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terragrunt output -json %s: %w", module, err)
	}
	var raw map[string]struct {
		Value interface{} `json:"value"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse terragrunt output-json %s: %w", module, err)
	}
	m := map[string]interface{}{}
	for k, v := range raw {
		m[k] = v.Value
	}
	return m, nil
}
