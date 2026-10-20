# Rendering tests for the production posture inputs: the TigerBeetle ledger
# without --development, and the bundled Vault in server mode behind a Vault
# Agent socket. Separate from render_unit_test.tftest.hcl only because that
# file is already at the repository's test-file length cap.
#
# Same properties as its siblings: `command = apply` is needed because the
# templates interpolate random_password, and the module's only resources ARE
# those in-memory random strings — nothing is created outside the test process.
#
# Run from the module directory: terraform init && terraform test

variables {
  exchange_hostname = "exchange.staging.example"
  broker_hostname   = "broker.staging.example"
  identity_hostname = "mcp.staging.example"
  zitadel_hostname  = "login.staging.example"

  default_tenant_domain = "exchange.staging.example"

  exchange_image = "registry.example.com/group/proj/exchange:test"
  broker_image   = "registry.example.com/group/proj/broker:test"
  identity_image = "registry.example.com/group/proj/identity:test"

  acme_email = "ops@example.com"

  # Dummy key material — shape only, never used cryptographically.
  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"
}

run "defaults_render_the_development_ledger_and_dev_vault" {
  command = apply

  # The staging and demo stacks pass none of the production inputs, and their
  # VMs are replaced whenever the rendered user data changes a byte. So the
  # defaults must render exactly what rendered before the inputs existed:
  # the development ledger, the dev-mode Vault, and none of the server-mode
  # services or volumes.

  assert {
    condition     = strcontains(output.compose_yaml, "/tigerbeetle format --cluster=0 --replica=0 --replica-count=1 --development /data/0_0.tigerbeetle")
    error_message = "By default the ledger must be formatted with --development, as staging always has been."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "/tigerbeetle start --addresses=0.0.0.0:3000 --cache-grid=256MiB --development /data/0_0.tigerbeetle")
    error_message = "By default the ledger must start with the 256MiB cache and --development, in this flag order — the staging bytes."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "command: [\"server\", \"-dev\"]") && strcontains(output.compose_yaml, "VAULT_TOKEN: \"")
    error_message = "By default Vault must run in dev mode with its root token in the Identity Service's environment — the staging trade-off."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "VAULT_ADDR: \"http://identity-vault:8200\"") && strcontains(output.compose_yaml, "IDENTITY_KV_MOUNT: \"secret\"")
    error_message = "By default the Identity Service must reach the dev-mode Vault directly over the compose network, on its default KV mount."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "vault-proxy") && !strcontains(output.compose_yaml, "network_mode: host") && !strcontains(output.compose_yaml, "vaultdata:")
    error_message = "By default no server-mode piece may render: no Vault Proxy, no host networking, no raft volume."
  }
}

run "production_ledger_drops_development_and_sizes_the_cache" {
  command = apply

  variables {
    tigerbeetle_development = false
    tigerbeetle_cache_grid  = "12GiB"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "/tigerbeetle format --cluster=0 --replica=0 --replica-count=1 /data/0_0.tigerbeetle")
    error_message = "A production ledger must be formatted without --development."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "/tigerbeetle start --addresses=0.0.0.0:3000 --cache-grid=12GiB /data/0_0.tigerbeetle")
    error_message = "A production ledger must start without --development and with the configured cache size."
  }

  assert {
    # Over the two command lines, not the whole document (the stanza comment
    # names the flag): --development on either command would turn the
    # Direct I/O requirement back off.
    condition     = length(regexall("/tigerbeetle (format|start) [^\n]*--development", output.compose_yaml)) == 0
    error_message = "--development must not appear on either ledger command once tigerbeetle_development is off."
  }
}

run "malformed_cache_grid_is_rejected" {
  command = plan

  variables {
    # A decimal unit: TigerBeetle refuses it at start, so the plan must.
    tigerbeetle_cache_grid = "256MB"
  }

  expect_failures = [
    var.tigerbeetle_cache_grid,
  ]
}

run "server_vault_renders_raft_tls_and_agent" {
  command = apply

  variables {
    vault_mode     = "server"
    vault_kv_mount = "fora-agents"
  }

  assert {
    # The quoted argument form: the ledger stanza's comment also contains the
    # letters "-dev" inside "--development".
    condition     = strcontains(output.compose_yaml, "command: [\"server\"]") && !strcontains(output.compose_yaml, "\"-dev\"")
    error_message = "Server mode must run `vault server` from its configuration directory, never -dev."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "- vaultdata:/vault/file") && strcontains(output.compose_yaml, "- vaultlogs:/vault/logs")
    error_message = "Server mode must give Vault a raft volume and an audit-log volume — the store is the only copy of every agent key."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "- /opt/fora/vault/server:/vault/config:ro") && strcontains(output.compose_yaml, "- /opt/fora/vault/tls:/vault/tls:ro") && strcontains(output.compose_yaml, "VAULT_CACERT: \"/vault/tls/vault.crt\"")
    error_message = "Server mode must mount the host-written server configuration and TLS material read-only, and trust that CA for its own CLI calls."
  }

  assert {
    # The doubled dollars are Compose's escape: Compose interpolates $VAR in
    # the file itself, so a single `$?` would reach the shell as an empty
    # string and the check would pass on any exit code.
    condition     = strcontains(output.compose_yaml, "test: [\"CMD-SHELL\", \"vault status >/dev/null 2>&1; rc=$$?; [ $$rc -eq 0 ] || [ $$rc -eq 2 ]\"]")
    error_message = "The server-mode healthcheck must accept exit code 2 (sealed) — Vault is sealed on first boot until the operator initialises it."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "vault proxy -config=/vault/proxy/proxy.hcl") && strcontains(output.compose_yaml, "- /opt/fora/vault/proxy:/vault/proxy:ro")
    error_message = "Server mode must run a Vault Proxy from /vault/proxy — not /vault/config, which the server loads as a whole directory."
  }

  assert {
    # The env-key form (newline, six spaces, the key): a comment in the same
    # stanza names VAULT_TOKEN to say it is absent.
    condition     = strcontains(output.compose_yaml, "VAULT_ADDR: \"unix:///vault-proxy/proxy.sock\"") && !strcontains(output.compose_yaml, "\n      VAULT_TOKEN:")
    error_message = "In server mode the Identity Service must reach Vault through the Proxy's socket and hold no token of its own."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_KV_MOUNT: \"fora-agents\"")
    error_message = "The configured KV mount must reach the Identity Service."
  }

  assert {
    # Both Vault containers share the host's network namespace to reach the
    # instance metadata service in one hop. Exactly two: nothing else in the
    # bundle may leave the compose network.
    condition     = length(regexall("network_mode: host", output.compose_yaml)) == 2
    error_message = "Exactly the Vault server and the Vault Proxy must run with host networking."
  }

  assert {
    # Server mode is the largest Vault arm the module can emit. The bundle
    # still renders as cloud-init, so it must still fit EC2's 16 KB user-data
    # limit (see user_data_size_unit_test.tftest.hcl for the arithmetic).
    condition     = length(base64gzip(output.user_data)) < 21848
    error_message = "Server-mode cloud-init exceeds EC2's 16 KB user-data limit — the instance would fail to launch"
  }
}

run "agent_socket_is_mounted_by_identity_only" {
  command = apply

  variables {
    vault_mode = "server"
  }

  # The Proxy proxies with its own Vault token, so whoever can reach its
  # listener holds the Identity Service's Vault permissions. The listener is a
  # Unix socket in a named volume, and these assertions pin who mounts it: the
  # Agent and the Identity Service, nobody else, and no TCP listener at all.

  assert {
    condition     = length(regexall("- vaultproxy:/vault-proxy", output.compose_yaml)) == 2
    error_message = "The socket volume must be mounted by exactly two services."
  }

  assert {
    # Each service stanza runs from its name to the next blank line.
    condition     = strcontains(regex("(?s)\n  vault-proxy:\n(.*?)\n\n", output.compose_yaml)[0], "- vaultproxy:/vault-proxy\n")
    error_message = "The Vault Proxy must mount the socket volume read-write — it creates the socket."
  }

  assert {
    condition     = strcontains(regex("(?s)\n  identity:\n(.*?)\n\n", output.compose_yaml)[0], "- vaultproxy:/vault-proxy:ro\n")
    error_message = "The Identity Service must mount the socket volume read-only."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "8100")
    error_message = "The Proxy must not expose a TCP listener: any container on the compose network could borrow the Identity Service's Vault permissions through it."
  }

  assert {
    condition     = strcontains(regex("(?s)\nvolumes:\n(.*)$", output.compose_yaml)[0], "\n  vaultproxy:\n")
    error_message = "The socket volume must be declared as a named volume, not a host path."
  }
}

run "server_vault_does_not_gate_identity_on_health" {
  command = apply

  variables {
    vault_mode = "server"
  }

  # On first boot Vault is sealed until the operator initialises it, and the
  # Agent cannot authenticate before that. A service_healthy dependency on
  # either would hold the Identity Service back forever.

  assert {
    condition     = strcontains(regex("(?s)\n  identity:\n(.*?)\n\n", output.compose_yaml)[0], "      identity-vault:\n        condition: service_started\n      vault-proxy:\n        condition: service_started\n")
    error_message = "In server mode the Identity Service must depend on the Vault pair with service_started, never service_healthy."
  }

  assert {
    condition     = !strcontains(regex("(?s)\n  identity:\n(.*?)\n\n", output.compose_yaml)[0], "identity-vault:\n        condition: service_healthy")
    error_message = "The dev-mode service_healthy dependency on Vault must not survive into server mode."
  }
}

run "invalid_vault_mode_is_rejected" {
  command = plan

  variables {
    vault_mode = "prod"
  }

  expect_failures = [
    var.vault_mode,
  ]
}

run "dev_vault_refuses_a_custom_kv_mount" {
  command = plan

  # A dev-mode Vault has exactly one KV engine. Pointing the Identity Service
  # elsewhere would boot cleanly and fail on the first key write.
  variables {
    vault_kv_mount = "fora-agents"
  }

  expect_failures = [
    random_password.postgres,
  ]
}

run "new_outputs_expose_the_side_documents" {
  command = apply

  # A caller that delivers the bundle some other way than this module's
  # cloud-init needs each side document as its own file.

  assert {
    condition     = strcontains(output.caddyfile, "exchange.staging.example {")
    error_message = "The Caddyfile output must carry the rendered site blocks."
  }

  assert {
    condition     = strcontains(output.init_sql, "CREATE DATABASE sor;")
    error_message = "The init SQL output must carry the CREATE DATABASE statements."
  }

  assert {
    condition     = strcontains(output.zitadel_init_steps, "UserName: \"zadmin\"") && strcontains(output.zitadel_init_steps, output.zitadel_admin_password)
    error_message = "The Zitadel init-steps output must be the rendered document, admin password included."
  }
}

# The compose file and the Caddyfile a stack ships in a size-capped bundle
# (a secret the host fetches at boot, for example): nothing but comment lines
# removed.
run "bundle_documents_without_comments_keep_every_setting" {
  command = apply

  assert {
    condition = (
      !can(regex("(?m)^[ \t]*#", output.compose_yaml_without_comments)) &&
      !can(regex("(?m)^[ \t]*#", output.caddyfile_without_comments)) &&
      yamldecode(output.compose_yaml_without_comments) == yamldecode(output.compose_yaml) &&
      output.caddyfile_without_comments == join("\n", [for line in split("\n", output.caddyfile) : line if !startswith(trimspace(line), "#")]) &&
      length(output.compose_yaml_without_comments) < length(output.compose_yaml)
    )
    error_message = "The comment-free compose file and Caddyfile must be the rendered ones minus their comment lines."
  }
}
