# The shape a hostname must have wherever this module checks one: a lowercase
# DNS name, labels of letters, digits and inner hyphens, joined by dots, with
# no scheme, port or path. Every such validation refers to this pattern, so
# the rule is written once. (A validation may refer to a local from Terraform
# 1.9 on, which versions.tf requires.)
locals {
  hostname_pattern = "^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$"
}

# ── Public hostnames (Caddy terminates TLS for each) ─────────────────────────

variable "exchange_hostname" {
  description = "Public full domain name of the Exchange, e.g. exchange.staging.example.com."
  type        = string
  nullable    = false
}

variable "broker_hostname" {
  description = "Public full domain name of the Broker."
  type        = string
  nullable    = false
}

variable "identity_hostname" {
  description = "Public full domain name of the Identity Service: the OAuth issuer developers sign up against, and the MCP endpoint agents connect to unless mcp_hostname gives that endpoint its own name. It is ALSO the zone agent directories are published under — an agent provisioned as <slug> serves its Web Bot Auth directory at <slug>.<identity_hostname>, so a wildcard *.<identity_hostname> record must resolve to this VM."
  type        = string
  nullable    = false
}

variable "mcp_hostname" {
  description = "Optional public full domain name MCP clients connect to, when the MCP endpoint is served under a name of its own. The same Identity container answers on it; the token audience it mints and the OAuth protected resource it advertises follow this name, while the issuer stays on identity_hostname because the /callback URL registered with Zitadel is derived from the issuer. Null (or a value equal to identity_hostname) keeps everything on identity_hostname."
  type        = string
  default     = null

  validation {
    condition     = var.mcp_hostname == null || can(regex(local.hostname_pattern, var.mcp_hostname))
    error_message = "mcp_hostname must be a bare lowercase DNS name: no scheme, port or path."
  }
}

variable "zitadel_hostname" {
  description = "Public full domain name of the bundled Zitadel, the upstream OIDC provider the Identity Service delegates developer sign-in to. Public because the sign-up flow redirects the developer's browser here, and because the OIDC issuer must be the same URL inside and outside the compose network."
  type        = string
  nullable    = false
}

variable "origin_hostname" {
  description = "Optional public full domain name for the demo publisher origin (the edge worker's ORIGIN_URL target). Requires publisher_image. Null disables the publisher service. Staging-only: the origin is publicly reachable, which a production publisher would restrict."
  type        = string
  default     = null
}

variable "static_wba_directories" {
  description = "Static Web Bot Auth key directories Caddy serves itself, for identities whose private keys deliberately never reach the VM, e.g. an operator-held smoke signer. A verifier resolves such a signer's key by fetching https://<hostname>/.well-known/http-message-signatures-directory. Map of hostname => that hostname's directory PRE-SIGNED, as the JSON object {body, content_digest, signature_input, signature}: body is the JWK Set document (public keys only), and the other three are the Content-Digest, Signature-Input and Signature header values of its response signatures — the WBA_SIGNED_DIRECTORY_JSON format the edge worker takes. The Web Bot Auth profile has a verifier take a key only from a directory response signed by that key for the host it fetched from, and this VM holds no key, so whoever holds the keys signs offline (gen-keys.sh does it for the stacks' identities, through src/exchange/cmd/fora-sign-directory). Caddy serves body byte for byte as application/http-message-signatures-directory+json, with the three headers. The plan checks the shape of every value, that content_digest is the digest of body, and that the signers are exactly the listed keys. It cannot verify the signatures, so signing for exactly this hostname, with every listed key, inside a window that has not closed, is the signer's responsibility. Each hostname needs its own DNS record pointing at this VM."
  type        = map(string)
  default     = {}

  validation {
    # The envelope: a JSON object with exactly the four members, as
    # src/edge/scripts/sign-wba-directory.mjs prints it and gen-keys.sh writes
    # it. A bare JWK Set, the shape this variable took before directories had
    # to be signed, fails here instead of being served unsigned.
    condition = alltrue([
      for doc in values(var.static_wba_directories) :
      try(join(",", sort(keys(jsondecode(doc)))) == "body,content_digest,signature,signature_input", false)
    ])
    error_message = "Every static_wba_directories value must be a pre-signed key directory: a JSON object with exactly the members body, content_digest, signature_input and signature. A bare JWK Set is refused, because a verifier takes no key from an unsigned directory."
  }

  validation {
    # body: mirrors the required half of the canonical directory schema
    # (internal/forawellknown/schema/fora-wba-directory.json): at least one
    # key, and every entry carries all seven members with an unpadded
    # 43-character base64url x and RFC 3339 window bounds. Without this, a
    # document missing `use` or carrying a padded x applies cleanly, serves
    # 200, and fails the first signed request with an error that points at
    # the signature — this check moves that failure to `terraform plan`.
    condition = alltrue([
      for doc in values(var.static_wba_directories) : try(
        length(jsondecode(jsondecode(doc).body).keys) > 0 &&
        alltrue([
          for k in jsondecode(jsondecode(doc).body).keys :
          try(k.kty, null) == "OKP" &&
          try(k.crv, null) == "Ed25519" &&
          try(k.use, null) == "sig" &&
          try(k.alg, null) == "EdDSA" &&
          can(regex("^[A-Za-z0-9_-]{43}$", try(k.x, ""))) &&
          can(regex("^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}(\\.\\d+)?(Z|[+-]\\d{2}:\\d{2})$", try(k.not_before, ""))) &&
          can(regex("^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}(\\.\\d+)?(Z|[+-]\\d{2}:\\d{2})$", try(k.not_after, "")))
        ]),
      false)
    ])
    error_message = "Every static_wba_directories body must be a JWK Set with at least one key, and every key must carry kty=OKP, crv=Ed25519, use=sig, alg=EdDSA, an unpadded 43-character base64url x, and RFC 3339 not_before/not_after. Verifiers require this exact shape — a malformed document would deploy fine and then fail every signed request."
  }

  validation {
    # The three header values are interpolated into the Caddyfile, each as a
    # backtick-quoted token, so they are held to the exact shapes the protocol
    # SDKs' SignDirectoryResponse emits: a Content-Digest of one sha-256
    # member; a Signature of one 64-byte value per label; a Signature-Input of
    # one member per label that covers "@authority";req and content-digest and
    # carries created, expires, keyid (an RFC 7638 thumbprint), alg="ed25519"
    # and the directory tag, in that order. Members are joined by ", " as the
    # SDKs join them. None of these shapes admits a backtick, a brace, a
    # newline or any other character that could end the token or open a Caddy
    # placeholder, so no value can reach the Caddyfile as configuration.
    condition = alltrue([
      for doc in values(var.static_wba_directories) : try(
        can(regex("^sha-256=:[A-Za-z0-9+/]{43}=:$", jsondecode(doc).content_digest)) &&
        alltrue([
          for m in split(", ", jsondecode(doc).signature) :
          can(regex("^[a-z][a-z0-9_-]{0,31}=:[A-Za-z0-9+/]{86}==:$", m))
        ]) &&
        alltrue([
          for m in split(", ", jsondecode(doc).signature_input) :
          can(regex("^[a-z][a-z0-9_-]{0,31}=\\(\"@authority\";req \"content-digest\"\\);created=[0-9]{1,12};expires=[0-9]{1,12};keyid=\"[A-Za-z0-9_-]{43}\";alg=\"ed25519\";tag=\"http-message-signatures-directory\"$", m))
        ]),
      false)
    ])
    error_message = "Every static_wba_directories value's content_digest, signature_input and signature must have the shapes the protocol SDKs' directory response signer emits: content_digest sha-256=:<base64>:, one signature_input member per label covering \"@authority\";req and content-digest with created, expires, keyid, alg=\"ed25519\" and tag=\"http-message-signatures-directory\", and one signature member per label, members joined by \", \". They are interpolated into the Caddyfile, so nothing else is accepted."
  }

  validation {
    # What the plan can check about the signatures without verifying them:
    # the digest is the digest of the body Caddy will serve, so an edit to the
    # body after signing fails here and not at the verifier; the two signature
    # headers name the same labels, once each; and the signers are exactly
    # the listed keys, one signature per key. keyid is the RFC 7638
    # thumbprint, base64url SHA-256 of {"crv":"Ed25519","kty":"OKP","x":"<x>"}.
    # The profile requires a signature by every listed key, and a verifier
    # hands out only the keys that signed.
    condition = alltrue([
      for doc in values(var.static_wba_directories) : try(
        jsondecode(doc).content_digest == "sha-256=:${base64sha256(jsondecode(doc).body)}:" &&
        join(",", sort([for m in split(", ", jsondecode(doc).signature) : split("=", m)[0]])) ==
        join(",", sort([for m in split(", ", jsondecode(doc).signature_input) : split("=", m)[0]])) &&
        length(distinct([for m in split(", ", jsondecode(doc).signature) : split("=", m)[0]])) == length(split(", ", jsondecode(doc).signature)) &&
        length(split(", ", jsondecode(doc).signature)) == length(jsondecode(jsondecode(doc).body).keys) &&
        join(",", sort(flatten(regexall("keyid=\"([A-Za-z0-9_-]{43})\"", jsondecode(doc).signature_input)))) ==
        join(",", sort([
          for k in jsondecode(jsondecode(doc).body).keys :
          replace(replace(trimsuffix(base64sha256("{\"crv\":\"Ed25519\",\"kty\":\"OKP\",\"x\":\"${k.x}\"}"), "="), "+", "-"), "/", "_")
        ])),
      false)
    ])
    error_message = "Every static_wba_directories value must be signed over its own body by exactly the keys it lists: content_digest must be the SHA-256 digest of body (sign again after any edit to body), signature and signature_input must name the same labels once each, and the keyids must be the RFC 7638 thumbprints of the listed keys, one signature per key."
  }

  validation {
    # The map KEYS are interpolated verbatim into a Caddy site block and
    # rewrite target (Caddyfile.tftpl) and a cloud-init write_files path. The
    # body travels base64-encoded and the header values are held to the
    # shapes above, so the hostname is the one part that lands in rendered
    # configuration with no other check. Same restriction the module's other
    # template-bound string inputs carry (registry_server, registry_username,
    # extra_databases).
    condition = alltrue([
      for hostname in keys(var.static_wba_directories) :
      can(regex(local.hostname_pattern, hostname))
    ])
    error_message = "Every static_wba_directories key must be a lowercase DNS hostname (labels of letters, digits, and inner hyphens, joined by dots) — it is interpolated verbatim into the Caddyfile and a cloud-init file path."
  }
}

# ── Container images ─────────────────────────────────────────────────────────

variable "exchange_image" {
  description = "Full Exchange image reference, e.g. registry.example.com/group/fora/exchange:latest."
  type        = string
  nullable    = false
}

variable "broker_image" {
  description = "Full Broker image reference."
  type        = string
  nullable    = false
}

variable "identity_image" {
  description = "Full Identity Service image reference (built by build-push-images.sh from src/identity/Dockerfile)."
  type        = string
  nullable    = false
}

variable "publisher_image" {
  description = "Optional demo publisher origin image (nginx + demo content, built by build-push-images.sh). Required when origin_hostname is set."
  type        = string
  default     = null
}

# ── Onboarding preview service (optional) ────────────────────────────────────
# The server behind the public publisher onboarding page. Stateless, talks to
# no other service in this bundle, and fetches addresses anonymous visitors
# type — so it is reached through Caddy only and its address guard is never
# switched off here (SKIP_SSRF and ALLOW_INSECURE are not inputs on purpose).

variable "onboarding_hostname" {
  description = "Optional public full domain name the onboarding preview service answers on, under the /onboarding path prefix. Requires onboarding_image. Null disables the service."
  type        = string
  default     = null
}

variable "onboarding_image" {
  description = "Full onboarding image reference, by tag or by digest (build-push-images.sh onboarding builds it, from a tree that carries the service's source). Required when onboarding_hostname is set."
  type        = string
  default     = null
}

variable "onboarding_allowed_origins" {
  description = "Websites whose pages may call the onboarding service from a browser, matched exactly (scheme, host, port). The public onboarding page is served from a different origin than this service, so a browser refuses the call unless its origin is listed. No wildcard: the service fetches addresses it is given."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for o in var.onboarding_allowed_origins : can(regex("^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?$", o))])
    error_message = "Each onboarding_allowed_origins entry must be a bare origin: scheme://host[:port], no path, no trailing slash."
  }
}

variable "onboarding_exchange_domain" {
  description = "The Exchange named in the previewed offer and in the discovery document the publisher is shown. Normally this bundle's own exchange_hostname."
  type        = string
  default     = null
}

variable "onboarding_exchange_endpoint" {
  description = "The Exchange's address in that discovery document. Normally https://<exchange_hostname>."
  type        = string
  default     = null
}

variable "onboarding_contact_recipients" {
  description = "Addresses that receive enquiries from the onboarding page's contact form. Empty disables the form (the route answers 503). Set together with onboarding_contact_from and the smtp_* relay inputs: the service refuses to start on a partial contact configuration."
  type        = list(string)
  default     = []

  validation {
    # Same header-injection reasoning as smtp_from: each address is
    # interpolated into the compose file and then into a mail header.
    condition     = alltrue([for a in var.onboarding_contact_recipients : can(regex("^[^@\\s,]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$", a))])
    error_message = "Each onboarding_contact_recipients entry must be a single email address with no whitespace, comma or line break."
  }
}

variable "onboarding_contact_from" {
  description = "The address the relay sends enquiries as. Must be an address the relay is allowed to send as — for SES, the same sender smtp_from names. The visitor's address is never the sender; it becomes Reply-To."
  type        = string
  default     = null

  validation {
    condition     = var.onboarding_contact_from == null ? true : can(regex("^[^@\\s]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$", var.onboarding_contact_from))
    error_message = "onboarding_contact_from must be a single email address with no whitespace or line breaks."
  }
}

# ── Registry auth (only for private registries) ──────────────────────────────

variable "registry_server" {
  description = "Registry hostname to docker-login against on first boot (e.g. registry.gitlab.com). Null when the images are public."
  type        = string
  default     = null

  # Interpolated into a root shell command in cloud-init (docker login) —
  # restrict to hostname[:port] characters so no shell syntax can ride in.
  validation {
    condition     = var.registry_server == null || can(regex("^[A-Za-z0-9.-]+(:[0-9]+)?$", var.registry_server))
    error_message = "registry_server must be a plain hostname, optionally with :port."
  }
}

variable "registry_username" {
  description = "Registry username (e.g. a GitLab deploy-token username). Required when registry_server is set."
  type        = string
  default     = null

  # Interpolated into the same root docker-login command as registry_server.
  # Real registry usernames never need anything outside this set; a quote or
  # shell metacharacter here would otherwise run as root on first boot.
  validation {
    condition     = var.registry_username == null || can(regex("^[A-Za-z0-9._+@-]+$", var.registry_username))
    error_message = "registry_username may only contain letters, digits, and . _ + @ - characters."
  }
}

variable "registry_password" {
  description = "Registry password/token. Delivered to the VM base64-encoded in a root-only file and piped to docker login via --password-stdin; any characters are safe."
  type        = string
  default     = null
  sensitive   = true
}

# ── Service configuration ────────────────────────────────────────────────────

variable "billing_adapter" {
  description = "FORA_BILLING_ADAPTER for the Exchange. \"tigerbeetle\" (staging default — per-article accounting on a real ledger) also enables the TigerBeetle container; \"free\" approves everything with no ledger."
  type        = string
  default     = "tigerbeetle"
  nullable    = false

  validation {
    condition     = contains(["tigerbeetle", "free", "inmemory"], var.billing_adapter)
    error_message = "billing_adapter must be one of: tigerbeetle, free, inmemory."
  }
}

variable "billing_ledger" {
  description = "EXCHANGE_BILLING_LEDGER for the tigerbeetle adapter: the ISO 4217 numeric currency codes this deployment bills in, one TigerBeetle ledger each. ORDER MATTERS — the Exchange advertises the FIRST entry as base_currency in its well-known document, so append a new currency rather than prepending it, and treat a reorder as a deliberate change to what other parties are told our prices are denominated in. The default [\"978\"] (EUR only) matches the demo feed's pricing currency. Ignored by the free/inmemory adapters."
  type        = list(string)
  default     = ["978"]
  nullable    = false

  validation {
    condition     = length(var.billing_ledger) > 0
    error_message = "billing_ledger must list at least one currency — the Exchange refuses to boot without one."
  }

  validation {
    condition     = alltrue([for code in var.billing_ledger : contains(["978", "840"], code)])
    error_message = "every billing_ledger entry must be an ISO 4217 numeric code the Exchange supports: 978 (EUR) or 840 (USD)."
  }

  validation {
    condition     = length(distinct(var.billing_ledger)) == length(var.billing_ledger)
    error_message = "billing_ledger must not list the same currency twice — the Exchange rejects a duplicate at boot."
  }
}

variable "broker_id" {
  description = "BROKER_ID the Broker announces."
  type        = string
  default     = "broker-staging"
  nullable    = false
}

variable "exa_api_key" {
  description = "Optional EXA API key for Broker discovery. Null leaves discovery to direct URIs only."
  type        = string
  default     = null
  sensitive   = true
}

variable "query_discovery" {
  description = "Optional catalog search for the Broker: a free-text query is matched against catalog_feed (the JSONL feed ingested into the Exchange, as text) through the Bedrock model, in aws_region. The access key pair must be allowed to invoke that model and nothing else: it is written to the Broker's own key directory and read by the Broker alone. Null (the default) leaves the setting off, and a query goes to the search provider as before."
  type = object({
    catalog_feed          = string
    model                 = string
    aws_region            = string
    aws_access_key_id     = string
    aws_secret_access_key = string
  })
  default   = null
  sensitive = true

  validation {
    condition = var.query_discovery == null ? true : alltrue([
      for v in values(var.query_discovery) : trimspace(v) != ""
    ])
    error_message = "query_discovery needs a non-empty catalog_feed, model, aws_region and access key pair; the Broker refuses to start without them."
  }
}

variable "extra_databases" {
  description = "Extra Postgres databases created at first cluster init (multi-exchange topologies). Empty for the single-exchange staging stack."
  type        = list(string)
  default     = []
  nullable    = false

  # Rendered into CREATE DATABASE statements in the init SQL. Operator-only
  # input, but restricting to plain identifiers keeps SQL syntax out by
  # construction.
  validation {
    condition     = alltrue([for db in var.extra_databases : can(regex("^[a-z_][a-z0-9_]*$", db))])
    error_message = "extra_databases entries must be plain lowercase identifiers (letters, digits, underscore; not starting with a digit)."
  }
}

variable "network_subnet" {
  description = "IPAM subnet of the compose network. Override when the default range collides with the host's existing networks (the compose bundle also runs in publisher data centers, where 172.28.0.0/24 may already be in use). TigerBeetle gets host 10 of this subnet as its static address."
  type        = string
  default     = "172.28.0.0/24"
  nullable    = false

  validation {
    condition     = can(cidrhost(var.network_subnet, 10))
    error_message = "network_subnet must be a valid CIDR block with room for at least 10 hosts (e.g. 172.28.0.0/24)."
  }
}

# ── TLS / ACME ───────────────────────────────────────────────────────────────

variable "acme_email" {
  description = "Email Caddy registers with Let's Encrypt."
  type        = string
  nullable    = false
}

variable "acme_staging" {
  description = "Use the Let's Encrypt STAGING CA (untrusted certs). Turn on while iterating on apply/destroy cycles to stay clear of production rate limits; off for the real staging environment."
  type        = bool
  default     = false
  nullable    = false
}

variable "acme_ca" {
  description = "ACME directory URL of the CA Caddy gets its certificates from, for example Google Trust Services (https://dv.acme-v02.api.pki.goog/directory) while Let's Encrypt rate-limits these hostnames. null (the default) keeps Caddy's own default: Let's Encrypt, with ZeroSSL as its fallback. Cannot be combined with acme_staging, which sets the CA itself."
  type        = string
  default     = null

  validation {
    # The value is written into the Caddyfile, so only URL characters pass.
    condition     = var.acme_ca == null || can(regex("^https://[A-Za-z0-9._~:/?&=%-]+$", var.acme_ca))
    error_message = "acme_ca must be an https:// ACME directory URL or null."
  }
}

variable "acme_eab_key_id" {
  description = "Key id of the External Account Binding (EAB) credentials the CA in acme_ca requires, for example from `gcloud publicca external-account-keys create`. Set together with acme_eab_mac_key, and only with acme_ca. The CA accepts a key once, to register one ACME account; Caddy stores that account with its certificates, so the key is not used again while the account is kept."
  type        = string
  default     = null

  validation {
    condition     = var.acme_eab_key_id == null || can(regex("^[A-Za-z0-9_-]+$", var.acme_eab_key_id))
    error_message = "acme_eab_key_id must be letters, digits, underscores and hyphens."
  }
}

variable "acme_eab_mac_key" {
  description = "Base64url HMAC key of the EAB credentials (see acme_eab_key_id). Reaches Caddy as an environment variable, so the Caddyfile holds no secret."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.acme_eab_mac_key == null || can(regex("^[A-Za-z0-9_=-]+$", var.acme_eab_mac_key))
    error_message = "acme_eab_mac_key must be base64url."
  }
}

variable "caddy_data_volume_id" {
  description = "Id of an EBS volume (vol-...) that holds Caddy's /data: its certificates and ACME account. cloud-init waits for the volume, formats it only when it is blank, and mounts it at /var/lib/fora-caddy before Docker starts, so a replaced VM keeps its certificates and does not request them again from Let's Encrypt. Nothing else goes on the volume. null (the default) keeps /data in a named Docker volume on the root disk, which is lost with the VM."
  type        = string
  default     = null

  validation {
    condition     = var.caddy_data_volume_id == null || can(regex("^vol-[0-9a-f]+$", var.caddy_data_volume_id))
    error_message = "caddy_data_volume_id must be an EBS volume id (vol-...) or null."
  }
}

# ── Key material (written to /opt/fora/keys on the VM) ───────────────────────
# Generate everything with deploy/terraform/scripts/gen-keys.sh.
# All of it lands in Terraform state — the state file is a secret.

variable "ed25519_private_pem" {
  description = "Exchange Ed25519 offer/URL signing key (PEM). The Exchange fails closed without it."
  type        = string
  sensitive   = true
  nullable    = false
}

variable "rsa_private_pem" {
  description = "Optional Exchange RSA signing key (PEM), needed only by AWS_CLOUDFRONT_RSA tenants. Null for a stack whose every tenant is on the ED25519 scheme: the Exchange starts without it and refuses only the requests that would need it, naming the setting to add. A key that IS supplied is parsed at boot, so a malformed one fails fast."
  type        = string
  default     = null
  sensitive   = true
}

variable "broker_relay_key_json" {
  description = "Broker relay signing keypair (JSON with private key). Without it the Broker ships unsigned Exchange calls and the Exchange rejects them."
  type        = string
  sensitive   = true
  nullable    = false
}

variable "default_tenant_domain" {
  description = "Domain of the seat the Exchange reads its agent-activation policy from at Register (EXCHANGE_DEFAULT_TENANT). A tenant row is the Exchange seat, so this MUST be the Exchange's own hostname — the same value as exchange_hostname, and the domain the seed step creates the seat under. A publisher's hostname is not a tenant: point this at one and no tenant row matches, so every agent Register fails with a default-tenant-not-seeded error. Stated rather than derived from exchange_hostname so the seat is written in the rendered compose file, where the seed step reads it back and cannot drift from it."
  type        = string
  nullable    = false

  # An empty string is the same as an unset variable: the binary treats an empty
  # env var as absent and falls back to EXCHANGE_DOMAIN. That fallback happens to
  # be the right value now, which is exactly why an empty one is refused here —
  # it would leave nothing stated for the seed step to read back, and the two
  # would be free to disagree silently.
  validation {
    condition     = length(trimspace(var.default_tenant_domain)) > 0
    error_message = "default_tenant_domain must not be empty (or whitespace) — the rendered compose file has to state the seat the seed step creates."
  }
}

variable "default_agent_credit" {
  description = "EXCHANGE_DEFAULT_AGENT_CREDIT for the Exchange: the one-time welcome credit granted to each newly registered agent, PER CURRENCY, written as comma-separated CODE=AMOUNT pairs (e.g. \"EUR=100,USD=100\"). Amounts are in WHOLE units, not minor units — \"EUR=100\" grants EUR 100.00 to each new agent, not 100 cents. Empty (the default) disables the grant in every currency. A currency listed here that billing_ledger does not carry stops the Exchange boot; a currency in billing_ledger that is absent here simply gets no grant. The Exchange reads this variable once at boot and grants from it at every first Register; nothing is stored in the database, so changing or removing a currency means re-applying and restarting the stack, and a seat created after the Exchange started grants the credit without a restart."
  type        = string
  default     = ""
  nullable    = false

  # The Exchange accepts only CODE=AMOUNT pairs whose amounts are plain
  # decimals with at most 8 fraction digits (the ledger asset scale), and STOPS
  # THE BOOT on anything else — a bare number (the old single-currency format),
  # exponents, negative values, currency symbols, thousands separators. This
  # check moves that boot failure to `terraform plan`. The currency codes are
  # checked for shape only; whether each one is actually billed in is a
  # cross-variable rule the Exchange enforces at boot.
  validation {
    condition = var.default_agent_credit == "" || can(regex(
      "^[A-Z]{3}=[0-9]+(\\.[0-9]{1,8})?(,[A-Z]{3}=[0-9]+(\\.[0-9]{1,8})?)*$",
      var.default_agent_credit
    ))
    error_message = "default_agent_credit must be empty or comma-separated CODE=AMOUNT pairs with plain decimal amounts of at most 8 fraction digits (e.g. \"EUR=100\", \"EUR=100,USD=9.99\") — anything else, including a bare number, stops the Exchange boot. The unit is whole currency units: \"EUR=100\" grants 100.00, not 1.00."
  }
}

variable "broker_identity_key_pem" {
  description = "Broker identity private key (PEM with a raw 64-byte ed25519 payload — the shape gen-keys.sh derives into keys/broker-identity-key.pem, NOT what openssl writes), delivered to the VM as a key file and read via BROKER_ED25519_KEY_FILE. A DIFFERENT key from broker_relay_key_json: the Broker publishes this one's public half in its WBA directory with a 90-day validity window, so it must be stable across restarts. The Broker refuses to boot without it rather than mint a throwaway that invalidates the window it just published. A key file rather than a BROKER_ED25519_SEED env value on purpose: the rendered compose file is world-readable on the VM, the per-service key directory is not."
  type        = string
  sensitive   = true
  nullable    = false

  # Two shape checks at plan time, together catching the two likely wrong
  # files: openssl's PKCS#8 (different label AND a 48-byte payload) and a
  # truncated paste. Both would otherwise pass the plan and crash-loop the
  # Broker on "PEM payload must be 64 bytes for ed25519 private key".
  validation {
    # The exact label the generator writes. openssl writes "PRIVATE KEY"
    # (PKCS#8) or "RSA PRIVATE KEY" — neither carries ED25519, so an
    # openssl-generated paste fails here, at plan time. Note this label check
    # is a generator convention and stricter than the Broker's loader, which
    # checks only the payload length: a differently-labelled file the Broker
    # would accept is still refused here.
    condition     = can(regex("-----BEGIN ED25519 PRIVATE KEY-----", var.broker_identity_key_pem))
    error_message = "broker_identity_key_pem must be the ED25519 PRIVATE KEY block gen-keys.sh writes to keys/broker-identity-key.pem — not an openssl-generated PEM, whose PKCS#8 payload the Broker's loader rejects."
  }

  validation {
    # 64 raw bytes base64-encode to exactly 88 characters (padding included).
    # Terraform cannot base64-decode binary key material (base64decode demands
    # UTF-8), so the payload length is checked in its encoded form: strip the
    # header/footer lines, count the base64 characters.
    condition     = length(join("", regexall("[A-Za-z0-9+/=]", replace(var.broker_identity_key_pem, "/-----[A-Z0-9 ]+-----/", "")))) == 88
    error_message = "broker_identity_key_pem's payload must be exactly 64 raw bytes (88 base64 characters) — the ed25519 seed||public shape the Broker's loader requires. A truncated paste or a PKCS#8 body fails this."
  }
}

# ── Zitadel outbound mail ────────────────────────────────────────────────────
# Zitadel reads these ONLY when it creates its instance on the very first
# start (its FirstInstance setup step). Changing them later has no effect on a
# running instance — the stored provider is edited through the Zitadel console
# or its Admin API instead. Because the values travel in the rendered compose
# file, and that file is the VM's cloud-init user data, changing any of them
# replaces the VM and rebuilds the whole deployment from empty.
#
# Left null the deployment has no mail provider: Zitadel cannot send the
# confirmation code self-registration needs.

variable "smtp_host" {
  description = "SMTP relay as host:port (e.g. \"email-smtp.us-east-1.amazonaws.com:587\"). Null disables outbound mail. Zitadel reads this only when it first creates its instance."
  type        = string
  default     = null

  validation {
    # The port is required, not optional: Zitadel's SMTP client dials the
    # string as given and does not default to 25/587, so a bare hostname
    # fails at send time — long after the apply that introduced it.
    condition     = var.smtp_host == null ? true : can(regex("^[A-Za-z0-9.-]+:[0-9]{1,5}$", var.smtp_host))
    error_message = "smtp_host must be host:port with a numeric port (e.g. email-smtp.us-east-1.amazonaws.com:587). Zitadel dials this string verbatim and supplies no default port."
  }
}

variable "smtp_user" {
  description = "SMTP username. For Amazon SES this is the IAM access key id of the sending user."
  type        = string
  default     = null
}

variable "smtp_password" {
  description = "SMTP password. For Amazon SES this is the region-derived SMTP password (aws_iam_access_key.<name>.ses_smtp_password_v4), NOT the raw secret access key."
  type        = string
  default     = null
  sensitive   = true
}

variable "smtp_from" {
  description = "Envelope and header sender address. Must be an address the relay is allowed to send as — for SES, one under the verified identity."
  type        = string
  default     = null

  validation {
    # Shape plus an explicit CR/LF ban: the value is interpolated into the
    # compose file and then into a mail header, where an embedded newline
    # would let a bad value inject a second header.
    condition     = var.smtp_from == null ? true : can(regex("^[^@\\s]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$", var.smtp_from))
    error_message = "smtp_from must be a single email address with no whitespace or line breaks."
  }
}

variable "smtp_from_name" {
  description = "Display name shown next to the sender address. Null renders the address alone."
  type        = string
  default     = null

  validation {
    # Same header-injection reasoning as smtp_from, with a length cap that
    # keeps the rendered YAML line reasonable.
    condition     = var.smtp_from_name == null ? true : (length(var.smtp_from_name) <= 78 && !can(regex("[\r\n]", var.smtp_from_name)))
    error_message = "smtp_from_name must be at most 78 characters and contain no line breaks."
  }
}

variable "exchange_registration_schema" {
  description = "JSON Schema the Exchange publishes as account_registration.data_schema and enforces on Register. Pass the schema as a JSON STRING (build it with jsonencode). Unset publishes no schema and registration accepts any payload. Publishing a schema IS the enforcement switch: a registration that does not conform is refused from the moment this is set."
  type        = string
  default     = null

  validation {
    # The Exchange REFUSES TO BOOT on a schema it cannot use — deliberate, so
    # an operator never believes a requirement is published while it is not.
    # That makes a typo here a demo outage, so catch unparseable JSON at plan
    # time instead of at first boot.
    condition     = var.exchange_registration_schema == null || can(jsondecode(var.exchange_registration_schema))
    error_message = "exchange_registration_schema must be a JSON string. The Exchange refuses to boot on a schema it cannot parse, so a malformed value here takes the deployment down rather than degrading."
  }
}

variable "exchange_terms_uri" {
  description = "URI of the terms document the Exchange publishes as account_registration.terms_uri. Point it at an immutable, revisioned path (…/terms/revision-1.txt), never a mutable one: registrations accepted under a revision must still resolve once the next revision exists."
  type        = string
  default     = null

  validation {
    condition     = var.exchange_terms_uri == null || can(regex("^https://", var.exchange_terms_uri))
    error_message = "exchange_terms_uri must be an https:// URI — a registering client fetches it to read the terms it is accepting."
  }
}

variable "exchange_terms_digest" {
  description = "Digest of the terms document the Exchange publishes as account_registration.terms_digest and holds registering clients to. It must equal the digest of the bytes actually SERVED — derive it with filesha256() over the same file passed in exchange_terms_documents rather than writing it by hand."
  type        = string
  default     = null

  validation {
    # The format the protocol pins for this field.
    condition     = var.exchange_terms_digest == null || can(regex("^(sha256:[0-9a-f]{64}|sha384:[0-9a-f]{96}|sha512:[0-9a-f]{128})$", var.exchange_terms_digest))
    error_message = "exchange_terms_digest must be sha256:<64 hex>, sha384:<96 hex> or sha512:<128 hex>, lowercase."
  }
}

variable "exchange_terms_currency" {
  description = "EXCHANGE_TERMS_CURRENCY for the Exchange and the catalog importer: the currency publishers' licensing is priced in. The console previews terms in it and the importer prices every imported entry in it on every run, so both services always get the same value, and a change re-prices every imported page at the next run. null, the default, sets it on neither service, both fall back to EUR, and the compose bundle is byte-identical to a deployment without this input. Required when console_hostname is set."
  type        = string
  default     = null

  validation {
    # Interpolated into the compose file for two services. The protocol
    # carries a currency as an ISO 4217 code: three uppercase letters.
    condition     = var.exchange_terms_currency == null || can(regex("^[A-Z]{3}$", var.exchange_terms_currency))
    error_message = "exchange_terms_currency must be an ISO 4217 alphabetic code: three uppercase letters, such as \"EUR\"."
  }
}

variable "exchange_terms_unit" {
  description = "EXCHANGE_TERMS_UNIT for the Exchange and the catalog importer: the metering unit of a per-access term, a registered pricing unit token. Given to both services for the same reason as exchange_terms_currency. null, the default, sets it on neither service and both fall back to fetches. Required when console_hostname is set."
  type        = string
  default     = null

  validation {
    # Interpolated into the compose file for two services. The registered
    # pricing unit tokens are lowercase words joined by hyphens, which this
    # shape accepts.
    condition     = var.exchange_terms_unit == null || can(regex("^[a-z][a-z0-9_-]*$", var.exchange_terms_unit))
    error_message = "exchange_terms_unit must be a pricing unit token: a lowercase letter, then lowercase letters, digits, underscores or hyphens, such as \"fetches\"."
  }
}

variable "exchange_terms_documents" {
  description = "Terms documents Caddy serves under /terms/ on the Exchange hostname: filename => content. A MAP rather than a single document because every revision you have published must stay online — a digest of a deleted document identifies nothing. Adding a revision later is one entry here plus one digest line, with no Caddy change. Empty serves nothing and leaves the rendered Caddyfile and compose bundle byte-identical to a deployment without this feature."
  type        = map(string)
  default     = {}

  validation {
    # The map KEYS are the half that reaches configuration verbatim: each one
    # becomes a cloud-init write_files path under /opt/fora/exchange-terms and
    # is served from that directory. The documents themselves travel base64,
    # so the filename is the only part landing in rendered config as-is. Same
    # restriction static_wba_directories puts on its hostname keys, and for
    # the same reason.
    condition = alltrue([
      for name in keys(var.exchange_terms_documents) :
      can(regex("^[A-Za-z0-9][A-Za-z0-9._-]*$", name)) && !strcontains(name, "..")
    ])
    error_message = "Every exchange_terms_documents key must be a plain filename: letters, digits, then letters, digits, dots, underscores or hyphens. No directory separators, no .., no whitespace, no control characters — the key becomes a cloud-init file path."
  }
}

variable "exchange_legal_path" {
  description = "Public path for the Exchange document directory. Demo retains /terms; production uses /legal."
  type        = string
  default     = "/terms"
  validation {
    condition     = contains(["/terms", "/legal"], var.exchange_legal_path)
    error_message = "exchange_legal_path must be /terms or /legal."
  }
}

# ── Production posture ───────────────────────────────────────────────────────
# Every default below renders the bundle exactly as it rendered before these
# inputs existed: the staging and demo stacks pass none of them, and a
# production stack turns each of them the other way.

variable "tigerbeetle_development" {
  description = "Run TigerBeetle with --development on both format and start. It turns off the Direct I/O requirement so the ledger runs on overlay and cloud volumes, and allows small caches — the staging default. A production ledger runs WITHOUT it, on a volume that supports O_DIRECT. Ignored when billing_adapter is not tigerbeetle."
  type        = bool
  default     = true
  nullable    = false
}

variable "tigerbeetle_cache_grid" {
  description = "TigerBeetle --cache-grid on start: how much memory the replica uses to cache its data file. 256MiB is a build-machine size; for production TigerBeetle's guidance is total RAM minus 3 GiB for itself minus 1 GiB for the system. The replica allocates well beyond this figure, so leave headroom or the container is OOM-killed with exit code 137 and no log line."
  type        = string
  default     = "256MiB"
  nullable    = false

  validation {
    # The value lands on the ledger's command line; TigerBeetle accepts only
    # an integer with a binary unit and refuses anything else at start.
    condition     = can(regex("^[0-9]+(MiB|GiB)$", var.tigerbeetle_cache_grid))
    error_message = "tigerbeetle_cache_grid must be an integer followed by MiB or GiB (e.g. \"256MiB\", \"12GiB\")."
  }
}

variable "vault_mode" {
  description = "How the bundled Vault runs. \"dev\": in-memory store, auto-unsealed, root token in the Identity Service's environment — the staging trade-off, where a restart loses every agent key. \"server\": raft storage on a volume, TLS, host networking, and a Vault Proxy that authenticates on the Identity Service's behalf over a Unix socket, so the service holds no token. Server mode reads its configuration from /opt/fora/vault/{server,proxy,tls} on the host; this module mounts those directories and does not write them, and deploy/storage/vault/DEPLOYMENT.md lists what each must contain."
  type        = string
  default     = "dev"
  nullable    = false

  validation {
    condition     = contains(["dev", "server"], var.vault_mode)
    error_message = "vault_mode must be \"dev\" or \"server\"."
  }
}

variable "vault_kv_mount" {
  description = "IDENTITY_KV_MOUNT: the KV version 2 secrets engine that holds the agent keys. Dev-mode Vault pre-enables only \"secret\", so the value is fixed there; server mode should use a dedicated engine, which the Vault bootstrap enables under this name."
  type        = string
  default     = "secret"
  nullable    = false

  validation {
    # Rendered verbatim into the compose file and used as a Vault path
    # segment: keep it to a plain mount name.
    condition     = can(regex("^[a-z0-9-]+$", var.vault_kv_mount))
    error_message = "vault_kv_mount must be lowercase letters, digits and hyphens only."
  }
}

# ── Automatic catalog import ─────────────────────────────────────────────────

variable "importer_key_json" {
  description = "Catalog-contributor keypair JSON (kid, private_key, public_key — the file gen-keys.sh writes to keys/contributor-key.json) the automatic catalog importer signs its pushes with. Its kid IS the contributor hostname a publisher authorizes in its manifest, and must be one of the static_wba_directories hostnames, where Caddy serves the matching public key. Delivered to the VM as /opt/fora/keys/importer/contributor-key.json and mounted ONLY into the importer service, never into the Exchange. Null disables the importer: no service, no timer, no key on the VM."
  type        = string
  default     = null
  sensitive   = true

  validation {
    condition     = var.importer_key_json == null || can(regex(local.hostname_pattern, jsondecode(var.importer_key_json).kid))
    error_message = "importer_key_json must be a JSON document whose kid is the contributor's directory hostname — the file gen-keys.sh writes to keys/contributor-key.json."
  }

  validation {
    # The Exchange verifies the importer's signature by fetching the key
    # directory at the kid's hostname. A key whose hostname this stack does
    # not serve would apply cleanly and then fail every push with a 401 that
    # points at the signature; this check moves that failure to plan time.
    condition     = var.importer_key_json == null || contains(keys(var.static_wba_directories), try(jsondecode(var.importer_key_json).kid, ""))
    error_message = "importer_key_json's kid must be one of the static_wba_directories hostnames: the Exchange resolves the importer's public key from that directory."
  }
}

variable "importer_schedule" {
  description = "systemd OnCalendar expression for the catalog importer's timer (default hourly). Every run imports every eligible listing; a run that overlaps a still-running one exits 0 without touching anything."
  type        = string
  default     = "hourly"
  nullable    = false

  validation {
    condition     = can(regex("^[A-Za-z0-9 :*,./-]+$", var.importer_schedule))
    error_message = "importer_schedule is interpolated into a systemd timer unit: letters, digits, spaces and : * , . / - only."
  }
}
