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

#tfsec:ignore:aws-vpc-no-public-egress-sgr
module "bastion_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "4.7.0"

  name            = "${local.identifier}-bastion"
  use_name_prefix = false
  description     = "Security group for ${local.identifier} bastion instance. Only allows outbound traffic"
  vpc_id          = local.vpc_id

  egress_rules = ["all-tcp"]
}

module "bastion_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-assumable-role"
  version = "4.7.0"

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
resource "aws_ssm_document" "bastion_mysql_config" {
  name            = "BastionMySQLConfig-${local.identifier}"
  document_type   = "Command"
  document_format = "YAML"
  content         = file("${path.module}/static/bastion_mysql_config.yaml")

  tags = local.tags
}

resource "aws_ssm_document" "bastion_kubernetes_config" {
  name            = "BastionKubernetesConfig-${local.identifier}"
  document_type   = "Command"
  document_format = "YAML"
  content         = file("${path.module}/static/bastion_kubernetes_config.yaml")

  tags = local.tags
}

resource "aws_ssm_association" "bastion_mysql_config" {
  name             = aws_ssm_document.bastion_mysql_config.name
  document_version = aws_ssm_document.bastion_mysql_config.latest_version

  parameters = {
    RDSEndpoint : module.primary_database.db_instance_address
    RDSCredentialSecret : aws_secretsmanager_secret.primary_database_credentials.id
    Region : data.aws_region.current.name
  }

  targets {
    key    = "tag:Role"
    values = ["Bastion"]
  }

  dynamic "targets" {
    for_each = local.tags
    content {
      key    = "tag:${targets.key}"
      values = [targets.value]
    }
  }
}

resource "aws_ssm_association" "bastion_kubernetes_config" {
  name             = aws_ssm_document.bastion_kubernetes_config.name
  document_version = aws_ssm_document.bastion_kubernetes_config.latest_version

  parameters = {
    EKSClusterName : module.eks_cluster.cluster_id
    EKSClusterRole : module.cluster_access_role_assumable.iam_role_arn
    Region : data.aws_region.current.name
  }

  targets {
    key    = "tag:Role"
    values = ["Bastion"]
  }

  dynamic "targets" {
    for_each = local.tags
    content {
      key    = "tag:${targets.key}"
      values = [targets.value]
    }
  }
}


#tfsec:ignore:aws-autoscaling-enable-at-rest-encryption
module "bastion" {
  source  = "terraform-aws-modules/autoscaling/aws"
  version = "3.8.0"

  name = "${local.identifier}-bastion"

  iam_instance_profile = module.bastion_role.iam_instance_profile_arn

  image_id                     = data.aws_ami.amazon_linux_2.id
  instance_type                = "t3.micro"
  security_groups              = [module.bastion_sg.security_group_id]
  associate_public_ip_address  = false
  recreate_asg_when_lc_changes = true

  # Leave it blank because it is a required variable but we don't use userdata anymore
  user_data_base64 = ""

  # Auto scaling group
  vpc_zone_identifier = local.private_subnet_ids
  health_check_type   = "EC2"
  min_size            = 1
  max_size            = 1
  desired_capacity    = 1

  tags_as_map = merge(local.tags, {
    Role = "Bastion"
  })
}