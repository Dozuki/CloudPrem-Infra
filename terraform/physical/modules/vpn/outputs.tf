output "aws_ec2_client_vpn_endpoint" {
  value = aws_ec2_client_vpn_endpoint.vpn-client
}
output "aws_vpn_security_group" {
  value = aws_security_group.vpn
}
output "aws_vpn_configuration_bucket" {
  value = aws_s3_bucket.vpn-config-files.bucket
}