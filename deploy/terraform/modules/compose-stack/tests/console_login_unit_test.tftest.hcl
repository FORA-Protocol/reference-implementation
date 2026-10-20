# Rendering tests for the publishers' Zitadel instance's inputs: the System API
# user's recovery window, and every combination of the instance's inputs the
# plan refuses. console_unit_test.tftest.hcl covers the instance's rendering
# with the console on and off.
#
# `command = apply` where something is read back, for the same reason as the
# sibling files: the templates interpolate random values. Every refusal run is
# otherwise a valid console configuration, so the expected failure is the only
# one.
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

  # A console configuration with the importer off, so the contributor host is
  # free. The keys are the console test's fixtures: shape only.
  exchange_terms_currency  = "EUR"
  exchange_terms_unit      = "fetches"
  console_hostname         = "console.staging.example"
  console_image            = "registry.example.com/group/proj/console@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  console_signing_key_pem  = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIGZha2UgY29uc29sZSBzaWduaW5nIGtleSBmaXh0dXJl\n-----END PRIVATE KEY-----\n"
  console_contributor_host = "catalog-contributor.staging.example"
  console_support_email    = "help@staging.example"

  console_login_hostname        = "login.console.staging.example"
  zitadel_system_api_public_key = "-----BEGIN PUBLIC KEY-----\nZmFrZSBzeXN0ZW0gYXBpIHB1YmxpYyBrZXkgZml4dHVyZQ==\n-----END PUBLIC KEY-----\n"
}

run "recovery_window_adds_iam_owner_on_one_instance" {
  command = apply

  variables {
    zitadel_system_api_recovery_instance_id = "393782486556999684"
  }

  assert {
    condition = jsondecode(yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_SYSTEMAPIUSERS) == {
      "fora-instance-admin" = {
        KeyData = base64encode(var.zitadel_system_api_public_key)
        Memberships = [
          { MemberType = "System", Roles = ["SYSTEM_OWNER"] },
          { MemberType = "IAM", Roles = ["IAM_OWNER"], AggregateID = "393782486556999684" },
        ]
      }
    }
    error_message = "A recovery window must keep the System API user's SYSTEM_OWNER and add IAM_OWNER on exactly the instance being recovered."
  }
}

run "console_without_login_hostname_fails_the_plan" {
  command = plan

  variables {
    console_login_hostname = null
  }

  expect_failures = [random_password.postgres]
}

run "console_without_system_api_key_fails_the_plan" {
  command = plan

  variables {
    zitadel_system_api_public_key = null
  }

  expect_failures = [random_password.postgres]
}

run "publisher_instance_inputs_without_console_fail_the_plan" {
  command = plan

  variables {
    console_hostname         = null
    console_image            = null
    console_signing_key_pem  = null
    console_contributor_host = null
    console_support_email    = null
  }

  expect_failures = [random_password.postgres]
}

run "login_hostname_of_the_developers_instance_fails_the_plan" {
  command = plan

  variables {
    console_login_hostname = "login.staging.example"
  }

  expect_failures = [random_password.postgres]
}

run "login_hostname_of_the_console_fails_the_plan" {
  command = plan

  variables {
    console_login_hostname = "console.staging.example"
  }

  expect_failures = [random_password.postgres]
}

run "recovery_window_without_system_api_key_fails_the_plan" {
  command = plan

  # Console off, so the pairing rule is satisfied and only the recovery rule
  # can fail this plan.
  variables {
    console_hostname                        = null
    console_image                           = null
    console_signing_key_pem                 = null
    console_contributor_host                = null
    console_support_email                   = null
    console_login_hostname                  = null
    zitadel_system_api_public_key           = null
    zitadel_system_api_recovery_instance_id = "393782486556999684"
  }

  expect_failures = [random_password.postgres]
}

run "private_key_as_system_api_key_is_rejected" {
  command = plan

  variables {
    zitadel_system_api_public_key = "-----BEGIN PRIVATE KEY-----\nZmFrZSBzeXN0ZW0gYXBpIHB1YmxpYyBrZXkgZml4dHVyZQ==\n-----END PRIVATE KEY-----\n"
  }

  expect_failures = [var.zitadel_system_api_public_key]
}

run "login_hostname_with_a_scheme_is_rejected" {
  command = plan

  variables {
    console_login_hostname = "https://login.console.staging.example"
  }

  expect_failures = [var.console_login_hostname]
}

run "recovery_instance_id_that_is_not_an_id_is_rejected" {
  command = plan

  variables {
    zitadel_system_api_recovery_instance_id = "393782486556999684\"}"
  }

  expect_failures = [var.zitadel_system_api_recovery_instance_id]
}
