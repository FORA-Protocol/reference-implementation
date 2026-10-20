# ── AWS ──────────────────────────────────────────────────────────────────────

variable "aws_region" {
  description = "AWS region for the demo VM. The Lambda@Edge function and the CloudFront certificate always go to us-east-1 (an AWS requirement), through this stack's aliased provider, whatever is set here."
  type        = string
  default     = "us-east-1"
  nullable    = false
}

variable "aws_profile" {
  description = "AWS CLI profile to authenticate with. Leave unset (null) to use the default AWS credential chain (AWS_PROFILE env var, default profile, or env keys)."
  type        = string
  default     = null
}

variable "name_prefix" {
  description = "Prefix for AWS resource names (VM, security group, Lambda, IAM role)."
  type        = string
  default     = "fora-demo"
  nullable    = false
}

variable "instance_type" {
  description = "EC2 instance type (amd64 only)."
  type        = string
  default     = "t3.large"
  nullable    = false
}

variable "ssh_operators" {
  description = "Operators allowed to SSH in, keyed by name. Each key is installed with an OpenSSH from= restriction limiting it to that operator's own addresses; the security group opens the union. WARNING: editing this map replaces the VM and destroys its data."
  type = map(object({
    public_key   = string
    source_cidrs = set(string)
  }))
  nullable = false
}

# ── DNS (Route 53) ───────────────────────────────────────────────────────────

variable "route53_zone_id" {
  description = "Id of the EXISTING Route 53 hosted zone of `domain`. The zone may be shared with other projects; this stack only ever adds its own records to it and refuses to overwrite records it did not create."
  type        = string
  nullable    = false
}

variable "domain" {
  description = "Zone apex the demo hostnames live under, e.g. publisher.example. Every hostname this stack creates sits under <publisher_subdomain>.<domain>."
  type        = string
  nullable    = false
}

# ── Hostnames ────────────────────────────────────────────────────────────────
#
# Unlike the staging stack, ALL hostnames nest under one label: the publisher
# hostname is <publisher_subdomain>.<domain> and every service sits one level
# below it (exchange.demo.<domain>, mcp.demo.<domain>, ...). One label owns
# the whole demo, so it cannot collide with anything else in a shared zone.

variable "publisher_subdomain" {
  description = "Label under `domain` for the demo publisher hostname CloudFront serves, and the parent label of every service hostname."
  type        = string
  default     = "demo"
  nullable    = false
}

variable "exchange_subdomain" {
  description = "Label for the Exchange hostname, under the publisher label."
  type        = string
  default     = "exchange"
  nullable    = false
}

variable "broker_subdomain" {
  description = "Label for the Broker hostname, under the publisher label."
  type        = string
  default     = "broker"
  nullable    = false
}

variable "identity_subdomain" {
  description = "Label for the Identity Service hostname, under the publisher label — the OAuth issuer developers sign up against, and the MCP endpoint agents connect to unless mcp_subdomain gives that endpoint its own label. Agent Web Bot Auth directories are published one label below it, at <agent-slug>.<identity_subdomain>.<publisher_subdomain>.<domain>, which is why the stack also creates a wildcard record for it."
  type        = string
  default     = "mcp"
  nullable    = false
}

variable "mcp_subdomain" {
  description = "Optional label, under the publisher label, for the MCP endpoint when it should answer under a name of its own. Set, the token audience follows <mcp_subdomain>.<publisher_subdomain>.<domain> while the issuer and the agent-directory wildcard stay on identity_subdomain. Null keeps the MCP endpoint on the Identity hostname. Changing it on a running demo replaces the VM (the compose file is in its user data) and invalidates every issued bearer."
  type        = string
  default     = null
}

variable "zitadel_subdomain" {
  description = "Label for the bundled Zitadel hostname, under the publisher label — the upstream OIDC provider developer sign-in redirects to."
  type        = string
  default     = "login"
  nullable    = false
}

variable "origin_subdomain" {
  description = "Label for the demo publisher origin hostname, under the publisher label — the custom origin CloudFront fetches verified content from."
  type        = string
  default     = "origin"
  nullable    = false
}

variable "smoke_agent_subdomain" {
  description = "Label for the smoke agent's Web Bot Auth directory hostname, under the publisher label. The full hostname <label>.<publisher_subdomain>.<domain> IS the smoke agent's identity: gen-keys.sh mints the agent key with that kid, seed-staging.sh registers it, and Caddy serves the public key directory at it so the services can verify the smoke signatures. The default matches the label scripts/lib/staging-env.sh derives ids with (run key generation with STAGING_DOMAIN set to the publisher hostname, <publisher_subdomain>.<domain>); seeding and smoke verify the two agree before running."
  type        = string
  default     = "smoke-agent"
  nullable    = false
}

variable "catalog_contributor_subdomain" {
  description = "Label for the catalog contributor's Web Bot Auth directory hostname, under the publisher label. The full hostname <label>.<publisher_subdomain>.<domain> IS the identity that pushes the demo catalog (seed-staging.sh signs ingest with its key; the edge manifest authorizes it), and Caddy serves its public key directory at it. The default matches the label scripts/lib/staging-env.sh derives ids with; seeding and smoke verify the two agree before running."
  type        = string
  default     = "catalog-contributor"
  nullable    = false
}

variable "onboarding_subdomain" {
  description = "Label for the onboarding preview service hostname, under the publisher label. The public publisher onboarding page calls https://<label>.<publisher_subdomain>.<domain>/onboarding/v1/preview, so the label has to match what that page was published with."
  type        = string
  default     = "pub-onboarding"
  nullable    = false
}

# ── Onboarding preview service (optional) ────────────────────────────────────
# The server behind the public publisher onboarding page. Enabled by naming an
# image; a demo without one renders exactly what it rendered before.

variable "onboarding_image" {
  description = "Full onboarding image reference, by tag (<registry>/<prefix>/onboarding:<tag>) or by digest (<registry>/<prefix>/onboarding@sha256:...). Null disables the service. Separate from image_tag on purpose: build-push-images.sh builds onboarding only when named explicitly, and only from a tree that carries its source, and a live rollout pins a digest, which the <base>:<tag> shape cannot express."
  type        = string
  default     = null
}

variable "onboarding_allowed_origins" {
  description = "Website origins whose pages may call the onboarding service from a browser, matched exactly — every origin the public onboarding page is served from (scheme and host, e.g. https://www.publisher.example). Required when onboarding_image is set."
  type        = list(string)
  default     = []
}

variable "onboarding_contact_recipients" {
  description = "Mailboxes that receive enquiries from the onboarding page's contact form, relayed through the SES sender in ses.tf. Empty disables the form (its route answers 503). While the SES account is sandboxed, each recipient must itself be a verified SES identity in us-east-1 or SES refuses the send."
  type        = list(string)
  default     = []
}

# ── Container images ─────────────────────────────────────────────────────────

variable "image_registry" {
  description = "Container registry host. GitLab Container Registry by default; GHCR/Docker Hub/ECR work through the same three variables."
  type        = string
  default     = "registry.gitlab.com"
  nullable    = false
}

variable "image_prefix" {
  description = "Path under the registry the images live at, e.g. \"<group>/<project>\". Must be lowercase (Docker repository paths reject uppercase; GitLab publishes under the lowercased project path). Images are <registry>/<prefix>/{exchange,broker,identity,publisher}:<tag> — matching scripts/build-push-images.sh."
  type        = string
  nullable    = false

  validation {
    condition     = lower(var.image_prefix) == var.image_prefix
    error_message = "image_prefix must be lowercase — the registry stores repository paths lowercased, and docker pull on the VM would fail with an uppercase path."
  }
}

variable "image_tag" {
  description = "Image tag to deploy."
  type        = string
  default     = "latest"
  nullable    = false
}

variable "registry_username" {
  description = "Registry username for the VM's docker login (e.g. a GitLab deploy-token username). Null when the images are public."
  type        = string
  default     = null
}

variable "registry_password" {
  description = "Registry password/token for the VM's docker login. Null when the images are public."
  type        = string
  default     = null
  sensitive   = true
}

# ── Stack behavior ───────────────────────────────────────────────────────────

variable "billing_adapter" {
  description = "Exchange billing adapter. Demo default is tigerbeetle — per-article accounting on a real ledger."
  type        = string
  default     = "tigerbeetle"
  nullable    = false
}

variable "default_agent_credit" {
  description = "One-time welcome credit granted to each newly registered agent, PER CURRENCY, as comma-separated CODE=AMOUNT pairs — \"EUR=100\" on the default EUR ledger grants EUR 100.00 per agent, not 100 cents. Empty (the default) disables the grant; a freshly deployed stack then starts with an empty ledger and agents are funded by the operator scripts instead. Format and full semantics: the compose-stack module's variable of the same name."
  type        = string
  default     = ""
  nullable    = false
}

variable "acme_email" {
  description = "Email for Let's Encrypt registration (Caddy on the VM mints the certificates for the service hostnames; the publisher hostname is covered by ACM through CloudFront)."
  type        = string
  nullable    = false
}

variable "acme_staging" {
  description = "Use the Let's Encrypt staging CA (untrusted certs, no rate limits). Turn on while iterating on apply/destroy cycles."
  type        = bool
  default     = false
  nullable    = false
}

variable "acme_ca" {
  description = "ACME directory URL of another CA for Caddy's certificates, for example Google Trust Services (https://dv.acme-v02.api.pki.goog/directory) while Let's Encrypt rate-limits the demo hostnames. null (the default) keeps Let's Encrypt, with ZeroSSL as Caddy's fallback. See the compose-stack module's variable of the same name."
  type        = string
  default     = null
}

variable "acme_eab_key_id" {
  description = "Key id of the External Account Binding credentials acme_ca requires (Google Trust Services: `gcloud publicca external-account-keys create`). Set together with acme_eab_mac_key."
  type        = string
  default     = null
}

variable "acme_eab_mac_key" {
  description = "HMAC key of the EAB credentials (b64MacKey). Put it in secrets.auto.tfvars."
  type        = string
  default     = null
  sensitive   = true
}

variable "exa_api_key" {
  description = "Optional EXA API key for Broker discovery."
  type        = string
  default     = null
  sensitive   = true
}

variable "query_discovery_enabled" {
  description = "Turn on the Broker's catalog search: an agent's free-text query is matched against the catalog feed through a Bedrock model. Adds an IAM user that may invoke only that model, whose access key is delivered to the Broker alone. Off by default."
  type        = bool
  default     = false
  nullable    = false
}

variable "query_discovery_model" {
  description = "Bedrock model the Broker calls for catalog search, available on demand in aws_region."
  type        = string
  default     = "amazon.nova-lite-v1:0"
  nullable    = false
}

variable "query_discovery_catalog_file" {
  description = "Path to the JSONL catalog feed the Broker searches: the same feed ingested into the Exchange. Only each record's domain, path and music.<field> ext keys reach the VM. Null uses the demo music feed at deploy/fixtures/demo/music.jsonl."
  type        = string
  default     = null
}

variable "resource_owner_id" {
  description = "resource_owner_id the publisher manifest attests as settlement payee."
  type        = string
  default     = "demo-resource-owner"
  nullable    = false
}

variable "lambda_zip_path" {
  description = "Path to the pre-built Lambda@Edge bundle zip. Default resolves to src/edge/dist/lambda-edge.zip in this repo checkout; build it with deploy/terraform/scripts/build-lambda-edge.sh from the config this stack renders (see the lambda_edge_config output)."
  type        = string
  default     = null
}

variable "deploy_edge" {
  description = "Deploy the CloudFront distribution + Lambda@Edge function fronting <publisher_subdomain>.<domain>. Set false for the FIRST apply on a fresh checkout: the Lambda bundle does not exist yet, and its config comes out of this very stack (the lambda_edge_config output). Bootstrap order: apply with false → terraform output -raw lambda_edge_config > lambda-edge-config.json → build-lambda-edge.sh lambda-edge-config.json → apply with true."
  type        = bool
  default     = true
  nullable    = false
}

variable "fora_enforce_binding" {
  description = "Override agent-key proof-of-possession in the RENDERED Lambda config (lambda_edge_config output). Null keeps the worker default — enforce, the production posture. Set \"false\" to let a signed URL be fetched without the bound-key proof, e.g. to smoke the backend alone. The value is baked into the bundle: changing it means re-rendering the config, rebuilding the zip, and re-applying."
  type        = string
  default     = null

  validation {
    condition     = var.fora_enforce_binding == null || contains(["true", "false"], coalesce(var.fora_enforce_binding, "true"))
    error_message = "fora_enforce_binding must be \"true\", \"false\", or null."
  }
}

variable "licensed_paths" {
  description = "Path prefixes the demo edge sells, rendered into lambda_edge_config as LICENSED_PATHS_JSON. An unsigned AI-bot read is sent to negotiate only under these prefixes; every other path, the landing page and the license documents included, passes to the origin. All demo publishers share this one edge, so the list must name every prefix that is for sale. Changing it means re-rendering the config, rebuilding the zip, and re-applying."
  type        = list(string)
  default     = ["/articles/", "/lyrics/", "/tracks/", "/sfx/"]

  validation {
    condition     = length(var.licensed_paths) > 0 && alltrue([for p in var.licensed_paths : startswith(p, "/") && p != "/"])
    error_message = "licensed_paths must name at least one prefix, each starting with \"/\"; \"/\" alone would license the whole site, which is what leaving the setting out already does."
  }
}
