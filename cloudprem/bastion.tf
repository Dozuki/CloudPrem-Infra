data "aws_ami" "amazon_linux_2" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name = "name"

    values = [
      "amzn2-ami-hvm-*-x86_64-gp2",
    ]
  }
}

module "bastion_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "3.17.0"

  name            = "${local.identifier}-bastion"
  use_name_prefix = false
  description     = "Security group for ${local.identifier} bastion instance. Only allows outbound traffic"
  vpc_id          = local.vpc_id

  egress_rules = ["all-tcp"]
}

module "bastion_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role"
  version = "3.6.0"

  create_role = true

  role_name               = "${local.identifier}-${data.aws_region.current.name}-bastion"
  role_requires_mfa       = false
  create_instance_profile = true

  custom_role_policy_arns = [
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonEC2RoleforSSM",
    "arn:${data.aws_partition.current.partition}:iam::aws:policy/AdministratorAccess"
  ]

  trusted_role_services = [
    "ec2.amazonaws.com",
  ]

  tags = local.tags
}

module "bastion" {
  source  = "terraform-aws-modules/autoscaling/aws"
  version = "3.8.0"

  depends_on = [module.vpc]

  name = "${local.identifier}-bastion"

  iam_instance_profile = module.bastion_role.this_iam_instance_profile_arn

  image_id                     = data.aws_ami.amazon_linux_2.id
  instance_type                = var.bastion_instance_type
  security_groups              = [module.bastion_sg.this_security_group_id]
  associate_public_ip_address  = false
  recreate_asg_when_lc_changes = true

  user_data_base64 = base64encode(templatefile("userdata.yml", {
    aws_region                     = data.aws_region.current.name
    eks_cluster_name               = module.eks_cluster.cluster_id
    eks_cluster_access_role        = module.cluster_access_role.this_iam_role_arn
    database_hostname              = module.primary_database.this_db_instance_address
    database_credentials_secret_id = aws_secretsmanager_secret.primary_database_credentials.arn
  }))

  # Auto scaling group
  vpc_zone_identifier = local.private_subnet_ids
  health_check_type   = "EC2"
  min_size            = 1
  max_size            = 1
  desired_capacity    = 1

  tags_as_map = local.tags
}