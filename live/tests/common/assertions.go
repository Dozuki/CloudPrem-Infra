package common

import (
	"crypto/tls"
	"encoding/json"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/databasemigrationservice"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/aws/aws-sdk-go/service/ssm"
	terratest_aws "github.com/gruntwork-io/terratest/modules/aws"
	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

const BIDBUser = "dozuki"
const BIDBPort = 3306
const BIDBName = "onprem_guide"

const OutputNLBKey = "dozuki_url"
const OutputDashKey = "dashboard_url"
const OutputBIKey = "bi_database_credential_secret"
const OutputDMSKey = "dms_task_arn"
const OutputBastionKey = "bastion_asg_name"
const SecretHostKey = "host"
const SecretPassKey = "password"

const DMSTaskRunningStatus = "running"

const AssertionRetries = 120
const AssertionRetryInterval = 30 * time.Second
const AssertionCoolDownTime = 600 * time.Second

const HTTPSuccessCode = 200

func BasicEndpointTest(t *testing.T, terragruntLogicalOptions *terraform.Options) {

	tlsConfig := tls.Config{InsecureSkipVerify: true}

	nlbUrl := terraform.Output(t, terragruntLogicalOptions, OutputNLBKey)
	dashUrl := terraform.Output(t, terragruntLogicalOptions, OutputDashKey)
	logger.Log(t, "Running Basic Endpoint Tests")

	http_helper.HttpGetWithRetryWithCustomValidation(t, dashUrl, &tlsConfig, AssertionRetries, AssertionRetryInterval, func(returnCode int, _ string) bool {
		if returnCode == HTTPSuccessCode {
			return true
		}
		return false
	})
	logger.Log(t, "Dashboard test successful")
	http_helper.HttpGetWithRetryWithCustomValidation(t, nlbUrl, &tlsConfig, AssertionRetries, AssertionRetryInterval, func(returnCode int, _ string) bool {
		if returnCode == HTTPSuccessCode {
			return true
		}
		return false
	})
	logger.Log(t, "App test successful")
}

func GetSecretValue(t *testing.T, id string, cfg *InfraTest) (string, error) {
	logger.Log(t, "Getting value of secret with ID %s", id)

	sess, err := session.NewSessionWithOptions(session.Options{
		Profile: cfg.Profile,
		Config: aws.Config{
			Region: aws.String(cfg.Region),
		},
	})

	client := secretsmanager.New(sess)

	secret, err := client.GetSecretValue(&secretsmanager.GetSecretValueInput{
		SecretId: aws.String(id),
	})
	if err != nil {
		return "", err
	}

	return aws.StringValue(secret.SecretString), nil
}

func PublicBIDMSAssertion(t *testing.T, terragruntPhysicalOptions *terraform.Options, cfg *InfraTest) {
	m := make(map[string]interface{})
	i := 0

	biSecret := terraform.Output(t, terragruntPhysicalOptions, OutputBIKey)
	dmsTaskArn := terraform.Output(t, terragruntPhysicalOptions, OutputDMSKey)

	secretValue, err := GetSecretValue(t, biSecret, cfg)
	err = json.Unmarshal([]byte(secretValue), &m)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Log(t, aerr.Error())
		} else {
			logger.Log(t, err.Error())
		}
		require.Fail(t, err.Error())
	}
	dbHost := m[SecretHostKey].(string)
	dbPass := m[SecretPassKey].(string)

	for PollDMSTask(t, cfg.Profile, cfg.Region, dmsTaskArn) != DMSTaskRunningStatus && i < 20 {
		logger.Log(t, "DMS Task not running yet, sleeping...")
		time.Sleep(AssertionRetryInterval)
		i++
	}
	logger.Log(t, "Testing for app database existence in replication instance.")
	schemaExists := terratest_aws.GetWhetherSchemaExistsInRdsMySqlInstance(t, dbHost, BIDBPort, BIDBUser, dbPass, BIDBName)

	assert.NotNil(t, dbHost)
	assert.True(t, schemaExists)

}

func PollDMSTask(t *testing.T, profile string, region string, dmsTaskArn string) string {
	sess, err := session.NewSessionWithOptions(session.Options{
		Profile: profile,
		Config: aws.Config{
			Region: aws.String(region),
		},
	})
	svc := databasemigrationservice.New(sess)
	input := &databasemigrationservice.DescribeReplicationTasksInput{
		Filters: []*databasemigrationservice.Filter{
			{
				Name: aws.String("replication-task-arn"),
				Values: []*string{
					aws.String(dmsTaskArn),
				},
			},
		},
		MaxRecords:      aws.Int64(20),
		WithoutSettings: &[]bool{true}[0],
	}

	result, err := svc.DescribeReplicationTasks(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case databasemigrationservice.ErrCodeResourceNotFoundFault:
				logger.Log(t, databasemigrationservice.ErrCodeResourceNotFoundFault, aerr.Error())
			default:
				logger.Log(t, aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			logger.Log(t, err.Error())
		}
		return ""
	}
	return *result.ReplicationTasks[0].Status
}
func VerifyBastion(t *testing.T, terragruntPhysicalOptions *terraform.Options, cfg *InfraTest) {

	sess, _ := session.NewSessionWithOptions(session.Options{
		Profile: cfg.Profile,
		Config: aws.Config{
			Region: aws.String(cfg.Region),
		},
	})
	svc := autoscaling.New(sess)
	ssmClient := ssm.New(sess)
	timeout := 1 * time.Minute

	bastionASGName := terraform.Output(t, terragruntPhysicalOptions, OutputBastionKey)

	var asgNames = autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []*string{&bastionASGName},
	}

	err := svc.WaitUntilGroupInService(&asgNames)
	asgs, err := svc.DescribeAutoScalingGroups(&asgNames)

	// There are a lot of these repeated error checking blocks and even though it triggers everything in me to refactor
	// and "fix" this, according to the golang experts it's perfectly acceptable to handle the errors at call-time like this
	// and to not wrap them or toss them off to a function call of some kind. It's cleaner and more semantic.
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Log(t, aerr.Error())
		} else {
			logger.Log(t, err.Error())
		}
		require.Fail(t, err.Error())
	}
	bastionId := asgs.AutoScalingGroups[0].Instances[0].InstanceId

	err = terratest_aws.WaitForSsmInstanceWithClientE(t, ssmClient, *bastionId, timeout)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Log(t, aerr.Error())
		} else {
			logger.Log(t, err.Error())
		}
		require.Fail(t, err.Error())
	}

	logger.Log(t, "Checking for existence of Kubectl")

	result, err := terratest_aws.CheckSSMCommandWithClientE(t, ssmClient, *bastionId, "sudo -iu ssm-user kubectl version --short=true", timeout)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Log(t, aerr.Error())
		} else {
			logger.Log(t, err.Error())
		}
		require.Fail(t, err.Error())
	}
	require.Contains(t, result.Stdout, "Client Version: v1.21")
	require.Equal(t, result.Stderr, "")
	require.Equal(t, int64(0), result.ExitCode)

	logger.Log(t, "Checking for existence of Helm")

	result, err = terratest_aws.CheckSSMCommandWithClientE(t, ssmClient, *bastionId, "helm version", timeout)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Log(t, aerr.Error())
		} else {
			logger.Log(t, err.Error())
		}
		require.Fail(t, err.Error())
	}
	require.Contains(t, result.Stdout, "version.BuildInfo{Version:\"v3.8.1\"")
	require.Equal(t, result.Stderr, "")
	require.Equal(t, int64(0), result.ExitCode)

	logger.Log(t, "Checking for existence and proper config of MySQL client")

	result, err = terratest_aws.CheckSSMCommandWithClientE(t, ssmClient, *bastionId, "sudo -iu ssm-user mysql -e \"show databases\"", timeout)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Log(t, aerr.Error())
		} else {
			logger.Log(t, err.Error())
		}
		require.Fail(t, err.Error())
	}
	require.Contains(t, result.Stdout, "information_schema")
	require.Equal(t, result.Stderr, "")
	require.Equal(t, int64(0), result.ExitCode)
}

func Assertions(t *testing.T, terragruntPhysicalOptions *terraform.Options, terragruntLogicalOptions *terraform.Options, cfg *InfraTest, coolDown ...time.Duration) {
	BasicEndpointTest(t, terragruntLogicalOptions)
	VerifyBastion(t, terragruntPhysicalOptions, cfg)

	var cd time.Duration

	if len(coolDown) == 0 {
		cd = AssertionCoolDownTime
	} else {
		cd = coolDown[0]
	}

	// Sleep to give the API time to cool down between creation and destroy
	logger.Log(t, "Sleeping to allow the infrastructure to settle")
	time.Sleep(cd)
}
