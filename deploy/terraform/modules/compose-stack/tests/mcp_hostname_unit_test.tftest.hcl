# The optional MCP hostname. Three properties, one run each: the default
# render is the render from before the option existed; the split render pins
# the token audience to the MCP name and gives Caddy a site for it; naming the
# Identity hostname explicitly is the default spelled out. The first property
# is what keeps the demo and staging VMs in place — their user data embeds
# this render, and the VM module replaces the instance when it changes.
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

  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  rsa_private_pem         = "-----BEGIN RSA PRIVATE KEY-----\ndGVzdA==\n-----END RSA PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"
}

run "default_keeps_mcp_on_the_identity_hostname" {
  command = apply

  assert {
    condition     = !strcontains(output.compose_yaml, "IDENTITY_TOKEN_AUDIENCE")
    error_message = "Without mcp_hostname the audience must stay at the service's default, the issuer — rendering it would change the user data of every existing deployment"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "      # The service's own public origin. It is the OAuth issuer, the audience\n      # of the bearers it mints, and the base of the /callback URL registered\n      # with Zitadel — changing it invalidates that registration.\n      IDENTITY_AUTH_ISSUER: \"https://mcp.staging.example\"\n")
    error_message = "The default issuer block, comment included, must render exactly as before the option existed: staging embeds the commented render in its user data"
  }

  assert {
    condition     = !strcontains(output.caddyfile, "# The MCP endpoint under its own name")
    error_message = "Without mcp_hostname the Caddyfile must not gain a site"
  }

  assert {
    condition     = output.mcp_url == "https://mcp.staging.example"
    error_message = "mcp_url defaults to the Identity hostname"
  }
}

run "split_hostname_pins_the_audience_and_adds_a_site" {
  command = apply

  variables {
    mcp_hostname = "agents.staging.example"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "      IDENTITY_AUTH_ISSUER: \"https://mcp.staging.example\"\n      IDENTITY_TOKEN_AUDIENCE: \"https://agents.staging.example\"\n")
    error_message = "The issuer stays on the Identity hostname (the registered /callback URL derives from it) and the audience follows the MCP hostname"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_BASE_DOMAIN: \"mcp.staging.example\"")
    error_message = "Agent directories stay under the Identity hostname; the MCP name is not a directory zone"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "      # /callback URL registered with Zitadel — changing it invalidates that\n      # registration. The MCP endpoint is served under its own name, so the\n")
    error_message = "The split render carries the comment that explains why the audience differs from the issuer"
  }

  assert {
    condition     = strcontains(output.caddyfile, "\nagents.staging.example {\n\treverse_proxy identity:8083\n}\n")
    error_message = "Caddy must serve the MCP hostname from the Identity container"
  }

  assert {
    condition     = strcontains(output.caddyfile, "\n*.mcp.staging.example {\n")
    error_message = "The on-demand wildcard stays on the Identity hostname"
  }

  assert {
    condition     = output.mcp_url == "https://agents.staging.example"
    error_message = "mcp_url names the MCP hostname when set"
  }
}

run "mcp_hostname_equal_to_identity_hostname_is_the_default" {
  command = apply

  variables {
    mcp_hostname = "mcp.staging.example"
  }

  assert {
    condition     = output.compose_yaml == run.default_keeps_mcp_on_the_identity_hostname.compose_yaml
    error_message = "Naming the Identity hostname explicitly must render the compose file byte-for-byte as the default"
  }

  assert {
    condition     = output.caddyfile == run.default_keeps_mcp_on_the_identity_hostname.caddyfile
    error_message = "Naming the Identity hostname explicitly must render the Caddyfile byte-for-byte as the default"
  }

  assert {
    condition     = output.user_data == run.default_keeps_mcp_on_the_identity_hostname.user_data && output.user_data_without_comments == run.default_keeps_mcp_on_the_identity_hostname.user_data_without_comments
    error_message = "Both user-data renders must stay byte-for-byte the default: the VM module replaces the instance when either changes"
  }
}

run "mcp_hostname_with_a_scheme_is_rejected" {
  command = plan

  variables {
    mcp_hostname = "https://agents.staging.example"
  }

  expect_failures = [var.mcp_hostname]
}
