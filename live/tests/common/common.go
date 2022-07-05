package common

import (
	"crypto/tls"
	"fmt"
	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"testing"
	"time"
)

func BootstrapFolders(partition string, region string, environment string, terraformFolder string) (string, string) {
	physicalFolder := fmt.Sprintf("%s/live/%s/%s/%s/physical", terraformFolder, partition, region, environment)
	logicalFolder := fmt.Sprintf("%s/live/%s/%s/%s/logical", terraformFolder, partition, region, environment)

	return physicalFolder, logicalFolder
}

func BasicAssertion(t *testing.T, terragruntLogicalOptions *terraform.Options) {

	tlsConfig := tls.Config{InsecureSkipVerify: true}

	nlbUrl := terraform.Output(t, terragruntLogicalOptions, "dozuki_url")
	dashUrl := terraform.Output(t, terragruntLogicalOptions, "dashboard_url")

	http_helper.HttpGetWithRetryWithCustomValidation(t, dashUrl, &tlsConfig, 120, 30*time.Second, func(returnCode int, _ string) bool {
		if returnCode == 200 {
			return true
		}
		return false
	})
	http_helper.HttpGetWithRetryWithCustomValidation(t, nlbUrl, &tlsConfig, 120, 30*time.Second, func(returnCode int, _ string) bool {
		if returnCode == 200 {
			return true
		}
		return false
	})

	// Sleep to give the API time to cool down between creation and destroy
	logger.Log(t, "Sleeping to allow the infrastructure to settle")
	time.Sleep(600 * time.Second)

}

func BootstrapTerraform(t *testing.T, physFolder string, physConfig map[string]interface{}, logicFolder string, logiConfig map[string]interface{}, prefix map[string]string) (*terraform.Options, *terraform.Options) {
	var physical *terraform.Options
	var logical *terraform.Options

	physical = terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir:             physFolder,
		TerraformBinary:          "terragrunt",
		Vars:                     physConfig,
		EnvVars:                  prefix,
		RetryableTerraformErrors: RetryableErrors,
		NoColor:                  true,
	})
	logical = terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir:             logicFolder,
		TerraformBinary:          "terragrunt",
		Vars:                     logiConfig,
		EnvVars:                  prefix,
		RetryableTerraformErrors: RetryableErrors,
		NoColor:                  true,
	})

	return physical, logical
}
