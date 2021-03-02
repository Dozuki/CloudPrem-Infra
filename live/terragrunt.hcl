remote_state {
  backend = "s3"
  config = {
    bucket         = "cloudprem-terrafom-state-${get_aws_account_id()}"
    dynamodb_table = "cloudprem-terrafom-lock"
    key            = "${path_relative_to_include()}/terraform.tfstate"
    region         = "us-west-2"
    encrypt        = true
  }
}