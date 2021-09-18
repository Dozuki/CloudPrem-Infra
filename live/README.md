# Live Testing Configuration
This folder is used for testing and CI for our terraform stack. To deploy production
stacks, please use our [deployment cloudformation](https://github.com/Dozuki/CloudPrem) or the [config repo](https://github.com/Dozuki/CloudPrem-Config) directly.

## Requirements
| Name | Version |
|------|---------|
| terraform | ~> 1.0.6 |
| terragrunt | ~> 0.31.8 |

## Folder Structure
This live environment is broken down into the following sections:
* account
  * region
    * environment
      * module

At each level a configuration file exists that sets variables for that level.

### Account
While we support any number of accounts _this_ folder structure consists
of a `standard` account for the normal AWS partition and a `gov` account for the govcloud
partition mostly as an example for multi-account use. Adding a new account is a matter of copying these folders and their content and
modifying the `account.hcl` file with the account number and the aws cli profile you will use
to access it.

An example file:
```hcl
locals {
  aws_account_id = "012345678"
  aws_profile    = "default"
}
```
This configuration will be used with the generated provider blocks to lock down access to the specified
account number and will use the specified profile when making aws cli calls. This is important if running from
your local workstation and will allow you to use different AWS credentials for different accounts without needing
to set environment variables. 

**Note:** _Be sure not to commit this file to the repo with actual AWS account numbers in it._

### Region
As an example the `us-east-1` region has a config file `region.hcl` inside the `standard/us-east-1` folder 
that looks like this:

```hcl
locals {
  aws_region = "us-east-1"
}
```
This `aws_region` variable should be edited to reflect the aws region if any new regions are added.

### Environment
Similarly to account and region, this level has an `env.hcl` file that specifies input variables for the
individual environments. To see a full list of the variables available refer to our [config repo](https://github.com/Dozuki/CloudPrem-Config).

The defaults included here are 4 separate environments:
* min
  * The minimum configuration required to get Dozuki up and running as fast as possible.
  * Takes ~30 minutes to boot
* bi 
  * The same minimum configuration with BI enabled.
  * Takes ~40 minutes to boot
* webhooks 
  * The same minimum configuration with webhooks enabled.
  * Takes ~60 minutes to boot (thanks kafka)
* full
  * Both BI and Webhooks enabled.
  * Takes ~60 minutes to boot

**Note:** Because this is meant for development, all HA and deletion protection options
are disabled for all 4 environments to save on cost and spin-up time.

An example environment configuration for minimum looks like:
```hcl
locals {
  environment = "min"
  enable_webhooks = false
  enable_bi = false
  rds_multi_az = false
  dozuki_license_parameter_name = "/dozuki/dev/license"
  protect_resources = false
}
```

### Module 
Inside the module there should be no additional configuration required to get it running.
The `terragrunt.hcl` is sufficient to pull in all necessary config, though if any dependencies,
inputs, or error retries are changed these files will need to be updated.

## Examples
The beauty of this live configuration is you can run terragrunt commands from almost any folder
and get as many or as few environments bootstrapped at once. If you want to spin up all environments in a specified 
region:

```bash
# CD to the region diretory
$ cd live/standard/us-east-1/
# Run all terraform modules in this directory recursively
$ terragrunt run-all apply
```
This would spin up 4 stacks: min, bi, webhooks, and full. This is probably not what
you want to do but it's possible.

For a more realistic example:
```bash
# CD into the environment directory
$ cd live/standard/us-east-1/min
# Run all terraform modules in this directory recursively
$ terragrunt run-all apply
```

This would give you 1 stack by spinning up the 4 modules inside only.

### Caveats
Because we are using a terragrunt multi-module stack with dependencies between them
we are unable to run traditional `plan` commands with the `run-all` system. The plan is not able to infer information
about resources that have not been deployed yet so it's kind of a all or nothing scenario if you
want to stick to the run-all system. However if you are developing a specific module, like lets say
the app module, you can definitely `cd` into the `app` directory and run single terragrunt commands
like `terragrunt plan` or `terragrunt apply` and you'll be able to get a plan for that individual
module. 

Keep in mind that the dependent modules must already be applied for this to work. If you want
to work on just the app module then the network, storage, and compute modules must alread be applied. 
Terragrunt will automatically query their outputs and include them for you as necessary. See the
`environment/module/terragrunt.hcl` file for dependency information.

### Don'ts
Don't modify the root `terragrunt.hcl` file in this directory. It uses black magic to build things and is
fully dynamic so don't touch it unless you know what your doing. The same goes for the `terragrunt.hcl` files
inside the modules themselves. This system is made so you only ever really have to modify the account, region, and 
env.hcl files to get things working.

## Region Support
The reason we have so many region directories in this folder structure is to illustrate all the 
regions we guarantee support in. If you don't see the region here, we're not sure our software works there.
It *should* but no guarantees. This counts for the govcloud regions also, you'll notice there's only one
region in the gov folder, that's because `us-gov-east-1` does not support DMS and some other services that are 
critial to our full stack so `us-gov-west-1` is the only gov cloud region we support.

### Regional Changes Required
Our stack is setup to be as region agnostic as possible but AWS in all their wisdom still has
some weird caveats and compatibility issues from region to region. Here are some concerns to be aware of
for different regions:

