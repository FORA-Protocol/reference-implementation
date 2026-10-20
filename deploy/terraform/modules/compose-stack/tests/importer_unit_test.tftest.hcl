# Rendering tests for the automatic catalog importer: the importer service in
# the compose bundle, its key delivery, and the systemd timer that schedules
# it. Separate from render_unit_test.tftest.hcl only because that file is at
# the repository's test-file length cap.
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

  # The contributor identity: its pre-signed public directory served by
  # Caddy (static_wba_unit_test.tftest.hcl explains the fixture), its
  # private key (fixture shape) delivered to the importer alone.
  static_wba_directories = {
    "catalog-contributor.staging.example" = jsonencode({
      body            = "{\"keys\": [{\"kty\": \"OKP\", \"crv\": \"Ed25519\", \"use\": \"sig\", \"alg\": \"EdDSA\", \"x\": \"sT4EIcOWpRK29gl0-Pk10IQMnmiC9TQyrj8-logOs2A\", \"not_before\": \"2026-08-04T00:00:00Z\", \"not_after\": \"2026-11-02T00:00:00Z\"}]}\n"
      content_digest  = "sha-256=:nKleGLbn+ROZmNuZfeKnXOJ316s2bkbzrbbO3CBniuM=:"
      signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176433;expires=2106536433;keyid=\"I3TRE_EbrOi6e5Fk__UCxHbTdMauzEXoAbU7S98_Glc\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
      signature       = "sig1=:9uT8PzqF+iIHYIkhtfuAG4lV31aNV+6fTIshpRStdqrnxPy3OCiaa5vhRV9XGhXVRqG2ScNpbmq/oN8GSndhDQ==:"
    })
  }
  importer_key_fixture = "{\"kid\": \"catalog-contributor.staging.example\", \"private_key\": \"fixture-private\", \"public_key\": \"fixture-public\"}\n"
}

run "importer_off_by_default" {
  command = apply

  # Staging and demo VMs are replaced whenever their user data changes a
  # byte, so a stack that passes no key must render exactly what it rendered
  # before the importer existed.
  assert {
    condition     = !strcontains(output.compose_yaml, "importer:") && !strcontains(output.compose_yaml, "/fora-import")
    error_message = "Without importer_key_json the compose bundle must carry no importer service."
  }
  assert {
    condition     = !strcontains(output.user_data, "fora-import") && !strcontains(output.user_data, "/opt/fora/keys/importer")
    error_message = "Without importer_key_json cloud-init must write no importer key and no importer unit."
  }
  assert {
    condition     = length(output.importer_units) == 0
    error_message = "Without importer_key_json the importer_units output must be empty."
  }
  assert {
    condition     = !strcontains(output.compose_yaml, "EXCHANGE_TERMS_CURRENCY") && !strcontains(output.compose_yaml, "EXCHANGE_TERMS_UNIT")
    error_message = "Without exchange_terms_currency and exchange_terms_unit the bundle must set neither, so it renders byte-identical."
  }
}

run "importer_prices_in_the_exchange_denomination" {
  command = apply

  variables {
    importer_key_json       = var.importer_key_fixture
    exchange_terms_currency = "USD"
    exchange_terms_unit     = "accesses"
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_TERMS_CURRENCY == "USD" &&
      yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_TERMS_UNIT == "accesses" &&
      yamldecode(output.compose_yaml).services.importer.environment.EXCHANGE_TERMS_CURRENCY == "USD" &&
      yamldecode(output.compose_yaml).services.importer.environment.EXCHANGE_TERMS_UNIT == "accesses"
    )
    error_message = "The Exchange and the importer must both carry the configured denomination: the importer prices imported entries in what the console previewed."
  }
}

run "importer_service_key_and_timer" {
  command = apply

  variables {
    importer_key_json = var.importer_key_fixture
    importer_schedule = "*-*-* 03:00:00"
  }

  assert {
    condition     = yamldecode(output.compose_yaml).services.importer.image == yamldecode(output.compose_yaml).services.exchange.image && yamldecode(output.compose_yaml).services.importer.entrypoint == ["/fora-import"]
    error_message = "The importer must run the Exchange image with /fora-import as its entrypoint."
  }
  assert {
    condition     = yamldecode(output.compose_yaml).services.importer.profiles == ["importer"]
    error_message = "The importer must sit behind the importer profile so `docker compose up` never starts it."
  }
  assert {
    condition     = yamldecode(output.compose_yaml).services.importer.volumes == ["/opt/fora/keys/importer:/keys:ro"]
    error_message = "The importer must mount its own key directory read-only and nothing else."
  }
  assert {
    condition     = !contains(yamldecode(output.compose_yaml).services.exchange.volumes, "/opt/fora/keys/importer:/keys:ro") && alltrue([for mount in yamldecode(output.compose_yaml).services.exchange.volumes : !strcontains(mount, "importer")])
    error_message = "The Exchange service must never mount the importer's key directory."
  }
  assert {
    condition = (
      yamldecode(output.compose_yaml).services.importer.environment.EXCHANGE_DSN == yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_DSN &&
      yamldecode(output.compose_yaml).services.importer.environment.EXCHANGE_DOMAIN == "exchange.staging.example" &&
      yamldecode(output.compose_yaml).services.importer.environment.IMPORT_EXCHANGE_URL == "https://exchange.staging.example" &&
      yamldecode(output.compose_yaml).services.importer.environment.IMPORT_KEY_FILE == "/keys/contributor-key.json" &&
      tostring(yamldecode(output.compose_yaml).services.importer.environment.IMPORT_MAX_RESOURCES) == "10000"
    )
    error_message = "The importer must read the Exchange database, the seat's hostname, the public Exchange URL, its key file and the run limits from its environment."
  }
  assert {
    condition     = strcontains(output.user_data, "path: /opt/fora/keys/importer/contributor-key.json") && strcontains(output.user_data, indent(6, var.importer_key_fixture))
    error_message = "cloud-init must write the contributor key under /opt/fora/keys/importer, root-only, where the runcmd re-own opens it to the importer's gid."
  }
  assert {
    condition     = strcontains(output.user_data, "path: /etc/systemd/system/fora-import.service") && strcontains(output.user_data, "path: /etc/systemd/system/fora-import.timer")
    error_message = "cloud-init must write both importer units under /etc/systemd/system."
  }
  assert {
    condition     = strcontains(output.user_data, "OnCalendar=*-*-* 03:00:00") && strcontains(output.user_data, "Persistent=true")
    error_message = "The timer must fire on importer_schedule and make up a missed run."
  }
  assert {
    condition     = strcontains(output.user_data, "ExecStart=/usr/bin/docker compose -p fora -f /opt/fora/docker-compose.yml --profile importer run --rm importer") && strcontains(output.user_data, "Type=oneshot")
    error_message = "The service must be a oneshot running the compose job under the importer profile — the same command a manual rerun uses."
  }
  assert {
    condition     = strcontains(output.user_data, "- systemctl enable --now fora-import.timer") && index(split("\n", output.user_data), "  - systemctl enable --now fora-import.timer") > index(split("\n", output.user_data), "  - docker compose -f /opt/fora/docker-compose.yml up -d")
    error_message = "cloud-init must enable the timer, after the stack is up."
  }
  assert {
    condition     = keys(output.importer_units) == tolist(["fora-import.service", "fora-import.timer"]) && strcontains(output.importer_units["fora-import.timer"], "OnCalendar=*-*-* 03:00:00")
    error_message = "importer_units must expose both units, rendered with the schedule, for a stack that installs them itself."
  }
}

run "importer_key_must_name_a_served_directory" {
  command = plan

  variables {
    importer_key_json = "{\"kid\": \"nobody.example\", \"private_key\": \"x\", \"public_key\": \"y\"}"
  }

  expect_failures = [
    var.importer_key_json,
  ]
}

run "importer_schedule_rejects_unit_syntax" {
  command = plan

  variables {
    importer_key_json = var.importer_key_fixture
    importer_schedule = "hourly\nExecStart=/bin/sh"
  }

  expect_failures = [
    var.importer_schedule,
  ]
}
