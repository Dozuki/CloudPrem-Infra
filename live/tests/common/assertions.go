package common

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/databasemigrationservice"
	terratest_aws "github.com/gruntwork-io/terratest/modules/aws"
	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/logger"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/assert"
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
const SecretHostKey = "host"
const SecretPassKey = "password"

const DMSTaskRunningStatus = "running"

const AssertionRetries = 120
const AssertionRetryInterval = 30 * time.Second
const AssertionCoolDownTime = 600 * time.Second

const HTTPSuccessCode = 200

func BasicAssertion(t *testing.T, terragruntLogicalOptions *terraform.Options, coolDown ...time.Duration) {

	tlsConfig := tls.Config{InsecureSkipVerify: true}

	nlbUrl := terraform.Output(t, terragruntLogicalOptions, OutputNLBKey)
	dashUrl := terraform.Output(t, terragruntLogicalOptions, OutputDashKey)

	var cd time.Duration

	http_helper.HttpGetWithRetryWithCustomValidation(t, dashUrl, &tlsConfig, AssertionRetries, AssertionRetryInterval, func(returnCode int, _ string) bool {
		if returnCode == HTTPSuccessCode {
			return true
		}
		return false
	})
	http_helper.HttpGetWithRetryWithCustomValidation(t, nlbUrl, &tlsConfig, AssertionRetries, AssertionRetryInterval, func(returnCode int, _ string) bool {
		if returnCode == HTTPSuccessCode {
			return true
		}
		return false
	})

	if len(coolDown) == 0 {
		cd = AssertionCoolDownTime
	} else {
		cd = coolDown[0]
	}

	// Sleep to give the API time to cool down between creation and destroy
	logger.Log(t, "Sleeping to allow the infrastructure to settle")
	time.Sleep(cd)

}

func PublicBIDMSAssertion(t *testing.T, terragruntPhysicalOptions *terraform.Options, cfg InfraTest) {
	m := make(map[string]interface{})
	i := 0

	biSecret := terraform.Output(t, terragruntPhysicalOptions, OutputBIKey)
	dmsTaskArn := terraform.Output(t, terragruntPhysicalOptions, OutputDMSKey)

	secretValue := terratest_aws.GetSecretValue(t, cfg.Region, biSecret)
	err := json.Unmarshal([]byte(secretValue), &m)
	if err != nil {
		logger.Log(t, fmt.Sprintf("Error: %s", err))
	}
	dbHost := m[SecretHostKey].(string)
	dbPass := m[SecretPassKey].(string)

	for PollDMSTask(cfg.Profile, cfg.Region, dmsTaskArn) != DMSTaskRunningStatus && i < 20 {
		logger.Log(t, "DMS Task not running yet, sleeping...")
		time.Sleep(AssertionRetryInterval)
		i++
	}

	schemaExists := terratest_aws.GetWhetherSchemaExistsInRdsMySqlInstance(t, dbHost, BIDBPort, BIDBUser, dbPass, BIDBName)

	assert.NotNil(t, dbHost)
	assert.True(t, schemaExists)

}

func PollDMSTask(profile string, region string, dmsTaskArn string) string {
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
				fmt.Println(databasemigrationservice.ErrCodeResourceNotFoundFault, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
		return ""
	}
	return string(*result.ReplicationTasks[0].Status)
}
