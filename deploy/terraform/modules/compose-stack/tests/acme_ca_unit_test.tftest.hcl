# Choosing Caddy's ACME CA (acme_ca) and its External Account Binding
# (acme_eab_key_id / acme_eab_mac_key), for example Google Trust Services while
# Let's Encrypt rate-limits the hostnames.
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

run "default_ca_is_caddys_own" {
  command = apply

  assert {
    condition     = !strcontains(output.caddyfile, "acme_ca") && !strcontains(output.caddyfile, "acme_eab")
    error_message = "Without acme_ca, the Caddyfile must leave Caddy's default issuers (Let's Encrypt, then ZeroSSL) in place."
  }

  assert {
    condition     = !can(yamldecode(output.compose_yaml).services.caddy.environment)
    error_message = "Without EAB credentials, Caddy must get no ACME environment."
  }
}

run "chosen_ca_with_eab" {
  command = apply

  variables {
    acme_ca          = "https://dv.acme-v02.api.pki.goog/directory"
    acme_eab_key_id  = "key-id_123"
    acme_eab_mac_key = "c2VjcmV0LW1hYy1rZXk_-"
  }

  assert {
    condition     = strcontains(output.caddyfile, "\tacme_ca https://dv.acme-v02.api.pki.goog/directory\n")
    error_message = "The Caddyfile's global block must name the chosen CA."
  }

  assert {
    condition     = strcontains(output.caddyfile, "key_id {$ACME_EAB_KEY_ID}") && strcontains(output.caddyfile, "mac_key {$ACME_EAB_MAC_KEY}")
    error_message = "The acme_eab block must read both EAB values from Caddy's environment."
  }

  # The Caddyfile is an unsensitive output and is written 0644 on the VM.
  assert {
    condition     = !strcontains(output.caddyfile, "c2VjcmV0LW1hYy1rZXk_-")
    error_message = "The MAC key must never be written into the Caddyfile."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.caddy.environment.ACME_EAB_KEY_ID == "key-id_123" &&
      yamldecode(output.compose_yaml).services.caddy.environment.ACME_EAB_MAC_KEY == "c2VjcmV0LW1hYy1rZXk_-"
    )
    error_message = "Caddy's environment must carry both EAB values exactly."
  }
}

run "chosen_ca_without_eab" {
  command = apply

  variables {
    acme_ca = "https://acme.example/directory"
  }

  assert {
    condition     = strcontains(output.caddyfile, "acme_ca https://acme.example/directory") && !strcontains(output.caddyfile, "acme_eab")
    error_message = "A CA without EAB credentials must get no acme_eab block."
  }
}

run "acme_ca_with_staging_is_rejected" {
  command = plan

  variables {
    acme_ca      = "https://dv.acme-v02.api.pki.goog/directory"
    acme_staging = true
  }

  expect_failures = [random_password.postgres]
}

run "eab_without_acme_ca_is_rejected" {
  command = plan

  variables {
    acme_eab_key_id  = "key-id"
    acme_eab_mac_key = "bWFj"
  }

  expect_failures = [random_password.postgres]
}

run "eab_key_id_without_mac_key_is_rejected" {
  command = plan

  variables {
    acme_ca         = "https://dv.acme-v02.api.pki.goog/directory"
    acme_eab_key_id = "key-id"
  }

  expect_failures = [random_password.postgres]
}

run "acme_ca_that_would_break_the_caddyfile_is_rejected" {
  command = plan

  variables {
    acme_ca = "https://ca.example/dir\n}\nimport evil"
  }

  expect_failures = [var.acme_ca]
}
