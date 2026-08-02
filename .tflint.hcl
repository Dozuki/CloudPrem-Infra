# pre-commit-terraform runs `tflint --init` once from the repository root
# before linting individual module directories. Declare the pinned plugin here
# so that initialization installs the plugin used by the module-local configs.
plugin "aws" {
  enabled = true
  version = "0.48.0"
  source  = "github.com/terraform-linters/tflint-ruleset-aws"
}
