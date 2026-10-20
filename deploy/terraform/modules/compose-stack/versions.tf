terraform {
  # 1.9 is the first release whose variable validations may refer to a local
  # (local.hostname_pattern); every stack that uses this module requires a
  # later one already.
  required_version = ">= 1.9"

  # Provider-neutral module: it renders templates and mints a database
  # password, nothing else. Cloud resources live in sibling modules (aws-vm),
  # so a future Hetzner/GCP layer reuses this module unchanged.
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}
