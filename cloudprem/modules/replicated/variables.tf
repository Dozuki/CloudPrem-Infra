variable "dozuki_license_parameter_name" {
  description = "The SSM parameter name that stores the Dozuki license file provided to you."
  type        = string
}
variable "nlb_hostname" {
  description = "The hostname of the deployed network loadbalancer"
  type = string
}
variable "release_sequence" {
  description = "Pin a replicated release sequence on firstboot."
  default = ""
}