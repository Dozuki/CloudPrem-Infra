# Single source of truth for the active database's connection facts, selected by
# var.db_engine. Both module.primary_database (rds) and module.aurora (aurora)
# are count-gated, so exactly one is present. Consumed by the credentials secret
# (rds.tf), DMS endpoints (bi.tf), and outputs.tf.
locals {
  db_is_aurora = var.db_engine == "aurora"
  # Clone path: an Aurora copy-on-write clone of a pre-seeded source cluster. The clone
  # inherits the source's master password, so it comes from the var, not random_password.
  db_is_clone = local.db_is_aurora && var.aurora_clone_source_cluster_id != ""

  db_host = local.db_is_aurora ? module.aurora[0].cluster_endpoint : module.primary_database[0].db_instance_address
  db_port = local.db_is_aurora ? module.aurora[0].cluster_port : module.primary_database[0].db_instance_port

  db_username = "dozuki"
  db_password = local.db_is_aurora ? (local.db_is_clone ? var.aurora_clone_master_password : one(random_password.aurora[*].result)) : module.primary_database[0].db_instance_password

  db_identifier  = local.db_is_aurora ? module.aurora[0].cluster_id : module.primary_database[0].db_instance_id
  db_resource_id = local.db_is_aurora ? module.aurora[0].cluster_resource_id : module.primary_database[0].db_instance_resource_id

  db_reader_endpoint = local.db_is_aurora ? module.aurora[0].cluster_reader_endpoint : ""
}
