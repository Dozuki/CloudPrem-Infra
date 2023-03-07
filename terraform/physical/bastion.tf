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
  version = "4.17.1"

  name            = "${local.identifier}-bastion"
  use_name_prefix = false
  description     = "Security group for ${local.identifier} bastion instance. Only allows outbound traffic"
  vpc_id          = local.vpc_id

  egress_rules = ["all-tcp"]
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
  version = "6.7.1"

  name = "${local.identifier}-bastion"

  create_iam_instance_profile = true
  iam_role_name               = "${local.identifier}-${data.aws_region.current.name}-bastion"
  iam_role_path               = "/ec2/"
  iam_role_description        = "Bastion IAM Role"
  iam_role_tags = {
    CustomIamRole = "Yes"
  }
  iam_role_policies = {
    AmazonSSMManagedInstanceCore = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
    AdministratorAccess          = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AdministratorAccess"
  }

  image_id        = data.aws_ami.amazon_linux_2.id
  instance_type   = "t3.micro"
  security_groups = [module.bastion_sg.security_group_id]

  # Auto scaling group
  vpc_zone_identifier = local.private_subnet_ids
  health_check_type   = "EC2"
  min_size            = 1
  max_size            = 1
  desired_capacity    = 1

  tags = merge(local.tags, {
    Role = "Bastion"
  })
}
