package common

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/rds"
	terratest_aws "github.com/gruntwork-io/terratest/modules/aws"
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
		TerraformDir:    physFolder,
		TerraformBinary: "terragrunt",
		Vars:            physConfig,
		EnvVars:         BuildEnvVars(env.Prefix),
		NoColor:         true,
	})
	logical = terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir:    logicFolder,
		TerraformBinary: "terragrunt",
		Vars:            logiConfig,
		EnvVars:         BuildEnvVars(env.Prefix),
		NoColor:         true,
	})

	return physical, logical
}

func RegionalOverrides(t *testing.T, config *InfraTest) {
	sess, err := session.NewSessionWithOptions(session.Options{
		Profile: config.Profile,
		Config: aws.Config{
			Region: aws.String(config.Region),
		},
	})
	if err != nil {
		fmt.Println("Error creating new AWS Session: ", err)
		return
	}

	ec2Client := ec2.New(sess)
	rdsClient := rds.New(sess)

	config.Environment.BastionInstanceType, err = terratest_aws.GetRecommendedInstanceTypeWithClientE(t, ec2Client, AWSBastionInstanceTypes)
	config.Environment.RDSInstanceType, err = terratest_aws.GetRecommendedRdsInstanceTypeWithClientE(t, rdsClient, "mysql", "8.0.28", AWSRDSInstanceTypes)

	if err != nil {
		fmt.Println("Error getting recommended instance type: ", err)
	}
	return

}
