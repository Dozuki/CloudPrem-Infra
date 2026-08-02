# reviewdog/action-tflint always invokes TFLint with an explicit config path.
# Keep the file local to this module so its AWS rules run without inheriting
# the logical layer's OpenTofu-specific rule exception.
plugin "aws" {
  enabled = true
  version = "0.48.0"
  source  = "github.com/terraform-linters/tflint-ruleset-aws"
}
