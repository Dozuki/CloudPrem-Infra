package src

import (
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"testing"
	"time"

	tc "dozuki.com/tests/common"
)

func Test_UsEast1_BI_Public(t *testing.T) {
	t.Parallel()

	var cfg = tc.ReadConfig()

	var testConfig = tc.InfraTest{
		Partition:   tc.StandardPartitionDir,
		Region:      endpoints.UsEast1RegionID,
		Profile:     tc.AWSTestDefaultProfile,
		Environment: cfg.BIPublic,
	}

	terraformFolder := test_structure.CopyTerraformFolderToTemp(t, tc.TfPath, "")

	physicalFolder, logicalFolder := tc.BootstrapFolders(testConfig, terraformFolder)

	terragruntPhysicalOptions, terragruntLogicalOptions := tc.BootstrapTerraform(t, physicalFolder, logicalFolder, testConfig.Environment)

	defer terraform.TgDestroyAll(t, terragruntPhysicalOptions)
	defer terraform.TgDestroyAll(t, terragruntLogicalOptions)

	terraform.TgApplyAll(t, terragruntPhysicalOptions)

	terraform.TgApplyAll(t, terragruntLogicalOptions)

	tc.BasicAssertion(t, terragruntLogicalOptions, 0*time.Second)
	tc.PublicBIDMSAssertion(t, terragruntPhysicalOptions, testConfig)
}
