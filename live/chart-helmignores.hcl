# Vendored-chart .helmignore generation for logical stacks (live harness).
# Pairs with logical.hcl. Split out of the former common.hcl.
locals {
  helmignore = <<EOF
.DS_Store
# Common VCS dirs
.git/
.gitignore
.bzr/
.bzrignore
.hg/
.hgignore
.svn/
# Common backup files
*.swp
*.bak
*.tmp
*.orig
*~
# Various IDEs
.project
.idea/
*.tmproj
.vscode/
.terragrunt-source-manifest
.terragrunt-source-manifest/
  EOF
}

generate "metrics_server_helmignore" {
  path      = "charts/metrics-server/.helmignore"
  if_exists = "overwrite_terragrunt"
  contents  = local.helmignore
}
