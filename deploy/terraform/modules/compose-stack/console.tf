# ── Publisher console (optional) ─────────────────────────────────────────────
# The web application a publisher signs in to, registers their business and
# sites through, and downloads their setup files from. Nothing in this file
# renders until console_hostname is set. The preconditions in main.tf then
# require the console's other inputs and the Exchange's terms denomination.
#
# Publishers sign in through a Zitadel instance of their own, so their
# accounts, browser session and sign-in pages are never the MCP developers'.
# It is a second instance of the bundle's one Zitadel, created through
# Zitadel's System API (deploy/zitadel/bootstrap/instance.sh) and answering at
# console_login_hostname, which Caddy serves from the Zitadel site.
#
# The console reaches the login provider and the Exchange at their public
# hostnames, through Caddy, the way the Identity Service reaches the provider.
# The Exchange fetches the console's key directory through its address guard,
# which refuses private addresses, so console_hostname needs a public DNS
# record that points at this VM. The console's own guard stays on: SKIP_SSRF,
# ALLOW_INSECURE and CONSOLE_FETCH_SCHEME are not inputs of this module.

variable "console_hostname" {
  description = "Optional public full domain name the publisher console answers on. Null (the default) renders no console. Set it together with console_image, console_signing_key_pem, console_contributor_host and console_support_email, and set exchange_terms_currency and exchange_terms_unit with it. The name needs a DNS record that points at this VM: the Exchange fetches the console's key directory from https://<console_hostname>/.well-known/http-message-signatures-directory and refuses a private address. Turning the console on adds a `console` database. On self-hosted PostgreSQL the init SQL creates it, and that SQL runs only when the cluster is first initialised: on a data volume that already holds a cluster, create the database by hand."
  type        = string
  default     = null

  validation {
    # Interpolated verbatim into a Caddy site address and into URLs in the
    # compose file. Same restriction as the static_wba_directories hostnames.
    condition     = var.console_hostname == null || can(regex(local.hostname_pattern, var.console_hostname))
    error_message = "console_hostname must be a lowercase DNS hostname (labels of letters, digits, and inner hyphens, joined by dots), with no scheme, port or path."
  }
}

variable "console_image" {
  description = "Full publisher console image reference, by tag or by digest (built from src/console/Dockerfile). Required when console_hostname is set."
  type        = string
  default     = null
}

variable "console_signing_key_pem" {
  description = "The console's own Ed25519 signing key: a PKCS#8 PEM, the format `openssl genpkey -algorithm ed25519` writes. The console signs every call to the Exchange with it and publishes the public half in its key directory, so it is a key of its own, never another service's. Delivered to the VM as /opt/fora/keys/console/console-signing-key.pem and mounted only into the console service. Required when console_hostname is set."
  type        = string
  default     = null
  sensitive   = true

  validation {
    # The console's loader accepts only PKCS#8 Ed25519. That DER is 48 bytes,
    # so the PEM body is 64 base64 characters, and its first 21 characters
    # encode the fixed header in front of the 32-byte seed. Checking the whole
    # shape refuses, at plan time, the three likely wrong files: the Broker's
    # raw-format key (another label and a longer body), an RSA key, and a
    # truncated paste. Each of them would otherwise crash-loop the console.
    #
    # The PEM label is passed to format() rather than written into the
    # pattern, so this file never holds a PEM header and its footer as literal
    # text: that pair is what the published-tree secret scan matches, whatever
    # lies between them.
    condition     = var.console_signing_key_pem == null || can(regex(format("^-----BEGIN %[1]s-----\\nMC4CAQAwBQYDK2VwBCIEI[A-Za-z0-9+/]{43}\\n-----END %[1]s-----\\n?$", "PRIVATE KEY"), var.console_signing_key_pem))
    error_message = "console_signing_key_pem must be a PKCS#8 Ed25519 private key: one PEM block labelled PRIVATE KEY, with a 64-character body, as `openssl genpkey -algorithm ed25519` writes it (gen-keys.sh uses that command for its PKCS#8 Ed25519 keys). The Broker's ED25519 PRIVATE KEY file, an RSA key and a truncated paste are refused: the console does not start with any of them."
  }
}

variable "console_contributor_host" {
  description = "Hostname of the catalog contributor this deployment operates. The console writes it into every publisher's fora.json (CONSOLE_CONTRIBUTOR_HOST), and the Exchange refuses to confirm a site whose file does not name it (EXCHANGE_CATALOG_CONTRIBUTOR_HOST). Both services get this one value. When this bundle runs the importer (importer_key_json), it must be the importer key's kid; with the importer off it may name a contributor that runs elsewhere. Required when console_hostname is set."
  type        = string
  default     = null

  validation {
    condition     = var.console_contributor_host == null || can(regex(local.hostname_pattern, var.console_contributor_host))
    error_message = "console_contributor_host must be a lowercase DNS hostname (labels of letters, digits, and inner hyphens, joined by dots): it is the host whose key directory signs catalog pushes."
  }
}

variable "console_support_email" {
  description = "The address the console shows a publisher whose network it cannot set up for them (CONSOLE_SUPPORT_EMAIL). That screen is a dead end by design, so the console needs someone to send the publisher to. Required when console_hostname is set."
  type        = string
  default     = null

  validation {
    # Same shape check as onboarding_contact_from: one address, no whitespace
    # or line break in a value interpolated into the compose file.
    condition     = var.console_support_email == null ? true : can(regex("^[^@\\s]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$", var.console_support_email))
    error_message = "console_support_email must be a single email address with no whitespace or line breaks."
  }
}

variable "console_default_rate" {
  description = "Optional CONSOLE_DEFAULT_RATE: the price the console fills in when a publisher chooses to charge for each access, in exchange_terms_currency per exchange_terms_unit. A decimal number greater than zero, written the way the protocol writes money (digits, an optional fractional part, at most 32 characters). Null (the default) renders nothing, and the console uses its own default, 0.01. Only valid with console_hostname."
  type        = string
  default     = null

  validation {
    # A zero price is the free arrangement, which publishers choose
    # explicitly; the console's default is not zero for that reason.
    condition     = var.console_default_rate == null ? true : (can(regex("^[0-9]+(\\.[0-9]+)?$", var.console_default_rate)) && length(var.console_default_rate) <= 32 && try(tonumber(var.console_default_rate) > 0, false))
    error_message = "console_default_rate must be a decimal number greater than zero, such as \"0.01\": digits, an optional fractional part, no sign or exponent, at most 32 characters."
  }
}

variable "console_login_hostname" {
  description = "Public full domain name of the publishers' Zitadel instance, where the console sends publishers to sign in and which it trusts as its only OIDC issuer (https://<console_login_hostname>). Required when console_hostname is set, and must differ from zitadel_hostname, the developers' instance. Caddy serves it from the Zitadel site, so it needs a DNS record that points at this VM. The instance itself is created after the first rollout, by bootstrap-identity.sh."
  type        = string
  default     = null

  validation {
    condition     = var.console_login_hostname == null || can(regex(local.hostname_pattern, var.console_login_hostname))
    error_message = "console_login_hostname must be a lowercase DNS hostname (labels of letters, digits, and inner hyphens, joined by dots), with no scheme, port or path."
  }
}

variable "zitadel_system_api_public_key" {
  description = "Public half (PEM, RSA) of the key that signs Zitadel System API tokens for the user fora-instance-admin, which creates the publishers' instance and, while zitadel_system_api_recovery_instance_id is set, recovers its bootstrap token. The private half stays with the operators: it is never a Terraform input, never in the bundle and never on the VM. The user holds SYSTEM_OWNER, which creates and deletes instances and sets the system-wide features every instance follows, so Caddy answers 404 on every public Zitadel hostname for each route a system permission opens, and only the compose network reaches them. Required when console_hostname is set."
  type        = string
  default     = null

  validation {
    condition     = var.zitadel_system_api_public_key == null || can(regex("^-----BEGIN PUBLIC KEY-----\\n[A-Za-z0-9+/=\\n]+-----END PUBLIC KEY-----\\n?$", var.zitadel_system_api_public_key))
    error_message = "zitadel_system_api_public_key must be one PEM block labelled PUBLIC KEY, as `openssl pkey -pubout` writes it (gen-keys.sh writes keys/zitadel-system-api-public.pem)."
  }
}

variable "zitadel_system_api_recovery_instance_id" {
  description = "Optional id of the publishers' Zitadel instance, set only while recovering a lost bootstrap token: the System API user then also holds IAM_OWNER on that instance, which lets instance.sh mint the instance's machine user a new token. While it is set, the System API key also opens that instance's Admin and Management APIs, which answer on its public hostname, so clear it again as soon as the token is back. Only valid with zitadel_system_api_public_key."
  type        = string
  default     = null

  validation {
    condition     = var.zitadel_system_api_recovery_instance_id == null || can(regex("^[0-9]{1,32}$", var.zitadel_system_api_recovery_instance_id))
    error_message = "zitadel_system_api_recovery_instance_id must be a Zitadel instance id: digits only, as `GET /admin/v1/instances/me` reports it."
  }
}

# The publishers' instance's own console admin, zadmin, like the developers'
# instance's (random_password.zitadel_admin in main.tf, same shape and the
# same reasons for it). The bootstrap imports zadmin with this password when
# it creates the user and never resets it; bootstrap-identity.sh reads it from
# the console_login_admin_password output, so it never enters the bundle.
resource "random_password" "console_login_admin" {
  count            = local.console_enabled ? 1 : 0
  length           = 24
  min_upper        = 1
  min_lower        = 1
  min_numeric      = 1
  min_special      = 1
  override_special = "!@#%^&*-_+="
}

# The key the console seals its sign-in cookies under: 32 random bytes, base64
# to CONSOLE_SESSION_KEY. Generated once and held in Terraform state, like the
# Identity Service's session key. The console has no per-process fallback, and
# a new value signs every publisher out once, so rotating it means replacing
# this resource on purpose (terraform apply -replace).
resource "random_bytes" "console_session_key" {
  count  = local.console_enabled ? 1 : 0
  length = 32
}

locals {
  console_enabled = var.console_hostname != null

  # The console's own origin. The console's CONSOLE_PUBLIC_ORIGIN, the
  # redirect address it sends to the login provider, and the key directory
  # the Exchange reads must all name this one place, so all three are built
  # from this value.
  console_origin = local.console_enabled ? "https://${var.console_hostname}" : null

  # The publishers' Zitadel instance's issuer, the one issuer the console
  # trusts. The compose file's CONSOLE_OIDC_ISSUER and the console_login_url
  # output both come from this value, and bootstrap-identity.sh compares the
  # two as strings, so it is built here once. The preconditions in main.tf
  # make the hostname set exactly when the console is.
  console_login_url = var.console_login_hostname == null ? null : "https://${var.console_login_hostname}"

  # ZITADEL_SYSTEMAPIUSERS: one System API user, whose tokens are checked
  # against the public key. Without memberships Zitadel gives the user
  # SYSTEM_OWNER; a recovery window lists that role and adds IAM_OWNER on the
  # one instance being recovered. Null renders no System API user at all.
  zitadel_system_api_users = var.zitadel_system_api_public_key == null ? null : jsonencode({
    "fora-instance-admin" = merge(
      { KeyData = base64encode(var.zitadel_system_api_public_key) },
      var.zitadel_system_api_recovery_instance_id == null ? {} : {
        Memberships = [
          { MemberType = "System", Roles = ["SYSTEM_OWNER"] },
          { MemberType = "IAM", Roles = ["IAM_OWNER"], AggregateID = var.zitadel_system_api_recovery_instance_id },
        ]
      },
    )
  })
}
