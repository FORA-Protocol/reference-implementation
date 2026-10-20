# Provider-neutral templating: renders the Docker Compose bundle, Caddyfile,
# Postgres init SQL, and the cloud-init user data that delivers them to a VM.
#
# The rendered compose file is the reference bundle for an own-DC deployment,
# and staging runs the exact artifact we hand over. Note that it now carries
# the whole agent-identity plane (the Identity Service, its Vault, and a
# Zitadel) alongside the publisher-facing services. A publisher operating only
# an Exchange and Broker does not need those three; splitting the bundle in two
# is future work, not something a caller can switch off today.

resource "random_password" "postgres" {
  # Alphanumeric only: the password is embedded in postgres:// DSNs, so it
  # must stay URL-safe without encoding.
  length  = 32
  special = false

  lifecycle {
    # Cross-variable pairing rules live here because preconditions must hang
    # off a resource, and this one is created unconditionally. A `check` block
    # would read nicer but only WARNS — it cannot fail the plan, so it cannot
    # gate.

    # Pairing rule for the demo publisher: origin_hostname gates the
    # service, publisher_image names what it runs. One without the other
    # would render an empty `image:` and fail confusingly at first boot —
    # fail the plan instead.
    precondition {
      condition     = (var.origin_hostname == null) == (var.publisher_image == null)
      error_message = "origin_hostname and publisher_image must be set together (or both left null)."
    }

    # Same pairing rule for the onboarding preview service, and the same
    # failure otherwise: an empty `image:` at first boot.
    precondition {
      condition     = (var.onboarding_hostname == null) == (var.onboarding_image == null)
      error_message = "onboarding_hostname and onboarding_image must be set together (or both left null)."
    }

    # A browser refuses a cross-origin call the service has not allowed, so an
    # enabled service with no origins is one the public page cannot use.
    precondition {
      condition     = var.onboarding_hostname == null || length(var.onboarding_allowed_origins) > 0
      error_message = "onboarding_allowed_origins must name at least one website origin when onboarding_hostname is set."
    }

    # The onboarding service refuses to start on a partial contact
    # configuration: recipients, a from address and a relay are one setting.
    # Recipients and from are paired here; the relay is the bundle's smtp_*
    # inputs, so contact settings without a relay — or without the service
    # that would send them — fail the plan instead of the first boot.
    precondition {
      condition = (
        (length(var.onboarding_contact_recipients) == 0) == (var.onboarding_contact_from == null) &&
        (length(var.onboarding_contact_recipients) == 0 || (var.smtp_host != null && var.onboarding_hostname != null))
      )
      error_message = "onboarding_contact_recipients and onboarding_contact_from must be set together, and only alongside onboarding_hostname and the smtp_* relay inputs."
    }

    # docker login needs all three registry settings. A server without
    # credentials would fail the template render with an opaque null-value
    # error; credentials without a server would silently skip the login and
    # the VM could never pull private images. Fail the plan clearly instead.
    precondition {
      condition = (
        (var.registry_server == null) == (var.registry_username == null) &&
        (var.registry_server == null) == (var.registry_password == null)
      )
      error_message = "registry_server, registry_username, and registry_password must be set together (or all left null)."
    }

    # Outbound mail needs all four together. Every relay this bundle is
    # deployed against authenticates, and Zitadel refuses to store a provider
    # with no sender address. A partial set renders a provider that fails on
    # its first send — which happens when a developer first tries to register,
    # not at apply time, and by then correcting it means rebuilding the VM.
    precondition {
      condition = (
        (var.smtp_host == null) == (var.smtp_user == null) &&
        (var.smtp_host == null) == (var.smtp_password == null) &&
        (var.smtp_host == null) == (var.smtp_from == null)
      )
      error_message = "smtp_host, smtp_user, smtp_password, and smtp_from must be set together (or all left null)."
    }

    # Each of acme_staging and acme_ca chooses the CA, and EAB credentials are
    # for a chosen CA only: Caddy's default issuers need none.
    precondition {
      condition     = !(var.acme_staging && var.acme_ca != null)
      error_message = "acme_ca and acme_staging cannot both be set: each chooses the CA."
    }

    precondition {
      condition     = (var.acme_eab_key_id == null) == (var.acme_eab_mac_key == null) && (var.acme_eab_key_id == null || var.acme_ca != null)
      error_message = "acme_eab_key_id and acme_eab_mac_key must be set together, and only with acme_ca."
    }

    # A dev-mode Vault pre-enables exactly one KV engine, `secret/`, and
    # nothing in this bundle enables another. Pointing the Identity Service
    # at a different mount there would boot cleanly and fail on the first
    # key write — fail the plan instead.
    precondition {
      condition     = var.vault_mode == "server" || var.vault_kv_mount == "secret"
      error_message = "vault_kv_mount can only be \"secret\" while vault_mode is \"dev\": a dev-mode Vault has no other KV engine."
    }

    # The publisher console's inputs are one setting. The console cannot run
    # without its image or its signing key. Without a contributor host it
    # hands every publisher a file the Exchange will not confirm, and without
    # a support address the publisher it cannot help has nobody to write to.
    # Publishers sign in through an instance of their own, which needs its
    # address and, to be created at all, the System API key.
    precondition {
      condition = (
        (var.console_hostname == null) == (var.console_image == null) &&
        (var.console_hostname == null) == (var.console_signing_key_pem == null) &&
        (var.console_hostname == null) == (var.console_contributor_host == null) &&
        (var.console_hostname == null) == (var.console_support_email == null) &&
        (var.console_hostname == null) == (var.console_login_hostname == null) &&
        (var.console_hostname == null) == (var.zitadel_system_api_public_key == null)
      )
      error_message = "console_hostname, console_image, console_signing_key_pem, console_contributor_host, console_support_email, console_login_hostname and zitadel_system_api_public_key must be set together (or all left null)."
    }

    # Zitadel tells its instances apart by the hostname a request names, so
    # the publishers' instance needs an address of its own.
    precondition {
      condition     = var.console_login_hostname == null || !contains([var.zitadel_hostname, var.console_hostname], var.console_login_hostname)
      error_message = "console_login_hostname must differ from zitadel_hostname and console_hostname: Zitadel routes each instance by its own hostname."
    }

    precondition {
      condition     = var.zitadel_system_api_recovery_instance_id == null || var.zitadel_system_api_public_key != null
      error_message = "zitadel_system_api_recovery_instance_id needs zitadel_system_api_public_key: the recovery membership belongs to the System API user."
    }

    # The console prices every publisher's terms in the Exchange's
    # denomination. Unset, both services fall back to a default, and the
    # Exchange does not check at boot that the currency is one it bills in,
    # so a mismatch shows up only when publishers register or agents buy.
    # With the console on, the operator states both values.
    precondition {
      condition     = var.console_hostname == null || (var.exchange_terms_currency != null && var.exchange_terms_unit != null)
      error_message = "exchange_terms_currency and exchange_terms_unit must be set when console_hostname is set: the console prices publishers' terms in them."
    }

    # The contributor the console names in every publisher's file must be the
    # one whose key signs this bundle's imports, or the Exchange refuses every
    # import push. With the importer off, the contributor runs elsewhere.
    precondition {
      condition     = (!local.importer_enabled || !local.console_enabled) ? true : var.console_contributor_host == nonsensitive(jsondecode(var.importer_key_json).kid)
      error_message = "console_contributor_host must equal the kid of importer_key_json: the Exchange accepts catalog pushes only from the contributor the publisher's file names."
    }

    precondition {
      condition     = var.console_default_rate == null || var.console_hostname != null
      error_message = "console_default_rate is a console setting: set it only together with console_hostname."
    }
  }
}

# ── Identity-plane secrets ───────────────────────────────────────────────────
# Generated here rather than taken as inputs: nothing outside this bundle ever
# needs to know them, and an operator-supplied value would only be one more
# secret to store. They land in Terraform state like the Postgres password.
#
# They also land in the rendered cloud-init, which the caller delivers as EC2
# user data — readable by anyone holding ec2:DescribeInstanceAttribute in the
# account. Two of them are worth more than a database password: the Vault root
# token unlocks every agent signing key the service custodies, and the Zitadel
# admin password controls the provider that gates developer sign-up. Together
# they are enough to sign FORA requests as any agent registered on the
# instance.
#
# Accepted for staging — throwaway VM, test identities — and NOT acceptable
# for production, where delivery has to move to a secrets store fetched at
# boot with an instance role. Adding a secret here puts it on the same
# footing, so weigh it against that before reaching for one. The full
# statement of the trade-off is in the README's "Secrets — read this".

resource "random_password" "zitadel_db" {
  # Alphanumeric for the same reason as the Postgres password: it travels in
  # connection settings that are not URL-escaped.
  length  = 32
  special = false
}

resource "random_password" "vault_root_token" {
  length  = 32
  special = false
}

resource "random_password" "zitadel_masterkey" {
  # Zitadel rejects a masterkey that is not EXACTLY 32 characters.
  length  = 32
  special = false
}

resource "random_password" "zitadel_admin" {
  # Console login for the operator (username zadmin). Zitadel's default
  # password policy wants all four character classes. The symbol set excludes
  # quotes, backslash and $ so the value stays safe to embed in the rendered
  # YAML without escaping.
  length           = 24
  min_upper        = 1
  min_lower        = 1
  min_numeric      = 1
  min_special      = 1
  override_special = "!@#%^&*-_+="
}

resource "random_bytes" "identity_session_key" {
  # Cookie encryption key for the sign-up flow: 32 raw bytes, base64 to env.
  length = 32
}

resource "random_bytes" "identity_token_signing_key" {
  # Ed25519 seed the service signs its own bearer tokens with. Generated once
  # and held in state on purpose: if it changed per boot, every bearer the
  # service ever issued would be rejected after a restart.
  length = 32
}

locals {
  # Static compose-network addressing: EXCHANGE_BILLING_TB_ADDRESS rejects
  # hostnames (IP:port only), so TigerBeetle gets a fixed address — host 10
  # on the (overridable) IPAM subnet.
  tigerbeetle_ip = cidrhost(var.network_subnet, 10)

  # The name MCP clients connect to. Unset, it is the Identity hostname and
  # the templates render exactly as they did before the option existed; set
  # to something else, the compose file pins the token audience to it and the
  # Caddyfile gains a site for it. A value equal to identity_hostname is the
  # default spelled out, and renders identically.
  mcp_hostname = coalesce(var.mcp_hostname, var.identity_hostname)
  mcp_split    = local.mcp_hostname != var.identity_hostname

  # Two databases beyond the shared `fora` one, both prepended rather than left
  # to the caller: each belongs to a service that is an unconditional part of
  # the bundle, so a stack that forgot to list it would boot a container that
  # crash-loops on a missing database.
  #
  #   identity  The Identity Service's own store (its migrations create an
  #             `identity` schema in it).
  #   sor       The Exchange's System of Record. EXCHANGE_SOR_DSN opens a
  #             SECOND pool distinct from EXCHANGE_DSN, and the Exchange
  #             applies the SoR migrations against it at boot but never
  #             CREATE DATABASEs — so the database has to exist first.
  #
  # A third joins them while the publisher console is on:
  #
  #   console   The console's own store. Its migrations create a `console`
  #             schema in it at boot; it never creates the database either.
  databases = concat(["identity", "sor"], local.console_enabled ? ["console"] : [], var.extra_databases)

  tigerbeetle_enabled = var.billing_adapter == "tigerbeetle"
  publisher_enabled   = var.origin_hostname != null
  onboarding_enabled  = var.onboarding_hostname != null
  # The previewed offer names this bundle's own Exchange unless told otherwise.
  onboarding_exchange_domain = coalesce(var.onboarding_exchange_domain, var.exchange_hostname)
  terms_enabled              = length(var.exchange_terms_documents) > 0

  # The pre-signed static key directories, decoded once. The variable's
  # validation has already checked every member, so nothing here re-checks.
  static_wba_signed = { for host, doc in var.static_wba_directories : host => jsondecode(doc) }
  # The served body of each directory, by its path under /opt/fora: written
  # there by this module's cloud-init, or by a stack that delivers the bundle
  # another way and takes them from the static_wba_files output.
  static_wba_files = { for host, signed in local.static_wba_signed : "wba/${host}.json" => signed.body }

  # Where cloud-init mounts caddy_data_volume_id, and so the host side of
  # Caddy's /data bind mount; null keeps /data in a named Docker volume.
  caddy_data_dir = var.caddy_data_volume_id == null ? null : "/var/lib/fora-caddy"

  # The registration schema crosses TWO encoding layers before it reaches the
  # Exchange, and each needs a different escape.
  #
  # jsonencode renders the schema as a quoted, escaped scalar so the quotes a
  # JSON Schema is full of survive YAML parsing.
  #
  # Doubling the dollars is Compose's own escape, and it is the layer that is
  # easy to miss: docker compose interpolates $VAR in the compose file itself,
  # and a JSON Schema opens with "$schema". Left unescaped, Compose warns that
  # `schema` is not set, substitutes an empty string, and the Exchange receives
  # a schema whose first key is "" — silent corruption of the one value the
  # service refuses to boot without understanding. No other value in this
  # bundle needs it: every generated secret avoids $ by construction (special
  # = false, and the override_special set above omits it), so the schema is the
  # first value here that must carry one.
  exchange_registration_schema_env = (
    var.exchange_registration_schema == null
    ? null
    : replace(jsonencode(var.exchange_registration_schema), "$", "$$")
  )

  # Whether a key was given is not the secret, and the units rendered from
  # that fact carry none: without nonsensitive() the comparison would inherit
  # the variable's sensitivity and taint the importer_units output with it.
  importer_enabled = nonsensitive(var.importer_key_json != null)
  # The importer's systemd units, rendered once and written by whichever
  # cloud-init delivers the host: this module's own below, or a stack that
  # ships the bundle another way and takes them from the importer_units output.
  importer_units = local.importer_enabled ? {
    "fora-import.service" = file("${path.module}/templates/fora-import.service")
    "fora-import.timer" = templatefile("${path.module}/templates/fora-import.timer.tftpl", {
      importer_schedule = var.importer_schedule
    })
  } : {}

  compose_yaml = templatefile("${path.module}/templates/docker-compose.yml.tftpl", {
    exchange_hostname = var.exchange_hostname
    broker_hostname   = var.broker_hostname
    identity_hostname = var.identity_hostname
    mcp_hostname      = local.mcp_hostname
    mcp_split         = local.mcp_split
    zitadel_hostname  = var.zitadel_hostname
    exchange_image    = var.exchange_image
    broker_image      = var.broker_image
    identity_image    = var.identity_image
    publisher_image   = var.publisher_image
    publisher_enabled = local.publisher_enabled
    postgres_password = random_password.postgres.result
    external_database = local.external_database
    database_host     = local.database_host
    database_port     = local.database_port
    database_dsns     = local.database_dsns

    # The onboarding block is gated by one flag; the precondition above
    # guarantees the image is set whenever the hostname is. The relay for its
    # contact form is the same smtp_* trio Zitadel uses, joined by the
    # precondition that forbids contact settings without one.
    onboarding_enabled            = local.onboarding_enabled
    onboarding_image              = var.onboarding_image
    onboarding_allowed_origins    = join(",", var.onboarding_allowed_origins)
    onboarding_exchange_domain    = local.onboarding_exchange_domain
    onboarding_exchange_endpoint  = coalesce(var.onboarding_exchange_endpoint, "https://${local.onboarding_exchange_domain}")
    onboarding_contact_enabled    = length(var.onboarding_contact_recipients) > 0
    onboarding_contact_recipients = join(",", var.onboarding_contact_recipients)
    onboarding_contact_from       = var.onboarding_contact_from

    # The console block, and the two Exchange settings that trust it, are
    # gated by one flag; the preconditions above guarantee the other console
    # inputs are set whenever the hostname is. The contributor host is passed
    # once and rendered into both services, so the two cannot disagree.
    console_enabled          = local.console_enabled
    console_origin           = local.console_origin
    console_image            = var.console_image
    console_session_key      = one(random_bytes.console_session_key[*].base64)
    console_contributor_host = var.console_contributor_host
    console_support_email    = var.console_support_email
    console_default_rate     = var.console_default_rate
    console_login_url        = local.console_login_url

    zitadel_system_api_users   = local.zitadel_system_api_users
    zitadel_db_password        = random_password.zitadel_db.result
    zitadel_masterkey          = random_password.zitadel_masterkey.result
    vault_root_token           = random_password.vault_root_token.result
    identity_session_key       = random_bytes.identity_session_key.base64
    identity_token_signing_key = random_bytes.identity_token_signing_key.base64
    billing_adapter            = var.billing_adapter
    billing_ledger             = join(",", var.billing_ledger)
    tigerbeetle_enabled        = local.tigerbeetle_enabled
    tigerbeetle_ip             = local.tigerbeetle_ip
    tigerbeetle_development    = var.tigerbeetle_development
    tigerbeetle_cache_grid     = var.tigerbeetle_cache_grid
    # No agent address: in server mode the Identity Service reaches the Vault
    # Agent over a Unix socket in a shared volume, never over TCP.
    vault_mode            = var.vault_mode
    vault_kv_mount        = var.vault_kv_mount
    network_subnet        = var.network_subnet
    broker_id             = var.broker_id
    default_tenant_domain = var.default_tenant_domain
    default_agent_credit  = var.default_agent_credit
    exa_api_key           = var.exa_api_key
    query_discovery       = var.query_discovery
    rsa_enabled           = var.rsa_private_pem != null
    wba_enabled           = length(var.static_wba_directories) > 0
    terms_enabled         = local.terms_enabled
    importer_enabled      = local.importer_enabled
    caddy_data_dir        = local.caddy_data_dir
    acme_eab_key_id       = var.acme_eab_key_id
    acme_eab_mac_key      = var.acme_eab_mac_key

    registration_schema_env = local.exchange_registration_schema_env
    terms_uri               = var.exchange_terms_uri
    terms_digest            = var.exchange_terms_digest
    terms_currency          = var.exchange_terms_currency
    terms_unit              = var.exchange_terms_unit

    # The precondition above guarantees the other three are set whenever the
    # host is, so one flag gates the whole block. The sender name is the only
    # optional member: Zitadel wants a non-empty string, so a null becomes a
    # neutral default rather than an empty display name.
    smtp_enabled   = var.smtp_host != null
    smtp_host      = var.smtp_host
    smtp_user      = var.smtp_user
    smtp_password  = var.smtp_password
    smtp_from      = var.smtp_from
    smtp_from_name = coalesce(var.smtp_from_name, "FORA")
  })

  caddyfile = templatefile("${path.module}/templates/Caddyfile.tftpl", {
    legal_path          = var.exchange_legal_path
    terms_enabled       = local.terms_enabled
    exchange_hostname   = var.exchange_hostname
    broker_hostname     = var.broker_hostname
    identity_hostname   = var.identity_hostname
    mcp_hostname        = local.mcp_hostname
    mcp_split           = local.mcp_split
    zitadel_hostname    = var.zitadel_hostname
    origin_hostname     = var.origin_hostname
    onboarding_hostname = var.onboarding_hostname
    console_hostname    = var.console_hostname
    acme_email          = var.acme_email
    acme_staging        = var.acme_staging
    acme_ca             = var.acme_ca
    acme_eab            = var.acme_eab_key_id != null
    # The publishers' instance shares the Zitadel site, and with it the
    # System API block, which renders whenever there is a System API user.
    console_login_hostname = var.console_login_hostname
    developer_login_css    = file("${path.module}/../../../zitadel/developer-login.css")
    publisher_login_css    = file("${path.module}/../../../zitadel/publisher-login.css")
    system_api_enabled     = var.zitadel_system_api_public_key != null
    # Each hostname and its three response-signature header values reach the
    # Caddyfile, and the variable's validation holds every one of them to a
    # strict shape at plan time: the hostname to a DNS name, the headers to
    # the structured-field forms the SDKs emit. The body is NOT passed here:
    # cloud-init writes it to /opt/fora/wba (base64-encoded in transit) and
    # Caddy serves it as a file, so no document byte is ever interpolated into
    # proxy configuration.
    static_wba_sites = {
      for host, signed in local.static_wba_signed : host => {
        content_digest  = signed.content_digest
        signature_input = signed.signature_input
        signature       = signed.signature
      }
    }
  })

  init_sql = templatefile("${path.module}/templates/exchange-dbs.sql.tftpl", {
    extra_databases = local.databases
  })

  zitadel_init_steps = templatefile("${path.module}/templates/zitadel-init-steps.yaml.tftpl", {
    zitadel_admin_password = random_password.zitadel_admin.result
  })

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    compose_yaml            = local.compose_yaml
    caddyfile               = local.caddyfile
    init_sql                = local.init_sql
    external_database       = local.external_database
    database_config_json    = local.database_config_json
    database_ca_pem         = local.external_database ? var.external_postgres.ca_pem : ""
    zitadel_init_steps      = local.zitadel_init_steps
    ed25519_private_pem     = var.ed25519_private_pem
    rsa_private_pem         = var.rsa_private_pem
    broker_relay_key_json   = var.broker_relay_key_json
    broker_identity_key_pem = var.broker_identity_key_pem
    static_wba_files        = local.static_wba_files
    query_discovery         = var.query_discovery
    terms_documents         = var.exchange_terms_documents
    importer_key_json       = var.importer_key_json
    importer_units          = local.importer_units
    console_signing_key_pem = var.console_signing_key_pem
    registry_server         = var.registry_server
    registry_username       = var.registry_username
    registry_password       = var.registry_password
    caddy_data_volume_id    = var.caddy_data_volume_id
    caddy_data_dir          = local.caddy_data_dir
  })

  # A full-line comment in a rendered document: a line whose first non-blank
  # character is #. Every *_without_comments value below removes exactly these
  # lines, with this one pattern, so a stack that ships a size-capped copy gets
  # the same result whichever document it ships. No rendered document has data
  # on such a line: the templates carry no YAML block scalars or inline
  # comments, and key material and documents are base64.
  comment_line = "/(?m)^[ \\t]*#.*\\n/"

  # The same user data without its full-line comments: the ones in this
  # module's templates and in every file embedded from them (the compose file,
  # the Caddyfile, Zitadel's steps). They are most of the document, and EC2
  # caps user data at 16 KB even after gzip. The #cloud-config header that
  # cloud-init needs is put back first.
  user_data_without_comments = "#cloud-config\n${replace(trimprefix(local.user_data, "#cloud-config\n"), local.comment_line, "")}"

  # The compose file and the Caddyfile without their full-line comments, for a
  # stack that delivers them outside cloud-init in a size-capped store.
  compose_yaml_without_comments = replace(local.compose_yaml, local.comment_line, "")
  caddyfile_without_comments    = replace(local.caddyfile, local.comment_line, "")
}
