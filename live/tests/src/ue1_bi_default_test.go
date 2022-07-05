package src

import (
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"testing"

	testCommon "dozuki.com/tests/common"
)

func Test_UsEast1_BI_Default(t *testing.T) {
	t.Parallel()

	var terraformPath = "../../../"
	var partition = "standard"
	var region = "us-east-1"
	var environment = "bi"
	var physConfig = testCommon.BIDefaultPhysConfig
	var logiConfig = testCommon.BIDefaultLogiConfig
	var prefixConfig = testCommon.DefaultPrefixConfig

	terraformFolder := test_structure.CopyTerraformFolderToTemp(t, terraformPath, "")

	physicalFolder, logicalFolder := testCommon.BootstrapFolders(partition, region, environment, terraformFolder)

	terragruntPhysicalOptions, terragruntLogicalOptions := testCommon.BootstrapTerraform(t, physicalFolder, physConfig, logicalFolder, logiConfig, prefixConfig)

	defer terraform.TgDestroyAll(t, terragruntPhysicalOptions)
	defer terraform.TgDestroyAll(t, terragruntLogicalOptions)

	terraform.TgApplyAll(t, terragruntPhysicalOptions)
	terraform.TgApplyAll(t, terragruntLogicalOptions)

	testCommon.BasicAssertion(t, terragruntLogicalOptions)
}
