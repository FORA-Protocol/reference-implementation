# Caddy's /data on a persistent EBS volume (caddy_data_volume_id). The volume
# keeps the certificates and the ACME account across a VM replacement, so a
# redeploy does not request every certificate again from Let's Encrypt.
#
# Run from the module directory: terraform init && terraform test

variables {
  exchange_hostname     = "exchange.demo.example"
  broker_hostname       = "broker.demo.example"
  identity_hostname     = "mcp.demo.example"
  zitadel_hostname      = "login.demo.example"
  default_tenant_domain = "exchange.demo.example"

  exchange_image = "registry.example.com/group/proj/exchange:test"
  broker_image   = "registry.example.com/group/proj/broker:test"
  identity_image = "registry.example.com/group/proj/identity:test"

  acme_email = "ops@example.com"

  # Dummy key material, shape only (see render_unit_test.tftest.hcl).
  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"
}

run "caddy_data_stays_in_a_named_volume_by_default" {
  command = apply

  assert {
    condition     = contains(yamldecode(output.compose_yaml).services.caddy.volumes, "caddydata:/data") && contains(keys(yamldecode(output.compose_yaml).volumes), "caddydata")
    error_message = "Without a volume id, Caddy's /data must stay in the named volume caddydata."
  }

  assert {
    condition     = !strcontains(output.user_data, "caddy-data") && !strcontains(output.user_data, "/var/lib/fora-caddy")
    error_message = "Without a volume id, cloud-init must not wait for or mount any volume."
  }
}

run "caddy_data_is_bound_to_the_mounted_volume" {
  command = apply

  variables {
    caddy_data_volume_id = "vol-0123456789abcdef0"
  }

  assert {
    condition     = contains(yamldecode(output.compose_yaml).services.caddy.volumes, "/var/lib/fora-caddy:/data")
    error_message = "Caddy's complete /data must be bind-mounted read-write from the volume's mount point."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "caddydata")
    error_message = "The named volume caddydata must be gone, or a later edit could put /data back on the root disk."
  }

  # Only Caddy's data moves: every other service keeps its named volume on the
  # root disk, so a redeploy still resets the demo.
  assert {
    condition     = alltrue([for name, service in yamldecode(output.compose_yaml).services : name == "caddy" || alltrue([for mount in try(service.volumes, []) : !startswith(mount, "/var/lib/fora-caddy")])])
    error_message = "No service other than Caddy may mount the Caddy data volume."
  }

  assert {
    condition = alltrue([for needle in [
      "/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol0123456789abcdef0",
      "mkfs.ext4 -q -L caddy-data",
      "LABEL=caddy-data /var/lib/fora-caddy ext4 defaults,nofail 0 2",
      "mountpoint -q /var/lib/fora-caddy || {",
    ] : strcontains(output.user_data, needle)])
    error_message = "cloud-init must wait for the volume by its id, format only a blank device, and mount it at /var/lib/fora-caddy."
  }

  # The mount must happen before Docker is installed: Docker starting Caddy on
  # an unmounted directory would request new certificates on the root disk.
  assert {
    condition     = strcontains(yamldecode(output.user_data).runcmd[0], "mountpoint -q /var/lib/fora-caddy") && strcontains(yamldecode(output.user_data).runcmd[1], "get.docker.com")
    error_message = "The volume mount must be the first runcmd step, before Docker is installed."
  }

  # The stacks deliver the comment-stripped variant; the script has no line
  # that starts with #, so stripping must leave it whole.
  assert {
    condition     = yamldecode(output.user_data_without_comments).runcmd[0] == yamldecode(output.user_data).runcmd[0]
    error_message = "Removing comments must not change the mount script."
  }
}

run "caddy_data_volume_id_must_be_an_ebs_id" {
  command = plan

  variables {
    caddy_data_volume_id = "/dev/sdf"
  }

  expect_failures = [var.caddy_data_volume_id]
}
