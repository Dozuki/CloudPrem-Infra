#tfsec:ignore:aws-vpc-no-public-egress-sgr
module "bastion_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "~> 5.0"

  create = local.aurora_migration_active

  name            = "${local.identifier}-bastion"
  use_name_prefix = false
  description     = "Security group for ${local.identifier} bastion instance. Only allows outbound traffic"
  vpc_id          = local.vpc_id

  egress_rules = ["all-tcp"]
}

resource "aws_ssm_document" "bastion_mysql_config" {
  count = local.aurora_migration_active ? 1 : 0

  name            = "BastionMySQLConfig-${local.identifier}"
  document_type   = "Command"
  document_format = "YAML"
  content         = file("${path.module}/static/bastion_mysql_config.yaml")

  tags = local.tags
}

resource "aws_ssm_association" "bastion_mysql_config" {
  count = local.aurora_migration_active ? 1 : 0

  name             = aws_ssm_document.bastion_mysql_config[0].name
  document_version = aws_ssm_document.bastion_mysql_config[0].latest_version

  parameters = {
    RDSEndpoint : local.db_host
    RDSCredentialSecret : aws_secretsmanager_secret.primary_database_credentials.id
    Region : data.aws_region.current.region
  }

  # Same selector the migration runner uses (Role + Name).
  targets {
    key    = "tag:Role"
    values = ["Bastion"]
  }

  targets {
    key    = "tag:Name"
    values = ["${local.identifier}-bastion"]
  }
}

#tfsec:ignore:aws-autoscaling-enable-at-rest-encryption
module "bastion" {
  source  = "terraform-aws-modules/autoscaling/aws"
  version = "~> 9.0"

  create = local.aurora_migration_active
  # The launch template ignores create, and the module indexes the instance profile on
  # create_iam_instance_profile alone, so both flags must follow create.
  create_launch_template      = local.aurora_migration_active
  create_iam_instance_profile = local.aurora_migration_active

  name = "${local.identifier}-bastion"

  iam_role_name        = "${local.identifier}-${data.aws_region.current.region}-bastion"
  iam_role_path        = "/ec2/"
  iam_role_description = "Bastion IAM Role"
  iam_role_tags = {
    CustomIamRole = "Yes"
  }
  iam_role_policies = {
    AmazonSSMManagedInstanceCore = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
    AdministratorAccess          = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AdministratorAccess"
  }

  # AMI resolves from AWS's public SSM parameter at instance launch, not at
  # plan time: the old latest-AMI data source made the plan (and drift
  # detection) go dirty every time Amazon published a new AL2023 AMI.
  image_id        = "resolve:ssm:/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64"
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
