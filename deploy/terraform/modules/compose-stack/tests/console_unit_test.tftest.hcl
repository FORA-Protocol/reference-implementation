# Rendering tests for the optional publisher console: its service, its Caddy
# site, its database, its signing key on the VM, and the two Exchange settings
# that turn its routes on.
#
# Same properties as its siblings: `command = apply` is needed because the
# templates interpolate random_password, and the module's only resources ARE
# those in-memory random values — nothing is created outside the test process.
#
# The runs share one state, in file order, so a later run sees the secrets an
# earlier one generated. console_renders_when_configured compares its output
# with console_off_renders_nothing, which has the same inputs except the
# console's own, and console_on_external_postgres reads the session key the
# run before it generated.
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

  # The blocks next to the four places the console is inserted are on here,
  # so the seam assertions read real bytes on both sides of each place: the
  # terms digest before the Exchange's ports, the onboarding service before
  # Caddy's, the onboarding site before the key-directory sites, and the
  # importer's key before its systemd units. console_off_with_neighbours_off
  # turns them off again. The terms denomination is set because the console
  # requires it.
  static_wba_directories = {
    "catalog-contributor.staging.example" = jsonencode({
      body            = "{\"keys\": [{\"kty\": \"OKP\", \"crv\": \"Ed25519\", \"use\": \"sig\", \"alg\": \"EdDSA\", \"x\": \"sT4EIcOWpRK29gl0-Pk10IQMnmiC9TQyrj8-logOs2A\", \"not_before\": \"2026-08-04T00:00:00Z\", \"not_after\": \"2026-11-02T00:00:00Z\"}]}\n"
      content_digest  = "sha-256=:nKleGLbn+ROZmNuZfeKnXOJ316s2bkbzrbbO3CBniuM=:"
      signature_input = "sig1=(\"@authority\";req \"content-digest\");created=1791176433;expires=2106536433;keyid=\"I3TRE_EbrOi6e5Fk__UCxHbTdMauzEXoAbU7S98_Glc\";alg=\"ed25519\";tag=\"http-message-signatures-directory\""
      signature       = "sig1=:9uT8PzqF+iIHYIkhtfuAG4lV31aNV+6fTIshpRStdqrnxPy3OCiaa5vhRV9XGhXVRqG2ScNpbmq/oN8GSndhDQ==:"
    })
  }
  importer_key_json          = "{\"kid\": \"catalog-contributor.staging.example\", \"private_key\": \"fixture-private\", \"public_key\": \"fixture-public\"}\n"
  onboarding_hostname        = "pub-onboarding.staging.example"
  onboarding_image           = "registry.example.com/group/proj/onboarding:test"
  onboarding_allowed_origins = ["https://site.example"]
  exchange_terms_digest      = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
  exchange_terms_currency    = "EUR"
  exchange_terms_unit        = "fetches"

  # The console's inputs, for the runs that turn it on. The key is a
  # well-formed PKCS#8 Ed25519 key whose 32-byte seed is the ASCII text
  # "fake console signing key fixture": the exact shape the module checks,
  # and plainly not a key anyone signs with.
  console_key_fixture   = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIGZha2UgY29uc29sZSBzaWduaW5nIGtleSBmaXh0dXJl\n-----END PRIVATE KEY-----\n"
  console_image_fixture = "registry.example.com/group/proj/console@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

  # The publishers' Zitadel instance, which the console needs. The key is a
  # PEM block of the right label whose body is the ASCII text "fake system api
  # public key fixture": shape only.
  console_login_fixture = "login.console.staging.example"
  # The block the Zitadel site carries while a System API user exists, as the
  # comment-free Caddyfile renders it: a 404 for every route a system
  # permission opens, the System API and the system-wide features.
  system_api_block       = "\t@system {\n\t\tpath /system/* /zitadel.system.v1.SystemService/*\n\t\tpath /v2/features/system /v2/features/system/* /v2beta/features/system /v2beta/features/system/*\n\t\tpath /zitadel.feature.v2.FeatureService/SetSystemFeatures /zitadel.feature.v2.FeatureService/ResetSystemFeatures /zitadel.feature.v2.FeatureService/GetSystemFeatures\n\t\tpath /zitadel.feature.v2beta.FeatureService/SetSystemFeatures /zitadel.feature.v2beta.FeatureService/ResetSystemFeatures /zitadel.feature.v2beta.FeatureService/GetSystemFeatures\n\t}\n\trespond @system 404\n"
  system_api_key_fixture = "-----BEGIN PUBLIC KEY-----\nZmFrZSBzeXN0ZW0gYXBpIHB1YmxpYyBrZXkgZml4dHVyZQ==\n-----END PUBLIC KEY-----\n"
}

run "console_off_renders_nothing" {
  command = apply

  # Every rendered document of master mentions "console" only in comment
  # lines (the Zitadel console), so the comment-free documents are where a
  # console artefact would show.
  assert {
    condition = (
      !strcontains(lower(output.compose_yaml_without_comments), "console") &&
      !strcontains(lower(output.caddyfile_without_comments), "console") &&
      !strcontains(lower(output.user_data_without_comments), "console") &&
      !strcontains(lower(output.init_sql), "console")
    )
    error_message = "With console_hostname unset no console service, setting, site, key file or database may render."
  }

  assert {
    condition     = length(random_bytes.console_session_key) == 0
    error_message = "With the console off no console session key may be generated."
  }

  assert {
    condition = (
      !strcontains(output.compose_yaml, "ZITADEL_SYSTEMAPIUSERS") &&
      !strcontains(output.caddyfile, "@system") &&
      length(random_password.console_login_admin) == 0 &&
      output.console_login_admin_password == null
    )
    error_message = "With the console off there is no publishers' instance: no System API user, no System API block in Caddy and no publishers' admin password."
  }

  assert {
    condition = (
      strcontains(output.caddyfile, file("../../../zitadel/developer-login.css")) &&
      !strcontains(output.caddyfile, "@publisher_css")
    )
    error_message = "Developer branding must render even without the publisher console."
  }

  # The seams: master's exact bytes on both sides of each insertion point.
  # The blocks are added with %{ if ~} ... %{ endif ~}, which renders nothing
  # when off; a lost ~ adds one blank line here, and the staging VM, whose
  # user data embeds these documents, would be replaced.
  assert {
    condition     = strcontains(output.compose_yaml, "      EXCHANGE_TERMS_DIGEST: \"sha256:0000000000000000000000000000000000000000000000000000000000000000\"\n    ports:\n")
    error_message = "The Exchange's environment must end where it did before the console existed: the terms digest, then ports."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "      retries: 3\n\n  caddy:\n")
    error_message = "The onboarding service must be followed by Caddy exactly as before the console existed."
  }

  assert {
    condition     = strcontains(output.caddyfile, "\trespond 404\n}\n\n# Static Web Bot Auth key directory")
    error_message = "The onboarding site must be followed by the key-directory sites exactly as before the console existed."
  }

  assert {
    condition     = strcontains(output.user_data, "\"public_key\": \"fixture-public\"}\n      \n\n  # The importer's schedule lives in systemd")
    error_message = "The importer's key file must be followed by its systemd units exactly as before the console existed."
  }
}

run "console_off_with_neighbours_off" {
  command = apply

  variables {
    static_wba_directories     = {}
    importer_key_json          = null
    onboarding_hostname        = null
    onboarding_image           = null
    onboarding_allowed_origins = []
    exchange_terms_digest      = null
  }

  assert {
    condition     = strcontains(output.compose_yaml, "      FORA_ED25519_PRIVATE_PEM_FILE: \"/keys/ed25519-private.pem\"\n\n    ports:\n")
    error_message = "With no terms digest the Exchange's environment must end as before the console existed: the key file, one blank line, then ports."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "      zitadel:\n        condition: service_started\n\n\n\n  caddy:\n")
    error_message = "With no publisher and no onboarding service, the Identity Service must be followed by Caddy exactly as before the console existed."
  }

  assert {
    condition     = endswith(output.caddyfile, "\treverse_proxy zitadel:8080 {\n\t\ttransport http {\n\t\t\tversions h2c 2\n\t\t}\n\t}\n}\n")
    error_message = "With no optional site the Caddyfile must end with the Zitadel site, as before the console existed."
  }

  assert {
    condition     = strcontains(output.user_data, "-----END ED25519 PRIVATE KEY-----\n      \n\n\n\n\nruncmd:\n")
    error_message = "With no importer the Broker's identity key must be followed by runcmd exactly as before the console existed."
  }
}

run "console_renders_when_configured" {
  command = apply

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  assert {
    condition = yamldecode(output.compose_yaml).services.console.environment == {
      CONSOLE_ADDR                    = ":8085"
      CONSOLE_DSN                     = "postgres://fora:${output.postgres_password}@postgres:5432/console?sslmode=disable"
      CONSOLE_SESSION_KEY             = random_bytes.console_session_key[0].base64
      CONSOLE_SIGNING_KEY_FILE        = "/keys/console-signing-key.pem"
      CONSOLE_PUBLIC_ORIGIN           = "https://console.staging.example"
      CONSOLE_OIDC_ISSUER             = "https://login.console.staging.example"
      CONSOLE_OIDC_CLIENT_ID_FILE     = "/console-oidc/console_client_id"
      CONSOLE_OIDC_CLIENT_SECRET_FILE = "/console-oidc/console_client_secret"
      CONSOLE_OIDC_REDIRECT_URL       = "https://console.staging.example/auth/callback"
      CONSOLE_OIDC_SCOPES             = "openid profile email"
      CONSOLE_EXCHANGE_URL            = "https://exchange.staging.example"
      CONSOLE_EXCHANGE_DOMAIN         = "exchange.staging.example"
      CONSOLE_EXCHANGE_WBA_URL        = "https://exchange.staging.example/.well-known/http-message-signatures-directory"
      CONSOLE_CONTRIBUTOR_HOST        = "catalog-contributor.staging.example"
      CONSOLE_SUPPORT_EMAIL           = "help@staging.example"
    }
    error_message = "The console's environment must be exactly the derived settings: public origins only, the publishers' own Zitadel instance as the issuer, the session key from state, the client files from the console's own directory, and no default rate unless one is given."
  }

  assert {
    condition = (
      jsondecode(yamldecode(output.compose_yaml).services.zitadel.environment.ZITADEL_SYSTEMAPIUSERS) == {
        "fora-instance-admin" = { KeyData = base64encode(var.system_api_key_fixture) }
      }
    )
    error_message = "Zitadel must trust exactly one System API user, fora-instance-admin, with the given public key and no membership beyond Zitadel's default (SYSTEM_OWNER)."
  }

  assert {
    condition     = strcontains(output.caddyfile_without_comments, "\nlogin.staging.example, login.console.staging.example {\n${var.system_api_block}")
    error_message = "One Zitadel site must serve both instances' hostnames and, before reverse_proxy, answer 404 for every route a system permission opens: the System API user holds SYSTEM_OWNER."
  }

  assert {
    condition = (
      strcontains(output.caddyfile, "\thost login.staging.example\n\t\tpath /ui/login/resources/themes/zitadel/css/zitadel.css\n\t\tnot query fora-original=1") &&
      strcontains(output.caddyfile, file("../../../zitadel/developer-login.css")) &&
      strcontains(output.caddyfile, "\thost login.console.staging.example\n\t\tpath /ui/login/resources/themes/zitadel/css/zitadel.css\n\t\tnot query fora-original=1") &&
      strcontains(output.caddyfile, file("../../../zitadel/publisher-login.css"))
    )
    error_message = "Both FORA stylesheets must be embedded and scoped to their respective login hosts, with the original stylesheet still reachable for its import."
  }

  assert {
    condition = (
      length(random_password.console_login_admin) == 1 &&
      output.console_login_admin_password == random_password.console_login_admin[0].result &&
      output.console_login_admin_password != output.zitadel_admin_password &&
      !strcontains(output.user_data, random_password.console_login_admin[0].result)
    )
    error_message = "The publishers' instance's admin must get a password of its own, exposed as an output and never written into the user data or the bundle."
  }

  assert {
    # The address guard's overrides, the proxy-header switch the console does
    # not read, and the edge release, which the image carries.
    condition = (
      alltrue([for name in ["SKIP_SSRF", "ALLOW_INSECURE", "FORA_TRUST_PROXY_HEADERS", "CONSOLE_FETCH_SCHEME", "CONSOLE_EDGE_RELEASE_REF"] : !contains(keys(yamldecode(output.compose_yaml).services.console.environment), name)]) &&
      !strcontains(output.compose_yaml, "SKIP_SSRF") && !strcontains(output.compose_yaml, "ALLOW_INSECURE")
    )
    error_message = "SKIP_SSRF, ALLOW_INSECURE, CONSOLE_FETCH_SCHEME, FORA_TRUST_PROXY_HEADERS and CONSOLE_EDGE_RELEASE_REF must never render for the console: the console reads publishers' own sites."
  }

  assert {
    condition     = strcontains(output.compose_yaml, "  console:\n    image: \"registry.example.com/group/proj/console@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\"\n    restart: unless-stopped\n")
    error_message = "The console must render with the image reference exactly as given — a digest reference has to survive verbatim."
  }

  assert {
    condition = (
      can(regex("^[A-Za-z0-9+/]{43}=$", yamldecode(output.compose_yaml).services.console.environment.CONSOLE_SESSION_KEY)) &&
      yamldecode(output.compose_yaml).services.console.environment.CONSOLE_SESSION_KEY != yamldecode(output.compose_yaml).services.identity.environment.IDENTITY_SESSION_KEY
    )
    error_message = "CONSOLE_SESSION_KEY must be 32 bytes in standard base64 (44 characters), and a key of its own, not the Identity Service's."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.console.healthcheck == { test = ["CMD", "/console", "healthcheck"], interval = "5s", timeout = "3s", retries = 20 } &&
      !can(yamldecode(output.compose_yaml).services.console.ports)
    )
    error_message = "The console must probe itself with its own binary (the image is distroless) and publish no ports: 8085 stays on the compose network."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.console.volumes == ["/opt/fora/keys/console:/keys:ro", "/opt/fora/console-oidc:/console-oidc:ro"] &&
      alltrue(flatten([for name, service in yamldecode(output.compose_yaml).services : [for mount in try(service.volumes, []) : !strcontains(mount, "keys/console") && !strcontains(mount, "console-oidc")] if name != "console"]))
    )
    error_message = "The console must mount its own key directory and its own login-client directory, read-only, and nothing else; no other service may mount either."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.console.depends_on == { postgres = { condition = "service_healthy" }, zitadel = { condition = "service_started" } } &&
      !contains(keys(yamldecode(output.compose_yaml).services.caddy.depends_on), "console")
    )
    error_message = "The console must wait for a healthy PostgreSQL and a started Zitadel, and Caddy must not wait on the console: the console reads the provider's discovery document through Caddy at boot."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_CONSOLE_WELLKNOWN_URL == "https://console.staging.example/.well-known/http-message-signatures-directory" &&
      yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_CATALOG_CONTRIBUTOR_HOST == "catalog-contributor.staging.example"
    )
    error_message = "The Exchange must read the console's key directory at the console's public origin and name the same contributor host as the console."
  }

  assert {
    condition     = strcontains(output.caddyfile, "\nconsole.staging.example {\n\treverse_proxy console:8085\n}\n")
    error_message = "Caddy must proxy every path on the console hostname to console:8085, the key directory included."
  }

  assert {
    condition = (
      length([for f in yamldecode(output.user_data).write_files : f if f.path == "/opt/fora/keys/console/console-signing-key.pem" && f.permissions == "0600" && f.content == var.console_key_fixture]) == 1 &&
      length(regexall("MC4CAQAwBQYDK2VwBCIEIGZha2Ug", output.user_data)) == 1
    )
    error_message = "cloud-init must write the console's key, root-only, to its own directory, and nowhere else."
  }

  assert {
    condition     = strcontains(output.init_sql, "\nCREATE DATABASE console;\n")
    error_message = "Self-hosted PostgreSQL must create the console's database at first cluster init."
  }

  # The seams in the on state: one blank line before and after each block, as
  # around its neighbours.
  assert {
    condition = (
      strcontains(output.compose_yaml, "      EXCHANGE_TERMS_DIGEST: \"sha256:0000000000000000000000000000000000000000000000000000000000000000\"\n      # Turns the publisher console's routes on.") &&
      strcontains(output.compose_yaml, "      EXCHANGE_CATALOG_CONTRIBUTOR_HOST: \"catalog-contributor.staging.example\"\n    ports:\n") &&
      strcontains(output.compose_yaml, "      retries: 3\n\n  # Publisher console: the web application") &&
      strcontains(output.compose_yaml, "      zitadel:\n        condition: service_started\n\n  caddy:\n")
    )
    error_message = "The console's compose blocks must sit between their neighbours with no extra blank line."
  }

  assert {
    condition = (
      strcontains(output.caddyfile, "\trespond 404\n}\n\n# Publisher console.") &&
      strcontains(output.caddyfile, "\treverse_proxy console:8085\n}\n\n# Static Web Bot Auth key directory") &&
      strcontains(output.user_data, "\"public_key\": \"fixture-public\"}\n      \n\n  # The publisher console's signing key") &&
      strcontains(output.user_data, "-----END PRIVATE KEY-----\n      \n  # The importer's schedule lives in systemd")
    )
    error_message = "The console's Caddy site and key file must sit between their neighbours with no extra blank line."
  }

  # The console-off run had the same inputs except the console's own, and the
  # same generated secrets: what differs is the console and nothing else.
  assert {
    condition = (
      setsubtract(keys(yamldecode(output.compose_yaml).services), keys(yamldecode(run.console_off_renders_nothing.compose_yaml).services)) == toset(["console"]) &&
      length(setsubtract(keys(yamldecode(run.console_off_renders_nothing.compose_yaml).services), keys(yamldecode(output.compose_yaml).services))) == 0 &&
      alltrue([for name, service in yamldecode(run.console_off_renders_nothing.compose_yaml).services : contains(["exchange", "zitadel"], name) || yamldecode(output.compose_yaml).services[name] == service]) &&
      yamldecode(output.compose_yaml).volumes == yamldecode(run.console_off_renders_nothing.compose_yaml).volumes &&
      yamldecode(output.compose_yaml).networks == yamldecode(run.console_off_renders_nothing.compose_yaml).networks
    )
    error_message = "Turning the console on must add the console service and change no other service, volume or network (the Exchange and Zitadel are checked below)."
  }

  assert {
    condition = (
      { for name, value in yamldecode(output.compose_yaml).services.zitadel.environment : name => value if name != "ZITADEL_SYSTEMAPIUSERS" } == yamldecode(run.console_off_renders_nothing.compose_yaml).services.zitadel.environment &&
      { for name, value in yamldecode(output.compose_yaml).services.zitadel : name => value if name != "environment" } == { for name, value in yamldecode(run.console_off_renders_nothing.compose_yaml).services.zitadel : name => value if name != "environment" }
    )
    error_message = "Turning the console on must add exactly the System API user to Zitadel and change nothing else about it."
  }

  assert {
    condition = (
      setsubtract(keys(yamldecode(output.compose_yaml).services.exchange.environment), keys(yamldecode(run.console_off_renders_nothing.compose_yaml).services.exchange.environment)) == toset(["EXCHANGE_CONSOLE_WELLKNOWN_URL", "EXCHANGE_CATALOG_CONTRIBUTOR_HOST"]) &&
      { for name, value in yamldecode(output.compose_yaml).services.exchange.environment : name => value if !contains(["EXCHANGE_CONSOLE_WELLKNOWN_URL", "EXCHANGE_CATALOG_CONTRIBUTOR_HOST"], name) } == yamldecode(run.console_off_renders_nothing.compose_yaml).services.exchange.environment &&
      { for name, value in yamldecode(output.compose_yaml).services.exchange : name => value if name != "environment" } == { for name, value in yamldecode(run.console_off_renders_nothing.compose_yaml).services.exchange : name => value if name != "environment" }
    )
    error_message = "Turning the console on must add exactly the two console settings to the Exchange and change nothing else about it."
  }

  assert {
    condition = (
      replace(replace(replace(output.caddyfile_without_comments, "/(?s)\\t@publisher_css \\{.*?\\nFORA_CSS\\n/", ""), "\nconsole.staging.example {\n\treverse_proxy console:8085\n}\n", ""),
        "\nlogin.staging.example, login.console.staging.example {\n${var.system_api_block}",
      "\nlogin.staging.example {\n") == run.console_off_renders_nothing.caddyfile_without_comments &&
      output.init_sql == replace(run.console_off_renders_nothing.init_sql, "CREATE DATABASE sor;\n", "CREATE DATABASE sor;\n\nCREATE DATABASE console;\n") &&
      setsubtract([for f in yamldecode(output.user_data).write_files : f.path], [for f in yamldecode(run.console_off_renders_nothing.user_data).write_files : f.path]) == toset(["/opt/fora/keys/console/console-signing-key.pem"]) &&
      length(setsubtract([for f in yamldecode(run.console_off_renders_nothing.user_data).write_files : f.path], [for f in yamldecode(output.user_data).write_files : f.path])) == 0 &&
      yamldecode(output.user_data).runcmd == yamldecode(run.console_off_renders_nothing.user_data).runcmd
    )
    error_message = "Turning the console on must add its Caddy site, the publishers' hostname, stylesheet and System API block to the Zitadel site, its database and its key file, and change nothing else in the Caddyfile, the init SQL or cloud-init."
  }
}

run "console_on_external_postgres" {
  command = apply

  # The importer is off, so the contributor may be one that runs elsewhere.
  variables {
    external_postgres = {
      host   = "db.staging.example"
      port   = 5543
      ca_pem = "-----BEGIN CERTIFICATE-----\ndGVzdA==\n-----END CERTIFICATE-----\n"
    }
    importer_key_json        = null
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.other.example"
    console_support_email    = "help@staging.example"
    console_default_rate     = "0.25"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  assert {
    condition = (
      toset(keys(jsondecode(output.database_config_json).databases)) == toset(["fora", "sor", "identity", "zitadel", "console"]) &&
      jsondecode(output.database_config_json).databases.console.username == "console" &&
      length(distinct([for credentials in values(jsondecode(output.database_config_json).databases) : credentials.password])) == 5 &&
      contains(keys(random_password.database), "console")
    )
    error_message = "External PostgreSQL must list five databases, the console's with an owner named console and a password of its own."
  }

  assert {
    condition     = yamldecode(output.compose_yaml).services.console.environment.CONSOLE_DSN == "postgres://console:${jsondecode(output.database_config_json).databases.console.password}@db.staging.example:5543/console?sslmode=verify-full&sslrootcert=/db-ca/ca.pem"
    error_message = "The console must connect as its own owner, with verified TLS against the mounted CA."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.console.volumes == ["/opt/fora/keys/console:/keys:ro", "/opt/fora/console-oidc:/console-oidc:ro", "/opt/fora/database/ca.pem:/db-ca/ca.pem:ro"] &&
      yamldecode(output.compose_yaml).services.console.depends_on == { zitadel = { condition = "service_started" } }
    )
    error_message = "On external PostgreSQL the console must also mount the CA file, and must not depend on a local database service."
  }

  assert {
    condition = (
      yamldecode(output.compose_yaml).services.console.environment.CONSOLE_DEFAULT_RATE == "0.25" &&
      yamldecode(output.compose_yaml).services.console.environment.CONSOLE_CONTRIBUTOR_HOST == "catalog-contributor.other.example" &&
      yamldecode(output.compose_yaml).services.exchange.environment.EXCHANGE_CATALOG_CONTRIBUTOR_HOST == "catalog-contributor.other.example"
    )
    error_message = "A given default rate must reach the console, and the contributor host must reach both services from the one input."
  }

  assert {
    condition     = yamldecode(output.compose_yaml).services.console.environment.CONSOLE_SESSION_KEY == yamldecode(run.console_renders_when_configured.compose_yaml).services.console.environment.CONSOLE_SESSION_KEY
    error_message = "The session key must be kept in state across applies: a new value signs every publisher out."
  }
}

# ── Inputs the plan refuses ──────────────────────────────────────────────────
# Each run below is otherwise a valid console configuration, so the expected
# failure is the only one.

run "console_hostname_without_image_fails_the_plan" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "console_hostname_without_signing_key_fails_the_plan" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "console_hostname_without_contributor_host_fails_the_plan" {
  command = plan

  # The importer is off, so only the pairing rule can fail this plan.
  variables {
    importer_key_json       = null
    console_hostname        = "console.staging.example"
    console_image           = var.console_image_fixture
    console_signing_key_pem = var.console_key_fixture
    console_support_email   = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "console_hostname_without_support_email_fails_the_plan" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "console_inputs_without_hostname_fail_the_plan" {
  command = plan

  variables {
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "console_without_terms_currency_fails_the_plan" {
  command = plan

  variables {
    exchange_terms_currency  = null
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "console_without_terms_unit_fails_the_plan" {
  command = plan

  variables {
    exchange_terms_unit      = null
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "contributor_other_than_the_importer_kid_fails_the_plan" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.other.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [random_password.postgres]
}

run "default_rate_without_console_fails_the_plan" {
  command = plan

  variables {
    console_default_rate = "0.25"
  }

  expect_failures = [random_password.postgres]
}

run "broker_format_signing_key_is_rejected" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = "-----BEGIN ED25519 PRIVATE KEY-----\nZmFrZSBicm9rZXIgaWRlbnRpdHkgZml4dHVyZTogZXhhY3RseSBzaXh0eS1mb3VyIGJ5dGVzIGZvciB0ZnRlcw==\n-----END ED25519 PRIVATE KEY-----\n"
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_signing_key_pem]
}

run "rsa_signing_key_is_rejected" {
  command = plan

  # A PKCS#8 RSA key has the same label as the Ed25519 one; its body starts
  # with another algorithm identifier and runs far past 64 characters.
  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7ZmFrZSByc2Eg\nZml4dHVyZSBmb3IgdGZ0ZXN0IG9ubHksIG5vdCBhIGtleSBhbnlvbmUgdXNlcy4=\n-----END PRIVATE KEY-----\n"
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_signing_key_pem]
}

run "pkcs1_rsa_signing_key_is_rejected" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = "-----BEGIN RSA PRIVATE KEY-----\ndGVzdA==\n-----END RSA PRIVATE KEY-----\n"
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_signing_key_pem]
}

run "truncated_signing_key_is_rejected" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIGZha2UgY29uc29sZSBzaWduaW5n\n-----END PRIVATE KEY-----\n"
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_signing_key_pem]
}

run "console_hostname_with_a_scheme_is_rejected" {
  command = plan

  variables {
    console_hostname         = "https://console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_hostname]
}

run "uppercase_contributor_host_is_rejected" {
  command = plan

  variables {
    importer_key_json        = null
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "Catalog-Contributor.staging.example"
    console_support_email    = "help@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_contributor_host]
}

run "support_email_with_a_line_break_is_rejected" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example\nBcc: someone@staging.example"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_support_email]
}

run "lowercase_terms_currency_is_rejected" {
  command = plan

  variables {
    exchange_terms_currency = "eur"
  }

  expect_failures = [var.exchange_terms_currency]
}

run "terms_unit_with_a_quote_is_rejected" {
  command = plan

  variables {
    exchange_terms_unit = "fetches\""
  }

  expect_failures = [var.exchange_terms_unit]
}

run "zero_default_rate_is_rejected" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"
    console_default_rate     = "0.00"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_default_rate]
}

run "default_rate_in_exponent_form_is_rejected" {
  command = plan

  variables {
    console_hostname         = "console.staging.example"
    console_image            = var.console_image_fixture
    console_signing_key_pem  = var.console_key_fixture
    console_contributor_host = "catalog-contributor.staging.example"
    console_support_email    = "help@staging.example"
    console_default_rate     = "1e-2"

    console_login_hostname        = var.console_login_fixture
    zitadel_system_api_public_key = var.system_api_key_fixture
  }

  expect_failures = [var.console_default_rate]
}
