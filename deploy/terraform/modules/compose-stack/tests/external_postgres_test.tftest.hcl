# Real-provider rendering contract for external PostgreSQL; no AWS resources.
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

run "external_postgres_omits_local_database_servers" {
  command = apply
  variables {
    external_postgres = {
      host   = "db.example.us-east-1.rds.amazonaws.com"
      port   = 5543
      ca_pem = "-----BEGIN CERTIFICATE-----\ndGVzdA==\n-----END CERTIFICATE-----\n"
    }
  }
  assert {
    condition     = !contains(keys(yamldecode(output.compose_yaml).services), "postgres") && !contains(keys(yamldecode(output.compose_yaml).services), "zitadel-db")
    error_message = "External PostgreSQL must omit postgres and zitadel-db services."
  }
}

run "external_credentials_tls_and_file_isolation" {
  command = apply
  variables {
    external_postgres = {
      host   = "db.example.us-east-1.rds.amazonaws.com"
      port   = 5543
      ca_pem = "-----BEGIN CERTIFICATE-----\ndGVzdA==\n-----END CERTIFICATE-----\n"
    }
  }

  assert {
    condition     = toset(keys(jsondecode(output.database_config_json))) == toset(["host", "port", "sslmode", "sslrootcert", "client_image", "databases"]) && jsondecode(output.database_config_json).host == var.external_postgres.host && jsondecode(output.database_config_json).port == 5543 && jsondecode(output.database_config_json).sslmode == "verify-full" && jsondecode(output.database_config_json).sslrootcert == "/db-ca/ca.pem" && jsondecode(output.database_config_json).client_image == "postgres:16-alpine"
    error_message = "Bootstrap contract must contain only scoped connection settings, PG16 client and verified TLS."
  }
  assert {
    condition     = toset(keys(jsondecode(output.database_config_json).databases)) == toset(["fora", "sor", "identity", "zitadel"]) && alltrue([for name, credentials in jsondecode(output.database_config_json).databases : credentials.username == name && length(credentials.password) >= 24 && toset(keys(credentials)) == toset(["username", "password"])]) && length(distinct([for credentials in values(jsondecode(output.database_config_json).databases) : credentials.password])) == 4
    error_message = "Each of four databases must have a distinct generated scoped owner credential; no administrator fields."
  }
  assert {
    condition = alltrue([for connection in [
      { service = "exchange", env = "EXCHANGE_DSN", db = "fora" },
      { service = "exchange", env = "EXCHANGE_SOR_DSN", db = "sor" },
      { service = "broker", env = "BROKER_DSN", db = "fora" },
      { service = "identity", env = "IDENTITY_DSN", db = "identity" }
      ] : startswith(yamldecode(output.compose_yaml).services[connection.service].environment[connection.env], "postgres://${connection.db}:${jsondecode(output.database_config_json).databases[connection.db].password}@${var.external_postgres.host}:5543/${connection.db}?") &&
      contains(split("&", split("?", yamldecode(output.compose_yaml).services[connection.service].environment[connection.env])[1]), "sslmode=verify-full") &&
      contains([for parameter in split("&", split("?", yamldecode(output.compose_yaml).services[connection.service].environment[connection.env])[1]) : replace(lower(parameter), "%2f", "/")], "sslrootcert=/db-ca/ca.pem")
    ])
    error_message = "All application DSNs must match bootstrap credentials and retain verified TLS and the CA path."
  }
  assert {
    condition     = alltrue([for name in ["exchange", "broker", "identity", "zitadel"] : contains(yamldecode(output.compose_yaml).services[name].volumes, "/opt/fora/database/ca.pem:/db-ca/ca.pem:ro")]) && alltrue(flatten([for service in values(yamldecode(output.compose_yaml).services) : [for mount in try(service.volumes, []) : !startswith(mount, "/opt/fora/database") || mount == "/opt/fora/database/ca.pem:/db-ca/ca.pem:ro"]]))
    error_message = "Only the read-only CA file may enter containers; connection.json and its directory contain other services' credentials."
  }
  assert {
    condition     = !contains(keys(yamldecode(output.compose_yaml).volumes), "pgdata") && !contains(keys(yamldecode(output.compose_yaml).volumes), "zitadeldata") && alltrue(flatten([for service in values(yamldecode(output.compose_yaml).services) : [for dependency in keys(try(service.depends_on, {})) : contains(keys(yamldecode(output.compose_yaml).services), dependency) && !contains(["postgres", "zitadel-db"], dependency)]]))
    error_message = "External mode must remove database volumes and local database dependencies without dangling dependencies."
  }
  assert {
    condition     = yamldecode(output.compose_yaml).services.zitadel.command[0] == "start-from-setup" && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_HOST == var.external_postgres.host && tostring(yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_PORT) == "5543" && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_DATABASE == "zitadel" && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_USER_USERNAME == "zitadel" && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_USER_PASSWORD == jsondecode(output.database_config_json).databases.zitadel.password && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE == "verify-full" && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_USER_SSL_ROOTCERT == "/db-ca/ca.pem" && alltrue([for name in keys(yamldecode(output.compose_yaml).services.zitadel.environment) : !startswith(name, "ZITADEL_DATABASE_POSTGRES_ADMIN_")])
    error_message = "Zitadel must run scoped setup with certificate verification and no database administrator settings."
  }
  assert {
    condition     = alltrue([for file in yamldecode(output.user_data).write_files : file.path != "/opt/fora/init-sql/10-create-exchange-dbs.sql"]) && length([for file in yamldecode(output.user_data).write_files : file if file.path == "/opt/fora/database/ca.pem" && file.permissions == "0644" && trimspace(file.content) == trimspace(var.external_postgres.ca_pem)]) == 1 && length([for file in yamldecode(output.user_data).write_files : file if file.path == "/opt/fora/database/connection.json" && file.permissions == "0600" && try(jsondecode(file.content), null) == jsondecode(output.database_config_json)]) == 1
    error_message = "External cloud-init must deliver readable CA and private scoped contract, without local database init SQL."
  }
}

run "self_hosted_defaults_remain_local" {
  command = apply
  assert {
    condition     = contains(keys(yamldecode(output.compose_yaml).services), "postgres") && contains(keys(yamldecode(output.compose_yaml).services), "zitadel-db") && contains(keys(yamldecode(output.compose_yaml).volumes), "pgdata") && contains(keys(yamldecode(output.compose_yaml).volumes), "zitadeldata") && yamldecode(output.compose_yaml).services.zitadel.command[0] == "start-from-init" && yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE == "disable"
    error_message = "Self-hosted defaults must retain local PostgreSQL services, volumes and Zitadel initialization."
  }
  assert {
    condition     = yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_DSN == "postgres://fora:${output.postgres_password}@postgres:5432/fora?sslmode=disable" && yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_SOR_DSN == "postgres://fora:${output.postgres_password}@postgres:5432/sor?sslmode=disable" && yamldecode(output.compose_yaml).services.broker.environment.BROKER_DSN == "postgres://fora:${output.postgres_password}@postgres:5432/fora?sslmode=disable" && yamldecode(output.compose_yaml).services.identity.environment.IDENTITY_DSN == "postgres://fora:${output.postgres_password}@postgres:5432/identity?sslmode=disable"
    error_message = "Legacy local service DSNs must remain unchanged."
  }
  assert {
    condition     = output.database_config_json == null && alltrue(flatten([for service in values(yamldecode(output.compose_yaml).services) : [for mount in try(service.volumes, []) : !startswith(mount, "/opt/fora/database")]]))
    error_message = "Self-hosted mode must not ship an external bootstrap credential contract or CA mounts."
  }
}

run "external_default_port" {
  command = apply
  variables {
    external_postgres = {
      host   = "db.example.us-east-1.rds.amazonaws.com"
      ca_pem = "-----BEGIN CERTIFICATE-----\ndGVzdA==\n-----END CERTIFICATE-----\n"
    }
  }
  assert {
    condition     = jsondecode(output.database_config_json).port == 5432 && strcontains(yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_DSN, "@db.example.us-east-1.rds.amazonaws.com:5432/fora?")
    error_message = "Omitting external port must use PostgreSQL default 5432 in contract and runtime DSNs."
  }
}

run "external_invalid_host_rejected" {
  command = plan
  variables {
    external_postgres = {
      host   = "localhost"
      port   = 5432
      ca_pem = "-----BEGIN CERTIFICATE-----\ndGVzdA==\n-----END CERTIFICATE-----\n"
    }
  }
  expect_failures = [var.external_postgres]
}

run "external_invalid_port_rejected" {
  command = plan
  variables {
    external_postgres = {
      host   = "db.example.us-east-1.rds.amazonaws.com"
      port   = 65536
      ca_pem = "-----BEGIN CERTIFICATE-----\ndGVzdA==\n-----END CERTIFICATE-----\n"
    }
  }
  expect_failures = [var.external_postgres]
}

run "external_invalid_ca_rejected" {
  command = plan
  variables {
    external_postgres = {
      host   = "db.example.us-east-1.rds.amazonaws.com"
      port   = 5432
      ca_pem = "not a PEM certificate"
    }
  }
  expect_failures = [var.external_postgres]
}
