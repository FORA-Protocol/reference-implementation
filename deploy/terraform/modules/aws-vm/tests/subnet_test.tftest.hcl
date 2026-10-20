# AWS alone is mocked: cloudinit renders the real document offline.
mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b"] }
  }
}
variables {
  name_prefix = "subnet-test"
  ami_id      = "ami-0123456789abcdef0"
  user_data   = "#cloud-config\n"
  ssh_operators = {
    operator = { public_key = "ssh-ed25519 AAAATEST operator@test", source_cidrs = ["203.0.113.7/32"] }
  }
}
run "legacy_subnet_defaults_are_preserved" {
  command = plan
  assert {
    condition     = aws_vpc.this.cidr_block == "10.80.0.0/24" && aws_subnet.public.cidr_block == aws_vpc.this.cidr_block
    error_message = "Existing consumers must retain the full-VPC public subnet."
  }
}
run "custom_vpc_without_subnet_override_preserves_legacy_behavior" {
  command = plan
  variables { vpc_cidr = "10.90.0.0/24" }
  assert {
    condition     = aws_subnet.public.cidr_block == "10.90.0.0/24"
    error_message = "An omitted public_subnet_cidr must follow vpc_cidr."
  }
}
run "production_subnet_leaves_room_for_private_database_subnets" {
  command = plan
  variables {
    vpc_cidr           = "10.80.0.0/22"
    public_subnet_cidr = "10.80.0.0/24"
  }
  assert {
    condition     = aws_vpc.this.cidr_block == "10.80.0.0/22" && aws_subnet.public.cidr_block == "10.80.0.0/24"
    error_message = "The production public subnet must leave separate address ranges for two private database subnets."
  }
}
