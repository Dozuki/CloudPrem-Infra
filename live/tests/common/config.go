package common

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"reflect"
	"strconv"
	"sync"
)

const TestConfigFile = "../test-config.yaml"
const TfPath = "../../../"
const StandardPartitionDir = "standard"
const GovCloudPartitionDir = "gov"

const AWSTestDefaultProfile = "default"
const AWSGovDefaultProfile = "gov"

const TgStatePrefixVar = "TG_STATE_PREFIX"

const AWSGovEKSInstanceTypes = "[\"m5.large\", \"m5a.large\", \"m5d.large\"]"

type Environment struct {
	Identifier                 string `yaml:"identifier" physical:"true" logical:"true"`
	AWSProfile                 string `yaml:"aws_profile" physical:"true" logical:"true"`
	Name                       string `yaml:"environment" physical:"true" logical:"true"`
	Prefix                     string `yaml:"prefix"`
	EnableWebhooks             bool   `yaml:"enable_webhooks" physical:"true" logical:"true"`
	HANatGateway               bool   `yaml:"highly_available_nat_gateway" physical:"true"`
	ProtectResources           bool   `yaml:"protect_resources" physical:"true"`
	VPCID                      string `yaml:"vpc_id" physical:"true" logical:"true"`
	VPCCIDR                    string `yaml:"vpc_cidr" physical:"true"`
	PublicAccess               bool   `yaml:"app_public_access" physical:"true"`
	ElasticacheInstanceType    string `yaml:"elasticache_instance_type" physical:"true"`
	ElasticacheClusterSize     string `yaml:"elasticache_cluster_size" physical:"true"`
	EKSKMSKeyId                string `yaml:"eks_kms_key_id" physical:"true"`
	EKSInstanceTypes           string `yaml:"eks_instance_types" physical:"true"`
	EKSVolumeSize              string `yaml:"eks_volume_size" physical:"true"`
	EKSClusterMinSize          string `yaml:"eks_min_size" physical:"true"`
	EKSClusterMaxSize          string `yaml:"eks_max_size" physical:"true"`
	EKSClusterDesiredCapacity  string `yaml:"eks_desired_capacity" physical:"true"`
	AppAccessCIDRs             string `yaml:"app_access_cidrs" physical:"true"`
	ReplicatedAccessCIDRs      string `yaml:"replicated_ui_access_cidrs" physical:"true"`
	LicenseParameter           string `yaml:"dozuki_license_parameter_name" logical:"true"`
	BootstrapAppSequenceNumber int    `yaml:"replicated_app_sequence_number" logical:"true"`
	GoogleTranslateAPIToken    string `yaml:"google_translate_api_token" logical:"true"`
	S3KMSKeyID                 string `yaml:"s3_kms_key_id" physical:"true" logical:"true"`
	S3CreateBuckets            bool   `yaml:"create_s3_buckets" physical:"true"`
	S3ObjectBucket             string `yaml:"s3_objects_bucket" physical:"true"`
	S3ImageBucket              string `yaml:"s3_images_bucket" physical:"true"`
	S3DocumentBucket           string `yaml:"s3_documents_bucket" physical:"true"`
	S3PDFBucket                string `yaml:"s3_pdfs_bucket" physical:"true"`
	S3LogBucket                string `yaml:"s3_logging_bucket" physical:"true"`
	BIEnabled                  bool   `yaml:"enable_bi" physical:"true" logical:"true"`
	BIPublicAccess             bool   `yaml:"bi_public_access" physical:"true"`
	BIVPNAccess                bool   `yaml:"bi_vpn_access" physical:"true"`
	BIVPNUserList              string `yaml:"bi_vpn_user_list" physical:"true"`
	BIAccessCIDRs              string `yaml:"bi_access_cidrs" physical:"true"`
	RDSKMSKeyID                string `yaml:"rds_kms_key_id" physical:"true"`
	RDSSnapshotIdentifier      string `yaml:"rds_snapshot_identifier" physical:"true"`
	RDSInstanceType            string `yaml:"rds_instance_type" physical:"true"`
	RDSMultiAZ                 bool   `yaml:"rds_multi_az" physical:"true"`
	RDSAllocatedStorage        string `yaml:"rds_allocated_storage" physical:"true"`
	RDSMaxAllocatedStorage     string `yaml:"rds_max_allocated_storage" physical:"true"`
	RDSBackupRetention         string `yaml:"rds_backup_retention_period" physical:"true"`
}

type TestConfig struct {
	MinDefault Environment `yaml:"min_default"`
	MinHA      Environment `yaml:"min_ha"`
	BIDefault  Environment `yaml:"bi_default"`
	BIHA       Environment `yaml:"bi_ha"`
	BIVPN      Environment `yaml:"bi_vpn"`
	BIHAVPN    Environment `yaml:"bi_ha_vpn"`
	BIPublic   Environment `yaml:"bi_public"`
	Webhooks   Environment `yaml:"webhooks"`
	Full       Environment `yaml:"full"`
}

type InfraTest struct {
	Partition   string
	Region      string
	Profile     string
	Environment Environment
}

func ReadConfig() TestConfig {
	var cfg TestConfig

	f, err := os.Open(TestConfigFile)
	if err != nil {
		println(err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			println(err)
		}
	}(f)

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		println(err)
	}
	return cfg
}

func getField(v *Environment, field string) interface{} {
	r := reflect.ValueOf(v)
	f := reflect.Indirect(r).FieldByName(field)
	return f
}

func ConvertToTFConfig(cfg Environment, module string) map[string]interface{} {
	t := reflect.TypeOf(cfg)
	tfConfig := make(map[string]interface{})
	tfConfigMutex := sync.RWMutex{}
	for i := 0; i < t.NumField(); i++ {
		// We are using the yaml struct tag to double as the terraform variable key. We loop through the configuration
		// struct checking each value for the "yaml" tag and pulling the value for that key in the struct instance and
		// assigning it to a string interface map that can be fed into the terraform test suite.
		// i.e. given cfg.EnableWebhooks = true then our map becomes enable_webhooks = true because the struct is defined
		// thusly: EnableWebhooks bool `yaml:"enable_webhooks" physical:"true" logical:"true"`. The physical and logical
		// tags allow us to figure out which variables go with which module.
		var tfKey, tfExist = t.Field(i).Tag.Lookup("yaml")
		var _, moduleExist = t.Field(i).Tag.Lookup(module)
		if tfExist && moduleExist {
			var rawValue = getField(&cfg, t.Field(i).Name)
			var convertedValue string
			switch v := rawValue.(type) {
			case bool:
				convertedValue = strconv.FormatBool(v)
			default:
				var strValue = fmt.Sprintf("%v", v)
				if strValue != "" {
					convertedValue = strValue
				}
			}
			if convertedValue != "" {
				tfConfigMutex.Lock()
				tfConfig[tfKey] = convertedValue
				tfConfigMutex.Unlock()
			}
		}
	}

	return tfConfig
}

func BuildEnvVars(prefix string) map[string]string {
	return map[string]string{
		TgStatePrefixVar: prefix,
	}
}

// GovInstanceOverrides Override the default EKS Instance types to exclude the m5ad.large that is not available in every
// us-gov-west-1 AZ
// @todo This needs to be handled inside the terraform
func GovInstanceOverrides(tc *InfraTest) {
	tc.Environment.EKSInstanceTypes = AWSGovEKSInstanceTypes
}
