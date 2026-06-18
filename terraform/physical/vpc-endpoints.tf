# S3 Gateway VPC endpoint. Routes private-subnet (EKS node) S3 traffic directly
# over the AWS backbone instead of through the NAT Gateway: removes NAT
# data-processing charges for S3 and the shared-SNAT bottleneck under load.
# Gateway endpoints are free. Only added to VPCs we create (create_vpc); on
# customer-managed VPCs the customer adds their own S3 endpoint.
resource "aws_vpc_endpoint" "s3" {
  count             = local.create_vpc ? 1 : 0
  vpc_id            = module.vpc[0].vpc_id
  service_name      = "com.amazonaws.${data.aws_region.current.id}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = module.vpc[0].private_route_table_ids

  tags = merge(
    {
      "Name" = "${local.identifier}-s3"
    },
    local.tags
  )
}
