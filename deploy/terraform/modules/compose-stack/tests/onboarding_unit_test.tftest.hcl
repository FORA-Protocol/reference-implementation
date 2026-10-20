# Rendering tests for the optional onboarding preview service. Separate from
# render_unit_test.tftest.hcl because that file is already over the
# repository's test-file length cap.
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

run "onboarding_is_optional" {
  command = apply

  assert {
    condition     = !strcontains(output.compose_yaml, "onboarding")
    error_message = "With no onboarding inputs the compose file must carry no onboarding service — the existing disabled behaviour is unchanged."
  }

  assert {
    condition     = !strcontains(output.caddyfile, "onboarding")
    error_message = "With no onboarding inputs the Caddyfile must carry no onboarding site block."
  }
}

run "onboarding_renders_when_configured" {
  command = apply

  variables {
    onboarding_hostname        = "pub-onboarding.demo.staging.example"
    onboarding_image           = "registry.example.com/group/proj/onboarding@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    onboarding_allowed_origins = ["https://site.example", "https://www.site.example"]
    # No Exchange inputs on purpose: the offer must name the bundle's own
    # Exchange unless told otherwise.
  }

  assert {
    condition     = strcontains(output.compose_yaml, "  onboarding:\n    image: \"registry.example.com/group/proj/onboarding@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"")
    error_message = "The onboarding service must render with the image reference exactly as given — a digest reference has to survive verbatim."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_ADDR: \":8084\"")
    error_message = "The listen address must be :8084, the port the image publishes."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_PATH_PREFIX: \"/onboarding\"")
    error_message = "The routes must be mounted under /onboarding, the prefix Caddy forwards unchanged."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_ALLOWED_ORIGINS: \"https://site.example,https://www.site.example\"")
    error_message = "The allowed origins must render as one comma-separated list in the order given."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_EXCHANGE_DOMAIN: \"exchange.staging.example\"")
    error_message = "The Exchange domain named in the previewed offer must default to this bundle's exchange_hostname."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_EXCHANGE_ENDPOINT: \"https://exchange.staging.example\"")
    error_message = "The Exchange endpoint named in the discovery document must be rendered."
  }

  assert {
    # Caddy terminates TLS in front, so the service must read the forwarded
    # address: without this every request arrives from the proxy and the
    # whole internet shares one rate-limit bucket.
    condition     = strcontains(output.compose_yaml, "FORA_TRUST_PROXY_HEADERS: \"true\"")
    error_message = "FORA_TRUST_PROXY_HEADERS must be true behind Caddy."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "test: [\"CMD\", \"/onboarding\", \"healthcheck\"]")
    error_message = "The health check must exec the service binary: the image is distroless, with no shell or curl."
  }

  assert {
    # The service is reached through Caddy only. A ports: mapping would
    # expose plain HTTP on the host and bypass the proxy's TLS and address
    # forwarding.
    condition     = !can(regex("onboarding:[^\\n]*\\n(?:[^\\n]*\\n)*?\\s+ports:", regex("(?s)  onboarding:.*?\\n  caddy:", output.compose_yaml)))
    error_message = "The onboarding service must publish no ports — 8084 stays on the compose network."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "SKIP_SSRF") && !strcontains(output.compose_yaml, "ALLOW_INSECURE")
    error_message = "SKIP_SSRF and ALLOW_INSECURE must never render: with either set a visitor can point the service at the host's own network."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "ONBOARDING_CONTACT_")
    error_message = "Without contact inputs no contact settings may render — a deployment that takes no enquiries is a supported posture."
  }

  assert {
    condition     = strcontains(output.caddyfile, "pub-onboarding.demo.staging.example {\n\thandle /onboarding/* {\n\t\treverse_proxy onboarding:8084\n\t}")
    error_message = "Caddy must forward /onboarding/* to onboarding:8084 with handle (not handle_path): the service mounts its routes under the prefix and expects it unchanged."
  }

  assert {
    condition     = strcontains(output.caddyfile, "\treverse_proxy onboarding:8084\n\t}\n\trespond 404\n}")
    error_message = "Every path outside the prefix must answer 404 on the onboarding hostname."
  }
}

run "contact_form_renders_when_configured" {
  command = apply

  variables {
    onboarding_hostname          = "pub-onboarding.demo.staging.example"
    onboarding_image             = "registry.example.com/group/proj/onboarding:test"
    onboarding_allowed_origins   = ["https://site.example"]
    onboarding_exchange_domain   = "exchange.staging.example"
    onboarding_exchange_endpoint = "https://exchange.staging.example"

    smtp_host     = "email-smtp.us-east-1.amazonaws.com:587"
    smtp_user     = "AKIAEXAMPLEEXAMPLE"
    smtp_password = "BEXAMPLEsmtppasswordEXAMPLE"
    smtp_from     = "noreply@demo.example"

    onboarding_contact_recipients = ["sales@demo.example", "ops@demo.example"]
    onboarding_contact_from       = "noreply@demo.example"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_CONTACT_RECIPIENTS: \"sales@demo.example,ops@demo.example\"")
    error_message = "The enquiry recipients must render as one comma-separated list."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_CONTACT_FROM: \"noreply@demo.example\"")
    error_message = "The relay sends as the configured from address; the visitor's address is only ever Reply-To."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_SMTP_ADDR: \"email-smtp.us-east-1.amazonaws.com:587\"")
    error_message = "The relay address must be the module's smtp_host, port included — the service has no default port."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_SMTP_USERNAME: \"AKIAEXAMPLEEXAMPLE\"") && strcontains(output.compose_yaml, "ONBOARDING_SMTP_PASSWORD: \"BEXAMPLEsmtppasswordEXAMPLE\"")
    error_message = "The relay credentials must be the module's smtp_user and smtp_password."
  }

  assert {
    condition     = !strcontains(output.compose_yaml, "ONBOARDING_SMTP_ALLOW_PLAINTEXT")
    error_message = "ONBOARDING_SMTP_ALLOW_PLAINTEXT must never render: it would send the relay password in the clear."
  }
}

run "exchange_inputs_override_the_default" {
  command = apply

  variables {
    onboarding_hostname          = "pub-onboarding.demo.staging.example"
    onboarding_image             = "registry.example.com/group/proj/onboarding:test"
    onboarding_allowed_origins   = ["https://site.example"]
    onboarding_exchange_domain   = "exchange.other.example"
    onboarding_exchange_endpoint = "https://exchange.other.example:8443"
  }

  assert {
    condition     = strcontains(output.compose_yaml, "ONBOARDING_EXCHANGE_DOMAIN: \"exchange.other.example\"") && strcontains(output.compose_yaml, "ONBOARDING_EXCHANGE_ENDPOINT: \"https://exchange.other.example:8443\"")
    error_message = "Explicit Exchange inputs must win over the bundle's own Exchange."
  }
}

run "enabled_without_origins_fails_the_plan" {
  command = plan

  variables {
    onboarding_hostname = "pub-onboarding.demo.staging.example"
    onboarding_image    = "registry.example.com/group/proj/onboarding:test"
  }

  expect_failures = [random_password.postgres]
}

run "hostname_without_image_fails_the_plan" {
  command = plan

  variables {
    onboarding_hostname = "pub-onboarding.demo.staging.example"
    # An origin is given so that only the pairing rule can fail this plan.
    onboarding_allowed_origins = ["https://site.example"]
  }

  expect_failures = [random_password.postgres]
}

run "image_without_hostname_fails_the_plan" {
  command = plan

  variables {
    onboarding_image = "registry.example.com/group/proj/onboarding:test"
  }

  expect_failures = [random_password.postgres]
}

run "contact_recipients_without_from_fails_the_plan" {
  command = plan

  variables {
    onboarding_hostname           = "pub-onboarding.demo.staging.example"
    onboarding_image              = "registry.example.com/group/proj/onboarding:test"
    onboarding_allowed_origins    = ["https://site.example"]
    onboarding_contact_recipients = ["sales@demo.example"]
    smtp_host                     = "email-smtp.us-east-1.amazonaws.com:587"
    smtp_user                     = "AKIAEXAMPLEEXAMPLE"
    smtp_password                 = "BEXAMPLEsmtppasswordEXAMPLE"
    smtp_from                     = "noreply@demo.example"
  }

  expect_failures = [random_password.postgres]
}

run "contact_from_without_recipients_fails_the_plan" {
  command = plan

  variables {
    onboarding_hostname        = "pub-onboarding.demo.staging.example"
    onboarding_image           = "registry.example.com/group/proj/onboarding:test"
    onboarding_allowed_origins = ["https://site.example"]
    onboarding_contact_from    = "noreply@demo.example"
    smtp_host                  = "email-smtp.us-east-1.amazonaws.com:587"
    smtp_user                  = "AKIAEXAMPLEEXAMPLE"
    smtp_password              = "BEXAMPLEsmtppasswordEXAMPLE"
    smtp_from                  = "noreply@demo.example"
  }

  expect_failures = [random_password.postgres]
}

run "contact_without_relay_fails_the_plan" {
  command = plan

  variables {
    onboarding_hostname           = "pub-onboarding.demo.staging.example"
    onboarding_image              = "registry.example.com/group/proj/onboarding:test"
    onboarding_allowed_origins    = ["https://site.example"]
    onboarding_contact_recipients = ["sales@demo.example"]
    onboarding_contact_from       = "noreply@demo.example"
  }

  expect_failures = [random_password.postgres]
}

run "contact_without_onboarding_fails_the_plan" {
  command = plan

  variables {
    onboarding_contact_recipients = ["sales@demo.example"]
    onboarding_contact_from       = "noreply@demo.example"
    smtp_host                     = "email-smtp.us-east-1.amazonaws.com:587"
    smtp_user                     = "AKIAEXAMPLEEXAMPLE"
    smtp_password                 = "BEXAMPLEsmtppasswordEXAMPLE"
    smtp_from                     = "noreply@demo.example"
  }

  expect_failures = [random_password.postgres]
}
