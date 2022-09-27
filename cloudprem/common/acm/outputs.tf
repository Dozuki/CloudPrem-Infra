output "ssm_ca_key" {
  value = aws_ssm_parameter.ca_key
}
output "ssm_ca_cert" {
  value = aws_ssm_parameter.ca_cert
}
output "ssm_server_key" {
  value = aws_ssm_parameter.server_key
}
output "ssm_server_cert" {
  value = aws_ssm_parameter.server_cert
}
output "acm_server_arn" {
  value = aws_acm_certificate.server.arn
}
output "acm_ca_arn" {
  value = aws_acm_certificate.ca.arn
}