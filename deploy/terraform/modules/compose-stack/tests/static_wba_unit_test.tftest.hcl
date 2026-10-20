# The static Web Bot Auth key directories Caddy serves for identities whose
# private key never reaches the VM. The Web Bot Auth profile has a verifier
# take a key only from a directory response signed by that key for the host it
# fetched from, so these directories arrive pre-signed: per hostname, the JSON
# object {body, content_digest, signature_input, signature}. Caddy serves body
# byte for byte as application/http-message-signatures-directory+json with the
# three signature headers.
#
# The fixtures are real: each was signed by src/exchange/cmd/fora-sign-directory (the
# protocol SDK's SignDirectoryResponse) for the hostname it is served under, by
# a throwaway key whose private half was discarded. The plan cannot verify a
# signature, so a real one is not required for these runs to pass, but the
# digest and the keyids are checked against the body and must be the ones a
# signer produces. The smoke fixture's body carries an extra member holding a
# backtick and a brace on purpose: the body must reach the VM through
# cloud-init's base64 channel and never through Caddyfile quoting.
#
# `command = apply` is required where the templates interpolate
# random_password; see render_unit_test.tftest.hcl.

variables {
  exchange_hostname     = "exchange.staging.example"
  broker_hostname       = "broker.staging.example"
  identity_hostname     = "mcp.staging.example"
  zitadel_hostname      = "login.staging.example"
  default_tenant_domain = "exchange.staging.example"

  exchange_image = "registry.example.com/group/proj/exchange:test"
  broker_image   = "registry.example.com/group/proj/broker:test"
  identity_image = "registry.example.com/group/proj/identity:test"

  acme_email = "ops@example.com"

  # Dummy key material — shape only, never used cryptographically.
  ed25519_private_pem     = "-----BEGIN PRIVATE KEY-----\ndGVzdA==\n-----END PRIVATE KEY-----\n"
  broker_relay_key_json   = "{\"kid\":\"broker.test.v1\"}"
  broker_identity_key_pem = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"

  # Signed for smoke-agent.staging.example by the one key it lists.
  smoke_directory = jsonencode({
    body            = "{\n  \"keys\": [\n    {\n      \"kty\": \"OKP\",\n      \"crv\": \"Ed25519\",\n      \"use\": \"sig\",\n      \"alg\": \"EdDSA\",\n      \"x\": \"eoIPHZgfb9zTFj10xLzW8eQSJOBID9KrMWCes8_ZhkQ\",\n      \"not_before\": \"2026-08-04T00:00:00Z\",\n      \"not_after\": \"2026-11-02T00:00:00Z\"\n    }\n  ],\n  \"note\": \"a backtick ` and a brace } never reach the Caddyfile\"\n}\n"
    content_digest  = "sha-256=:J9ZBbr6fJykOjccwOnL3jHUmRHNuHk8ugz4ge+Uk8XY=:"
    signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176433;expires=2106536433;keyid=\"bAoGcE1aV0bBwG1jI5baUf-6LyTVH2-3_r9z89OUZ4E\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
    signature       = "sig1=:fqMMiSuCjKjPGuvPxlX7RPFvN2F1v6Lpue9njDDH0AWL2aX8TO3IngeJCq3JdUHGuGPW7JkrNxW/Xcui0oDKAg==:"
  })

  # Signed for catalog-contributor.staging.example by the one key it lists.
  contributor_directory = jsonencode({
    body            = "{\"keys\": [{\"kty\": \"OKP\", \"crv\": \"Ed25519\", \"use\": \"sig\", \"alg\": \"EdDSA\", \"x\": \"sT4EIcOWpRK29gl0-Pk10IQMnmiC9TQyrj8-logOs2A\", \"not_before\": \"2026-08-04T00:00:00Z\", \"not_after\": \"2026-11-02T00:00:00Z\"}]}\n"
    content_digest  = "sha-256=:nKleGLbn+ROZmNuZfeKnXOJ316s2bkbzrbbO3CBniuM=:"
    signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176433;expires=2106536433;keyid=\"I3TRE_EbrOi6e5Fk__UCxHbTdMauzEXoAbU7S98_Glc\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
    signature       = "sig1=:9uT8PzqF+iIHYIkhtfuAG4lV31aNV+6fTIshpRStdqrnxPy3OCiaa5vhRV9XGhXVRqG2ScNpbmq/oN8GSndhDQ==:"
  })

  # Two listed keys, signed by both for two-keys.staging.example: the SDK's
  # list form, labels sig1 and sig2 joined by ", ".
  two_key_directory = jsonencode({
    body            = "{\"keys\":[{\"alg\":\"EdDSA\",\"crv\":\"Ed25519\",\"kty\":\"OKP\",\"not_after\":\"2026-11-02T00:00:00Z\",\"not_before\":\"2026-08-04T00:00:00Z\",\"use\":\"sig\",\"x\":\"uWPAy8OmQcB0WXxCRLEOO1iqir3CmZ7ZcyZvwf8A4_g\"},{\"alg\":\"EdDSA\",\"crv\":\"Ed25519\",\"kty\":\"OKP\",\"not_after\":\"2026-11-02T00:00:00Z\",\"not_before\":\"2026-08-04T00:00:00Z\",\"use\":\"sig\",\"x\":\"UFpBijUsVr_7cEfecZf5CZPzl4gOnjL4hLm_E8izJRk\"}]}\n"
    content_digest  = "sha-256=:4WDBK4G5fWQt9CjIFoE8ou4AsXXjNJXq2IZw2li6kaE=:"
    signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176008;expires=2106536008;keyid=\"mB8f9G29taSBINbvnMj_7nd26VqoeAEVA5n24TRi-kM\";alg=\"ed25519\";tag=\"http-message-signatures-directory\", sig2=(\"@authority\";req \"content-digest\");created=1791176008;expires=2106536008;keyid=\"KJspYhATZUr0idC0Kt7kXEKVpVieskRkJnYJbdcTwec\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
    signature       = "sig1=:cTDT5O5S9keQUY//KwxxW9KPVAE9OID6ub5LqV3MMIB/VjRjz32NC7IvUEA461Xwg5ZURktTQpprDi4JMPsjBg==:, sig2=:GwZGFCtpSWqd1LWk9rFOVypd3sKY4kwyNfppQGcpf+TMALI/H3OFVxehO33hXuyA0cr8K9xU81R/XfbTOKsOAA==:"
  })
}

run "signed_directory_serves_body_and_signature_headers" {
  command = apply

  variables {
    static_wba_directories = { "smoke-agent.staging.example" = var.smoke_directory }
  }

  assert {
    condition     = strcontains(output.caddyfile, "smoke-agent.staging.example {")
    error_message = "each static_wba_directories hostname must get its own Caddy site block"
  }
  assert {
    # The body lands as a base64-delivered file for Caddy's file_server — the
    # body alone, not the envelope it came in, because the Content-Digest
    # covers exactly the body's bytes.
    condition = (
      strcontains(output.user_data, "path: /opt/fora/wba/smoke-agent.staging.example.json") &&
      strcontains(output.user_data, base64encode(jsondecode(var.smoke_directory).body)) &&
      !strcontains(output.user_data, base64encode(var.smoke_directory))
    )
    error_message = "the body, and only the body, must reach /opt/fora/wba/<hostname>.json byte for byte (base64-encoded in cloud-init)"
  }
  assert {
    condition = (
      output.static_wba_files == { "wba/smoke-agent.staging.example.json" = jsondecode(var.smoke_directory).body }
    )
    error_message = "static_wba_files must map the path under /opt/fora to the body, for a stack that ships the bundle another way"
  }
  assert {
    # No byte of the body is interpolated into the Caddyfile: its extra
    # member, with a backtick that would end a quoted token, is absent.
    condition     = strcontains(output.caddyfile, "file_server") && !strcontains(output.caddyfile, "never reach the Caddyfile") && !strcontains(output.caddyfile, "respond `")
    error_message = "the directory must be served from the written file, never from an interpolated body"
  }
  assert {
    condition = (
      strcontains(output.caddyfile, "\t\theader Content-Type application/http-message-signatures-directory+json\n") &&
      !strcontains(output.user_data, "application/jwk-set+json")
    )
    error_message = "the directory must be served with the media type the Web Bot Auth profile names, never the JWK Set one"
  }
  assert {
    condition = (
      strcontains(output.caddyfile, "\t\theader Content-Digest `${jsondecode(var.smoke_directory).content_digest}`\n") &&
      strcontains(output.caddyfile, "\t\theader Signature-Input `${jsondecode(var.smoke_directory).signature_input}`\n") &&
      strcontains(output.caddyfile, "\t\theader Signature `${jsondecode(var.smoke_directory).signature}`\n")
    )
    error_message = "the directory must be served with its Content-Digest, Signature-Input and Signature headers, each one backtick-quoted token"
  }
  assert {
    condition     = strcontains(output.caddyfile, "\t\trequest_header -Range\n")
    error_message = "the directory must answer 200 with the whole signed body, so a Range request header is dropped"
  }
  assert {
    condition     = strcontains(output.compose_yaml, "- /opt/fora/wba:/srv/wba:ro")
    error_message = "the Caddy container must mount the written directory read-only"
  }
}

run "directory_signed_by_two_keys_serves_both_signatures" {
  command = apply

  variables {
    static_wba_directories = { "two-keys.staging.example" = var.two_key_directory }
  }

  assert {
    condition = (
      strcontains(output.caddyfile, "\t\theader Signature-Input `${jsondecode(var.two_key_directory).signature_input}`\n") &&
      strcontains(output.caddyfile, "\t\theader Signature `${jsondecode(var.two_key_directory).signature}`\n")
    )
    error_message = "a directory listing two keys must be served with both keys' signatures, in the SDK's list form"
  }
}

run "bare_jwk_set_is_rejected" {
  command = plan

  variables {
    # The shape this variable took before directories had to be signed. A
    # verifier takes no key from an unsigned directory, so serving it would
    # authenticate nobody.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsondecode(var.smoke_directory).body
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "envelope_with_a_member_it_does_not_define_is_rejected" {
  command = plan

  variables {
    # A valid signed directory plus a member the format does not define, as
    # a misspelt or hand-added field would arrive. Everything Caddy serves is
    # valid, so only the envelope validation refuses it: the format is exactly
    # the four members, and a value that is not that format is not trusted.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        signature_agent = "https://smoke-agent.staging.example"
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "malformed_signature_value_is_rejected" {
  command = plan

  variables {
    # A well-formed signature followed by a backtick, a newline and a Caddy
    # site block: interpolated as it is, it would end the quoted token and
    # add a site that answers anything. Only the header-shape validation
    # stands in the way; the digest and the keyids are untouched.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        signature = "${jsondecode(var.smoke_directory).signature}`\n}\nowned.example {\n\trespond \"owned\""
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "signature_input_with_a_caddy_placeholder_is_rejected" {
  command = plan

  variables {
    # A brace opens a Caddy placeholder, which Caddy would expand at request
    # time, inside the header value. The keyid still names the listed key, so
    # only the header-shape validation can refuse it.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        signature_input = replace(jsondecode(var.smoke_directory).signature_input, "tag=\"http-message-signatures-directory\"", "tag=\"{env.HOME}\"")
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "body_edited_after_signing_is_rejected" {
  command = plan

  variables {
    # The body changed after it was signed, keeping a valid shape and the
    # same key: content_digest no longer covers what Caddy would serve, so
    # every verifier would refuse the directory.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        body = replace(jsondecode(var.smoke_directory).body, "never reach", "do not reach")
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "directory_signed_by_a_key_it_does_not_list_is_rejected" {
  command = plan

  variables {
    # The smoke body and its digest, with the contributor's signature: every
    # shape is valid and the digest matches, but the keyid is not the
    # thumbprint of the listed key, so a verifier would hand out no key.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        signature_input = jsondecode(var.contributor_directory).signature_input
        signature       = jsondecode(var.contributor_directory).signature
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "directory_signed_by_one_of_two_listed_keys_is_rejected" {
  command = plan

  variables {
    # Two listed keys and only the first signature: the profile requires a
    # signature by every listed key.
    static_wba_directories = {
      "two-keys.staging.example" = jsonencode(merge(jsondecode(var.two_key_directory), {
        signature_input = split(", ", jsondecode(var.two_key_directory).signature_input)[0]
        signature       = split(", ", jsondecode(var.two_key_directory).signature)[0]
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "body_missing_schema_member_is_rejected" {
  command = plan

  variables {
    # `use` is omitted from the body, and content_digest is recomputed so the
    # digest still matches. Without the body validation this directory
    # deploys cleanly, serves 200, and fails the first signed request with an
    # error that points at the signature — the validation moves that failure
    # to `terraform plan`.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        body           = replace(jsondecode(var.smoke_directory).body, "      \"use\": \"sig\",\n", "")
        content_digest = "sha-256=:${base64sha256(replace(jsondecode(var.smoke_directory).body, "      \"use\": \"sig\",\n", ""))}:"
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "body_with_padded_x_is_rejected" {
  command = plan

  variables {
    # A padded (44-character, trailing "=") x: standard base64 instead of the
    # unpadded base64url the schema requires. Same failure mode as a missing
    # member — verifiers cannot match the key.
    static_wba_directories = {
      "smoke-agent.staging.example" = jsonencode(merge(jsondecode(var.smoke_directory), {
        body           = replace(jsondecode(var.smoke_directory).body, "ZhkQ\"", "ZhkQ=\"")
        content_digest = "sha-256=:${base64sha256(replace(jsondecode(var.smoke_directory).body, "ZhkQ\"", "ZhkQ=\""))}:"
      }))
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "malformed_hostname_key_is_rejected" {
  command = plan

  variables {
    # The KEY reaches rendered configuration verbatim (Caddy site block,
    # rewrite target, cloud-init write_files path) — here it carries a
    # Caddyfile brace and a path separator. The directory is valid on purpose:
    # only the hostname validation can reject this input.
    static_wba_directories = {
      "bad host {\nrespond `owned`\n} ignore/../../etc" = var.smoke_directory
    }
  }

  expect_failures = [
    var.static_wba_directories,
  ]
}

run "no_static_wba_directories_by_default" {
  command = apply

  assert {
    # The directory media type is written only by the static-directory site
    # block, so its absence proves the block is not rendered. The path string
    # alone would not discriminate — the compose file names other well-known
    # URLs.
    condition     = !strcontains(output.user_data, "application/http-message-signatures-directory+json") && length(output.static_wba_files) == 0
    error_message = "with no static_wba_directories the Caddyfile must carry no directory site block and no file"
  }
  assert {
    condition     = !strcontains(output.compose_yaml, "/srv/wba")
    error_message = "with no static_wba_directories the Caddy container must not mount the directory volume"
  }
}
