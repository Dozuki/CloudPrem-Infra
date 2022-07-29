package src

import (
	tc "dozuki.com/tests/common"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"testing"
)

func Test_UsGovWest1_BI_Public(t *testing.T) {
	t.Parallel()

	var cfg = tc.ReadConfig()

	var testConfig = tc.InfraTest{
		Partition:   tc.GovCloudPartitionDir,
		Region:      endpoints.UsGovWest1RegionID,
		Profile:     tc.AWSGovDefaultProfile,
		Environment: cfg.BIPublic,
	}

	tc.GovInstanceOverrides(&testConfig)

	terraformFolder := test_structure.CopyTerraformFolderToTemp(t, tc.TfPath, "")

	physicalFolder, logicalFolder := tc.BootstrapFolders(testConfig, terraformFolder)

	terragruntPhysicalOptions, terragruntLogicalOptions := tc.BootstrapTerraform(t, physicalFolder, logicalFolder, testConfig.Environment)

	defer terraform.TgDestroyAll(t, terragruntPhysicalOptions)
	defer terraform.TgDestroyAll(t, terragruntLogicalOptions)

	terraform.TgApplyAll(t, terragruntPhysicalOptions)

	terraform.TgApplyAll(t, terragruntLogicalOptions)

	tc.PublicBIDMSAssertion(t, terragruntPhysicalOptions, &testConfig)
	tc.Assertions(t, terragruntPhysicalOptions, terragruntLogicalOptions, &testConfig)
}
