package src

import (
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"testing"

	testCommon "dozuki.com/tests/common"
)

func Test_UsGovWest1_BI_HA_VPN(t *testing.T) {
	t.Parallel()

	var terraformPath = "../../../"
	var partition = "gov"
	var region = "us-gov-west-1"
	var environment = "bi"
	var physConfig = testCommon.BIHAVPNPhysConfig
	var logiConfig = testCommon.BIHAVPNLogiConfig
	var prefixConfig = testCommon.HAVPNPrefixConfig

	terraformFolder := test_structure.CopyTerraformFolderToTemp(t, terraformPath, "")

	physicalFolder, logicalFolder := testCommon.BootstrapFolders(partition, region, environment, terraformFolder)

	terragruntPhysicalOptions, terragruntLogicalOptions := testCommon.BootstrapTerraform(t, physicalFolder, physConfig, logicalFolder, logiConfig, prefixConfig)

	defer terraform.TgDestroyAll(t, terragruntPhysicalOptions)
	defer terraform.TgDestroyAll(t, terragruntLogicalOptions)

	terraform.TgApplyAll(t, terragruntPhysicalOptions)
	terraform.TgApplyAll(t, terragruntLogicalOptions)

	testCommon.BasicAssertion(t, terragruntLogicalOptions)
}
