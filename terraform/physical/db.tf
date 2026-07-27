# Single source of truth for the active database's connection facts, selected by
# var.db_engine. Both module.primary_database (rds) and module.aurora (aurora)
# are count-gated, so exactly one is present. Consumed by the credentials secret
# (rds.tf), DMS endpoints (bi.tf), and outputs.tf.
#
# aurora_migration_state widens this: during an RDS->Aurora DMS migration
# (aurora-migration.tf) BOTH engines exist side by side. The Aurora cluster comes
# up at "provision" (guarded, DMS-only reachable), and these locals - and with
# them every consumer: the credentials secret, the BI DMS source endpoint, the
# bastion mysql association, outputs - flip to Aurora at "cutover". db_engine
# stays "rds" through the whole migration so the live RDS is never in a destroy
# path; the final retirement (env flips db_engine="aurora" + state="off") is a
# separate, deliberate, OPA-labeled apply.
locals {
  db_is_aurora = var.db_engine == "aurora"

  # Aurora exists: the steady-state aurora engine OR any active migration phase.
  aurora_migration_active = var.aurora_migration_state != "off"
  aurora_present          = local.db_is_aurora || local.aurora_migration_active

  # Connection facts point at Aurora: steady-state aurora OR the migration has
  # reached cutover (cleanup keeps them on Aurora while the DMS rig is removed).
  db_uses_aurora = local.db_is_aurora || contains(["cutover", "cleanup"], var.aurora_migration_state)

  db_host = local.db_uses_aurora ? module.aurora[0].cluster_endpoint : module.primary_database[0].db_instance_address
  db_port = local.db_uses_aurora ? module.aurora[0].cluster_port : module.primary_database[0].db_instance_port

  db_username = "dozuki"
  db_password = local.db_uses_aurora ? random_password.aurora[0].result : module.primary_database[0].db_instance_password

  db_identifier  = local.db_uses_aurora ? module.aurora[0].cluster_id : module.primary_database[0].db_instance_id
  db_resource_id = local.db_uses_aurora ? module.aurora[0].cluster_resource_id : module.primary_database[0].db_instance_resource_id

  db_reader_endpoint = local.db_uses_aurora ? module.aurora[0].cluster_reader_endpoint : ""
}
