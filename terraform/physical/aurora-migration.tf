# RDS -> Aurora Serverless v2 migration rig (DMS), driven by var.aurora_migration_state.
# The state machine (each transition one env.hcl change + one gated Spacelift apply):
#
#   off       - nothing here exists; normal single-engine behavior.
#   provision - Aurora (aurora.tf, count widened by local.aurora_present) comes up EMPTY
#               alongside the live RDS, reachable ONLY through the guard SG (DMS + bastion;
#               the app must never boot against an empty 8.4 - the fresh-install sharp
#               edge). This file's DMS rig is created but the task is NOT started; the
#               migration runner (schema pre-load, task orchestration, validation, fence)
#               drives everything DB-side from the env bastion via SSM.
#   cutover   - db.tf's locals flip every consumer (credentials secret, outputs, BI DMS
#               source, bastion mysql config) to Aurora and aurora.tf swaps the cluster
#               onto the primary DB SG. Applied only at the runner's go-live gate.
#   cleanup   - this rig (DMS instance/endpoints/task, guard SG, subnet group) is
#               destroyed. Aurora stays primary; the RDS survives read_only as the
#               fail-forward net until the env's final db_engine="aurora" flip retires it
#               (a deliberate apply that trips the db-replace-guard OPA label on purpose).
#
# The task settings/mappings are the values proven by the 2026-07-26 gca rehearsal
# (docs: 3m-aurora-dms-migration-design): DO_NOTHING prep (schema is pre-loaded natively),
# BatchApplyEnabled=false + target FK-checks-off Initstmt during load (MySQL does not
# binlog cascaded child rows - the runner removes the Initstmt, while stopped, after
# deferred FKs are active), auto-stop after cached changes, limited LOB @128KB, row
# validation, fail-fast error policy.

locals {
  # The DMS rig lives through provision + cutover and is removed at cleanup.
  aurora_migration_dms = contains(["provision", "cutover"], var.aurora_migration_state)
}

# --- network guard -----------------------------------------------------------

# The migration DMS instance's own SG (egress-only; DMS dials out to both DBs).
module "aurora_migration_dms_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "~> 5.0"
  count   = local.aurora_migration_dms ? 1 : 0

  name            = "${local.identifier}-aurora-migration-dms"
  use_name_prefix = false
  description     = "Aurora migration DMS instance"
  vpc_id          = local.vpc_id

  egress_rules = ["all-tcp"]

  tags = local.tags
}

# Guard SG: the ONLY SG on the Aurora cluster while state="provision". Admits the
# migration DMS instance and the bastion - nothing else, most importantly not the
# EKS cluster SG, so no app pod can reach the target before cutover.
module "aurora_migration_guard_sg" {
  source  = "terraform-aws-modules/security-group/aws"
  version = "~> 5.0"
  count   = local.aurora_migration_dms ? 1 : 0

  name            = "${local.identifier}-aurora-migration-guard"
  use_name_prefix = false
  description     = "Aurora migration target guard (DMS + bastion only)"
  vpc_id          = local.vpc_id

  ingress_with_source_security_group_id = [
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.aurora_migration_dms_sg[0].security_group_id
    },
    {
      rule                     = "mysql-tcp"
      source_security_group_id = module.bastion_sg.security_group_id
    },
  ]

  tags = local.tags
}

# The migration DMS instance must also read the live RDS (full load + CDC). The
# primary DB SG's ingress is authored in rds.tf; this standalone rule appends to
# it without touching the module (append-only - no recreate risk on that SG).
resource "aws_security_group_rule" "aurora_migration_dms_to_rds" {
  count = local.aurora_migration_dms ? 1 : 0

  type                     = "ingress"
  from_port                = 3306
  to_port                  = 3306
  protocol                 = "tcp"
  security_group_id        = module.primary_database_sg.security_group_id
  source_security_group_id = module.aurora_migration_dms_sg[0].security_group_id
  description              = "Aurora migration DMS source reads"
}

# --- IAM: dms-access-for-tasks -----------------------------------------------

# Account-wide role the DMS premigration assessment requires (absent from this
# account today - the rehearsal had to skip the assessment API without it). Same
# keep-out-of-state pattern as the dms-vpc/cloudwatch roles in bi.tf: created
# only if missing, never owned by any one stack.
data "aws_iam_roles" "dms-access-for-tasks" {
  name_regex = "dms-access-for-tasks"
}
resource "null_resource" "create_dms_access_for_tasks_role" {
  count = local.aurora_migration_dms ? length(data.aws_iam_roles.dms-access-for-tasks.names) > 0 ? 0 : 1 : 0

  provisioner "local-exec" {
    command = <<-EOT
      aws iam create-role \
        --role-name dms-access-for-tasks \
        --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"dms.${data.aws_partition.current.dns_suffix}"},"Action":"sts:AssumeRole"}]}'
      aws iam attach-role-policy \
        --role-name dms-access-for-tasks \
        --policy-arn arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonDMSRedshiftS3Role || true
    EOT
  }
}

# --- DMS rig -----------------------------------------------------------------

resource "aws_dms_replication_subnet_group" "aurora_migration" {
  count = local.aurora_migration_dms ? 1 : 0

  # AWS requires the account-wide DMS roles to exist before the first DMS
  # resource is created; the create-if-absent null_resources must win the race
  # on a fresh account.
  depends_on = [
    null_resource.create_dms_vpc_role,
    null_resource.create_dms_cloudwatch_role,
    null_resource.create_dms_access_for_tasks_role,
  ]

  replication_subnet_group_id          = "${local.identifier}-aurora-migration"
  replication_subnet_group_description = "${local.identifier} aurora migration subnet group"

  subnet_ids = local.private_subnet_ids

  tags = local.tags
}

resource "aws_dms_replication_instance" "aurora_migration" {
  count = local.aurora_migration_dms ? 1 : 0

  replication_instance_id    = "${local.identifier}-aurora-migration"
  replication_instance_class = var.aurora_migration_dms_instance_type
  allocated_storage          = var.aurora_migration_dms_storage
  engine_version             = var.aurora_migration_dms_engine_version
  auto_minor_version_upgrade = false # pinned for run-to-run reproducibility across the fleet

  publicly_accessible         = false
  replication_subnet_group_id = aws_dms_replication_subnet_group.aurora_migration[0].id

  vpc_security_group_ids = [module.aurora_migration_dms_sg[0].security_group_id]

  tags = local.tags
}

resource "aws_dms_certificate" "aurora_migration" {
  count = local.aurora_migration_dms ? 1 : 0

  certificate_id  = "${local.identifier}-aurora-migration"
  certificate_pem = file(local.ca_cert_pem_file)

  tags = local.tags
}

# Source: ALWAYS the RDS instance, referenced directly - never local.db_host,
# which flips to Aurora at cutover (the source must keep reading RDS through the
# fence). 48h binlog retention gives the CDC checkpoint generous slack.
resource "aws_dms_endpoint" "aurora_migration_source" {
  count = local.aurora_migration_dms ? 1 : 0

  endpoint_id                 = "${local.identifier}-aurora-migration-source"
  certificate_arn             = aws_dms_certificate.aurora_migration[0].certificate_arn
  ssl_mode                    = "verify-full"
  endpoint_type               = "source"
  engine_name                 = "mysql"
  extra_connection_attributes = "afterConnectScript=call mysql.rds_set_configuration('binlog retention hours', 48);"
  port                        = 3306

  username    = local.db_username
  password    = module.primary_database[0].db_instance_password
  server_name = module.primary_database[0].db_instance_address

  tags = local.tags
}

# Target: the Aurora CLUSTER writer endpoint (never the instance endpoint - the
# cluster endpoint follows a writer failover; instance endpoints do not). The
# FK-checks-off Initstmt is load-phase only: the migration runner REMOVES it
# (endpoint modify, while the task is stopped post-full-load) so ongoing CDC
# applies with FKs active and target-side cascades execute. ignore_changes keeps
# Terraform from re-adding it behind the runner's back.
resource "aws_dms_endpoint" "aurora_migration_target" {
  count = local.aurora_migration_dms ? 1 : 0

  endpoint_id                 = "${local.identifier}-aurora-migration-target"
  certificate_arn             = aws_dms_certificate.aurora_migration[0].certificate_arn
  ssl_mode                    = "verify-full"
  endpoint_type               = "target"
  engine_name                 = "aurora"
  extra_connection_attributes = "Initstmt=SET FOREIGN_KEY_CHECKS=0;"
  port                        = 3306

  username    = local.db_username
  password    = random_password.aurora[0].result
  server_name = module.aurora[0].cluster_endpoint

  tags = local.tags

  lifecycle {
    ignore_changes = [extra_connection_attributes]
  }
}

# Created, never auto-started: the runner starts it only after the native schema
# pre-load passes its information_schema diff gate.
resource "aws_dms_replication_task" "aurora_migration" {
  count = local.aurora_migration_dms ? 1 : 0

  replication_task_id       = "${local.identifier}-aurora-migration"
  migration_type            = "full-load-and-cdc"
  replication_instance_arn  = aws_dms_replication_instance.aurora_migration[0].replication_instance_arn
  table_mappings            = file("static/aurora_migration_mapping.json")
  replication_task_settings = file("static/aurora_migration_settings.json")

  source_endpoint_arn = aws_dms_endpoint.aurora_migration_source[0].endpoint_arn
  target_endpoint_arn = aws_dms_endpoint.aurora_migration_target[0].endpoint_arn

  tags = local.tags

  lifecycle {
    # The runner owns the task lifecycle (start/stop/resume around its gates);
    # settings drift (e.g. validation counters) must not dirty the plan.
    ignore_changes = [replication_task_settings]
  }
}

# --- migration credentials -----------------------------------------------------

# Aurora's master password (random_password.aurora) exists only in Terraform
# state until cutover updates the primary credentials secret. The migration
# runner needs target credentials from provision onward, so they get their own
# secret for the migration's lifetime.
resource "aws_secretsmanager_secret" "aurora_migration_credentials" {
  count = local.aurora_migration_dms ? 1 : 0

  name_prefix             = "${local.identifier}-aurora-migration"
  description             = "Aurora migration target credentials (runner use only; removed at cleanup)"
  recovery_window_in_days = 0

  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "aurora_migration_credentials" {
  count = local.aurora_migration_dms ? 1 : 0

  secret_id = aws_secretsmanager_secret.aurora_migration_credentials[0].id
  secret_string = jsonencode({
    host     = module.aurora[0].cluster_endpoint
    port     = 3306
    username = local.db_username
    password = random_password.aurora[0].result
  })
}

# --- transition safety ---------------------------------------------------------

# Phase memory: replaced on every state change, so the PLAN DIFF always shows
# "aurora_migration_phase must be replaced: <old> -> <new>" - the reviewer of the
# gated apply sees the transition explicitly. Terraform cannot hard-enforce a
# transition graph at plan time (the previous value is unknowable exactly when it
# changes), so enforcement is layered instead:
#   - the db-replace-guard OPA policy already fails any plan that destroys the
#     Aurora cluster or the RDS instance without the allow-db-replace label
#     (covers the catastrophic cutover/cleanup -> off reversal);
#   - every transition is a gated, human-confirmed Spacelift apply showing this
#     resource's replacement diff;
#   - the migration runner's phases each assert the AWS-side facts they need
#     (task exists/stopped, marker present, secret host) before acting.
resource "terraform_data" "aurora_migration_phase" {
  input            = var.aurora_migration_state
  triggers_replace = var.aurora_migration_state

  lifecycle {
    # The BI replica secret mixes hosts/credentials across engines if a
    # non-DMS BI path is active during a migration (its host selector keys on
    # db_engine, its credentials on local.db_*). The 3M fleet is BI-via-DMS;
    # anything else must not migrate until that seam is reworked.
    precondition {
      condition     = var.aurora_migration_state == "off" || !var.enable_bi || local.dms_enabled
      error_message = "aurora_migration_state requires BI disabled or BI-via-DMS (bi_dms_enabled/bi_public_access): the RDS-read-replica BI path would mix engines at cutover."
    }
  }
}
