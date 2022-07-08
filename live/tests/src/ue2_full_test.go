package src

import (
	tc "dozuki.com/tests/common"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"testing"
)

func Test_UsEast2_Full(t *testing.T) {
	t.Parallel()

	var cfg = tc.ReadConfig()

	var testConfig = tc.InfraTest{
		Partition:   tc.StandardPartitionDir,
		Region:      endpoints.UsEast2RegionID,
		Profile:     tc.AWSTestDefaultProfile,
		Environment: cfg.Full,
	}

	terraformFolder := test_structure.CopyTerraformFolderToTemp(t, tc.TfPath, "")

	physicalFolder, logicalFolder := tc.BootstrapFolders(testConfig, terraformFolder)

	terragruntPhysicalOptions, terragruntLogicalOptions := tc.BootstrapTerraform(t, physicalFolder, logicalFolder, testConfig.Environment)

	defer terraform.TgDestroyAll(t, terragruntPhysicalOptions)
	defer terraform.TgDestroyAll(t, terragruntLogicalOptions)

	terraform.TgApplyAll(t, terragruntPhysicalOptions)

	terraform.TgApplyAll(t, terragruntLogicalOptions)

	tc.BasicAssertion(t, terragruntLogicalOptions)
}
