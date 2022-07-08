package common

var DefaultPrefixConfig = map[string]string{
	"TG_STATE_PREFIX": "test/default/",
}
var HAPrefixConfig = map[string]string{
	"TG_STATE_PREFIX": "test/ha/",
}
var VPNPrefixConfig = map[string]string{
	"TG_STATE_PREFIX": "test/vpn/",
}
var HAVPNPrefixConfig = map[string]string{
	"TG_STATE_PREFIX": "test/havpn/",
}

var MinDefaultPhysConfig = map[string]interface{}{
	"identifier":                   "default",
	"environment":                  "min",
	"enable_webhooks":              "false",
	"enable_bi":                    "false",
	"rds_multi_az":                 "false",
	"highly_available_nat_gateway": "false",
	"protect_resources":            "false",
}
var MinDefaultLogiConfig = map[string]interface{}{
	"identifier":                    "default",
	"environment":                   "min",
	"enable_webhooks":               "false",
	"enable_bi":                     "false",
	"dozuki_license_parameter_name": "/dozuki/workstation/beta/license",
}
var MinHaPhysConfig = map[string]interface{}{
	"identifier":                   "ha",
	"environment":                  "min",
	"enable_webhooks":              "false",
	"enable_bi":                    "false",
	"rds_multi_az":                 "true",
	"highly_available_nat_gateway": "true",
	"protect_resources":            "false",
}
var MinHALogiConfig = map[string]interface{}{
	"identifier":                    "ha",
	"environment":                   "min",
	"enable_webhooks":               "false",
	"enable_bi":                     "false",
	"dozuki_license_parameter_name": "/dozuki/workstation/beta/license",
}
var BIDefaultPhysConfig = map[string]interface{}{
	"identifier":                   "default",
	"environment":                  "bi",
	"enable_webhooks":              "false",
	"enable_bi":                    "true",
	"rds_multi_az":                 "false",
	"highly_available_nat_gateway": "false",
	"protect_resources":            "false",
}
var BIDefaultLogiConfig = map[string]interface{}{
	"identifier":                    "default",
	"environment":                   "bi",
	"enable_webhooks":               "false",
	"enable_bi":                     "true",
	"dozuki_license_parameter_name": "/dozuki/workstation/beta/license",
}
var BIHAPhysConfig = map[string]interface{}{
	"identifier":                   "ha",
	"environment":                  "bi",
	"enable_webhooks":              "false",
	"enable_bi":                    "true",
	"rds_multi_az":                 "true",
	"highly_available_nat_gateway": "true",
	"protect_resources":            "false",
}
var BIHALogiConfig = map[string]interface{}{
	"identifier":                    "ha",
	"environment":                   "bi",
	"enable_webhooks":               "false",
	"enable_bi":                     "true",
	"dozuki_license_parameter_name": "/dozuki/workstation/beta/license",
}
var BIVPNPhysConfig = map[string]interface{}{
	"identifier":                   "vpn",
	"environment":                  "bi",
	"enable_webhooks":              "false",
	"enable_bi":                    "true",
	"rds_multi_az":                 "false",
	"highly_available_nat_gateway": "false",
	"protect_resources":            "false",
	"bi_vpn_access":                "true",
	"bi_access_cidrs":              "[\"0.0.0.0/0\"]",
}
var BIVPNLogiConfig = map[string]interface{}{
	"identifier":                    "vpn",
	"environment":                   "bi",
	"enable_webhooks":               "false",
	"enable_bi":                     "true",
	"dozuki_license_parameter_name": "/dozuki/workstation/beta/license",
}
var BIHAVPNPhysConfig = map[string]interface{}{
	"identifier":                   "havpn",
	"environment":                  "bi",
	"enable_webhooks":              "false",
	"enable_bi":                    "true",
	"rds_multi_az":                 "true",
	"highly_available_nat_gateway": "true",
	"protect_resources":            "false",
	"bi_vpn_access":                "true",
	"bi_access_cidrs":              "[\"0.0.0.0/0\"]",
}
var BIHAVPNLogiConfig = map[string]interface{}{
	"identifier":                    "havpn",
	"environment":                   "bi",
	"enable_webhooks":               "false",
	"enable_bi":                     "true",
	"dozuki_license_parameter_name": "/dozuki/workstation/beta/license",
}
var WebhooksPhysConfig = map[string]interface{}{
	"identifier":                   "default",
	"environment":                  "hooks",
	"enable_webhooks":              "true",
	"enable_bi":                    "false",
	"rds_multi_az":                 "false",
	"highly_available_nat_gateway": "false",
	"protect_resources":            "false",
}
var WebhooksLogiConfig = map[string]interface{}{
	"identifier":                    "default",
	"environment":                   "hooks",
	"enable_webhooks":               "true",
	"enable_bi":                     "false",
	"dozuki_license_parameter_name": "/dozuki/workstation/alpha/license",
}
var FullPhysConfig = map[string]interface{}{
	"identifier":                   "default",
	"environment":                  "full",
	"enable_webhooks":              "true",
	"enable_bi":                    "true",
	"rds_multi_az":                 "true",
	"highly_available_nat_gateway": "false",
	"protect_resources":            "false",
	"bi_vpn_access":                "true",
	"bi_access_cidrs":              "[\"0.0.0.0/0\"]",
}
var FullLogiConfig = map[string]interface{}{
	"identifier":                    "default",
	"environment":                   "full",
	"enable_webhooks":               "true",
	"enable_bi":                     "true",
	"dozuki_license_parameter_name": "/dozuki/workstation/alpha/license",
}
var RetryableErrors = map[string]string{
	"(?s).*DependencyViolation": "Retrying due to dependency violation",
	"(?s).*sites-config-update": "Retrying due to k8 job failure",
	"(?s).*replicated":          "Retrying due to a helm deploy failure",
}
