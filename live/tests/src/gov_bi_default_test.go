package src

import (
	tc "dozuki.com/tests/common"
	"github.com/gruntwork-io/terratest/modules/aws"
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"testing"
)

func Test_Gov_BI_Default(t *testing.T) {
	t.Parallel()

	var cfg = tc.ReadConfig()

	var testConfig = tc.InfraTest{
		Partition:   tc.GovCloudPartitionDir,
		Region:      aws.GetRandomRegion(t, tc.AWSGovAllowedRegions, nil),
		Profile:     tc.AWSGovDefaultProfile,
		Environment: cfg.BIDefault,
	}

	tc.RegionalOverrides(t, &testConfig)

	terraformFolder := test_structure.CopyTerraformFolderToTemp(t, tc.TfPath, "")

	physicalFolder, logicalFolder := tc.BootstrapFolders(testConfig, terraformFolder)

	terragruntPhysicalOptions, terragruntLogicalOptions := tc.BootstrapTerraform(t, physicalFolder, logicalFolder, testConfig.Environment)

	defer terraform.TgDestroyAll(t, terragruntPhysicalOptions)
	defer terraform.TgDestroyAll(t, terragruntLogicalOptions)

	terraform.TgApplyAll(t, terragruntPhysicalOptions)

	terraform.TgApplyAll(t, terragruntLogicalOptions)

	tc.Assertions(t, terragruntPhysicalOptions, terragruntLogicalOptions, &testConfig)
}
