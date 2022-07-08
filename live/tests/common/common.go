package common

import (
	"fmt"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"testing"
)

func BootstrapFolders(config InfraTest, terraformFolder string) (string, string) {
	physicalFolder := fmt.Sprintf("%s/live/%s/%s/%s/physical", terraformFolder, config.Partition, config.Region, config.Environment.Name)
	logicalFolder := fmt.Sprintf("%s/live/%s/%s/%s/logical", terraformFolder, config.Partition, config.Region, config.Environment.Name)

	return physicalFolder, logicalFolder
}

func BootstrapTerraform(t *testing.T, physFolder string, logicFolder string, env Environment) (*terraform.Options, *terraform.Options) {
	var physical *terraform.Options
	var logical *terraform.Options

	var physConfig = ConvertToTFConfig(env, "physical")
	var logiConfig = ConvertToTFConfig(env, "logical")

	physical = terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir:             physFolder,
		TerraformBinary:          "terragrunt",
		Vars:                     physConfig,
		EnvVars:                  BuildEnvVars(env.Prefix),
		RetryableTerraformErrors: RetryableErrors,
		NoColor:                  true,
	})
	logical = terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir:             logicFolder,
		TerraformBinary:          "terragrunt",
		Vars:                     logiConfig,
		EnvVars:                  BuildEnvVars(env.Prefix),
		RetryableTerraformErrors: RetryableErrors,
		NoColor:                  true,
	})

	return physical, logical
}
