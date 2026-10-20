# The EC2 user-data budget, moved out of render_unit_test.tftest.hcl because
# that file is over the repository's test-file length cap.
#
# EC2 caps user data at 16384 bytes. aws-vm submits it gzipped, and Terraform
# can only measure the base64 of those bytes: base64 of n bytes is
# 4*ceil(n/3) characters, so 16384 bytes is 21848 characters. Comparing with
# `<` keeps the check on the safe side of the rounding.
#
# The module renders the user data twice. user_data keeps every comment, and a
# stack with few optional blocks ships it as it is: the staging-shaped run
# measures that. With every optional block on, the commented document is over
# the limit, so a stack that turns many of them on ships
# user_data_without_comments instead: the fully-loaded run measures that one.
# If the comment-free document ever trips too, the fix is not to trim the
# templates: it is to stop inlining the bundle and fetch it at boot, which is a
# design change worth making deliberately.
#
# Same properties as its siblings: `command = apply` is needed because the
# templates interpolate random_password, and the module's only resources ARE
# those in-memory random values — nothing is created outside the test process.
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

  # Dummy key material — shape only, never used cryptographically. The RSA key
  # is the one large key, so its placeholder has the size of a real 2048-bit
  # key file (26 body lines, 1624 characters) and a body that compresses like
  # one: base64 of SHA-512 digests, computed here rather than written out.
  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  rsa_private_pem         = "-----BEGIN PRIVATE KEY-----\n${join("\n", [for i in range(26) : substr(base64sha512("rsa-placeholder-${i}"), 0, i < 25 ? 64 : 24)])}\n-----END PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"

  # The two pre-signed public key directories the staging and demo stacks
  # serve, real in size and shape (static_wba_unit_test.tftest.hcl explains
  # the fixtures), and the contributor key the importer signs with.
  static_wba_directories = {
    "catalog-contributor.staging.example" = jsonencode({
      body            = "{\"keys\": [{\"kty\": \"OKP\", \"crv\": \"Ed25519\", \"use\": \"sig\", \"alg\": \"EdDSA\", \"x\": \"sT4EIcOWpRK29gl0-Pk10IQMnmiC9TQyrj8-logOs2A\", \"not_before\": \"2026-08-04T00:00:00Z\", \"not_after\": \"2026-11-02T00:00:00Z\"}]}\n"
      content_digest  = "sha-256=:nKleGLbn+ROZmNuZfeKnXOJ316s2bkbzrbbO3CBniuM=:"
      signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176433;expires=2106536433;keyid=\"I3TRE_EbrOi6e5Fk__UCxHbTdMauzEXoAbU7S98_Glc\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
      signature       = "sig1=:9uT8PzqF+iIHYIkhtfuAG4lV31aNV+6fTIshpRStdqrnxPy3OCiaa5vhRV9XGhXVRqG2ScNpbmq/oN8GSndhDQ==:"
    })
    "smoke-agent.staging.example" = jsonencode({
      body            = "{\n  \"keys\": [\n    {\n      \"kty\": \"OKP\",\n      \"crv\": \"Ed25519\",\n      \"use\": \"sig\",\n      \"alg\": \"EdDSA\",\n      \"x\": \"eoIPHZgfb9zTFj10xLzW8eQSJOBID9KrMWCes8_ZhkQ\",\n      \"not_before\": \"2026-08-04T00:00:00Z\",\n      \"not_after\": \"2026-11-02T00:00:00Z\"\n    }\n  ],\n  \"note\": \"a backtick ` and a brace } never reach the Caddyfile\"\n}\n"
      content_digest  = "sha-256=:J9ZBbr6fJykOjccwOnL3jHUmRHNuHk8ugz4ge+Uk8XY=:"
      signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176433;expires=2106536433;keyid=\"bAoGcE1aV0bBwG1jI5baUf-6LyTVH2-3_r9z89OUZ4E\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
      signature       = "sig1=:fqMMiSuCjKjPGuvPxlX7RPFvN2F1v6Lpue9njDDH0AWL2aX8TO3IngeJCq3JdUHGuGPW7JkrNxW/Xcui0oDKAg==:"
    })
  }
  importer_key_json = "{\"kid\": \"catalog-contributor.staging.example\", \"private_key\": \"fixture-private\", \"public_key\": \"fixture-public\"}\n"

  origin_hostname   = "origin.staging.example"
  publisher_image   = "registry.example.com/group/proj/publisher:test"
  registry_server   = "registry.example.com"
  registry_username = "gitlab+deploy-token-123456"
  registry_password = "glpat-XXXXXXXXXXXXXXXXXXXX"
}

run "fully_loaded_user_data_fits_the_ec2_limit" {
  command = apply

  # Every optional block a deployment can turn on at once, on top of the
  # staging-shaped ones above, because the limit has to hold for the LARGEST
  # cloud-init the module can emit, not the fixture's minimum. Where a choice
  # has two arms, the larger one: self-hosted PostgreSQL, whose two database
  # services render more than the external arm's CA file and mounts, and the
  # server-mode Vault. The documents (terms, the catalog feed) are small: their
  # size is the operator's, not the module's. A new optional block belongs in
  # this list.
  variables {
    exa_api_key = "00000000-0000-0000-0000-000000000000"

    smtp_host      = "smtp.staging.example:587"
    smtp_user      = "smtp-user-placeholder"
    smtp_password  = "BEXAMPLEsmtppasswordEXAMPLEsmtppasswordEXAM"
    smtp_from      = "noreply@demo.staging.example"
    smtp_from_name = "FORA Demo"

    onboarding_hostname           = "pub-onboarding.staging.example"
    onboarding_image              = "registry.example.com/group/proj/onboarding:test"
    onboarding_allowed_origins    = ["https://site.example"]
    onboarding_contact_recipients = ["sales@demo.staging.example"]
    onboarding_contact_from       = "noreply@demo.staging.example"

    exchange_registration_schema = "{\"$schema\": \"https://json-schema.org/draft/2020-12/schema\", \"type\": \"object\", \"required\": [\"legal_entity\"]}"
    exchange_terms_uri           = "https://exchange.staging.example/terms/revision-1.txt"
    exchange_terms_digest        = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    exchange_terms_documents     = { "revision-1.txt" = "Terms\n" }
    exchange_terms_currency      = "EUR"
    exchange_terms_unit          = "fetches"

    query_discovery = {
      catalog_feed          = "{\"domain\":\"music.example\",\"path\":\"/a.mp3\",\"terms\":[]}\n"
      model                 = "example-model-v1"
      aws_region            = "us-east-1"
      aws_access_key_id     = "access-key-placeholder"
      aws_secret_access_key = "test-secret-access-key"
    }

    acme_ca              = "https://acme.staging.example/directory"
    acme_eab_key_id      = "key-id_123"
    acme_eab_mac_key     = "c2VjcmV0LW1hYy1rZXk_-"
    caddy_data_volume_id = "vol-0123456789abcdef0"

    vault_mode     = "server"
    vault_kv_mount = "fora-agents"

    console_hostname         = "console.staging.example"
    console_image            = "registry.example.com/group/proj/console@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    console_signing_key_pem  = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIGZha2UgY29uc29sZSBzaWduaW5nIGtleSBmaXh0dXJl\n-----END PRIVATE KEY-----\n"
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"
    console_default_rate     = "0.25"

    # The publishers' Zitadel instance, in its larger arm: a recovery window,
    # which adds the memberships. The System API key has the size of a real
    # 3072-bit public key file (9 body lines, 564 characters) and a body that
    # compresses like one.
    console_login_hostname                  = "login.console.staging.example"
    zitadel_system_api_public_key           = "-----BEGIN PUBLIC KEY-----\n${join("\n", [for i in range(9) : substr(base64sha512("system-api-placeholder-${i}"), 0, i < 8 ? 64 : 52)])}\n-----END PUBLIC KEY-----\n"
    zitadel_system_api_recovery_instance_id = "393782486556999684"
  }

  assert {
    condition     = length(base64gzip(output.user_data_without_comments)) < 21848
    error_message = "The comment-free cloud-init with every optional block on exceeds EC2's 16 KB user-data limit — the instance would fail to launch."
  }

  # Documents why such a stack ships the comment-free document.
  assert {
    condition     = length(base64gzip(output.user_data)) >= 21848
    error_message = "With every optional block on, the commented cloud-init is expected to exceed the limit, which is why a stack that turns them on ships user_data_without_comments. If it fits again, that reason has changed."
  }

  # Documents why aws-vm gzips rather than submitting the text: uncompressed,
  # even the comment-free document is past the limit.
  assert {
    condition     = length(output.user_data_without_comments) > 16384
    error_message = "The comment-free cloud-init is expected to exceed the raw limit; if it no longer does, the compression in aws-vm is load-bearing for a reason that has changed."
  }
}

run "staging_shaped_user_data_fits_the_ec2_limit" {
  command = apply

  # The optional blocks the staging stack turns on, all set above: the demo
  # publisher origin, the registry login, the RSA key, the two key directories
  # and the importer. Staging ships user_data with its comments.

  assert {
    condition     = length(base64gzip(output.user_data)) < 21848
    error_message = "The staging-shaped cloud-init, comments included, exceeds EC2's 16 KB user-data limit — the instance would fail to launch."
  }
}
