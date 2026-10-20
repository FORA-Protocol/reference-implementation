# Variables reference

The authoritative source is each `variables.tf` (every variable carries a
description there). This page is the overview: what you must set, what has a
sensible default, and where secrets go.

Secrets (marked **sensitive**) belong in a gitignored `secrets.auto.tfvars`
or `TF_VAR_*` environment variables — never in committed files.

## SSH access — two things to know before you edit `ssh_operators`

**Editing `ssh_operators` replaces the VM and destroys its data.** The map is
part of the instance's user data, so adding, removing, or re-keying an operator
recreates the instance. The Elastic IP survives, so DNS does not have to follow;
everything on the root volume does not. Do not add an operator on the morning of
a demo.

**Everyone logs in as `ubuntu`. Per-operator keys are not per-operator
accounts.** Each key can be revoked on its own and each authentication is
attributable to a key, but once the session is open every operator is the same
Unix user, and shell history cannot be attributed to a person. If per-person
shell accountability is needed, the answer is a Unix account per operator or
Systems Manager session logging — this change does not provide it.


## stacks/staging-aws

### Required

| Variable | Sensitive | What it is |
|---|---|---|
| `ssh_operators` | | Operators allowed to SSH in, keyed by name: `{ alice = { public_key = "ssh-ed25519 AAAA... alice@laptop", source_cidrs = ["203.0.113.7/32"] } }`. Two mechanisms, both required — the security group opens the union of every operator's addresses, and an OpenSSH `from=` option on each installed key refuses that key from any address that is not its own. `public_key` is the one-line contents of the `.pub` file, not a path to it. Addresses are single hosts as `/32`. See the note below the tables |
| `cloudflare_api_token` | yes | DNS:Edit + Workers Routes:Edit (zone), Workers Scripts:Edit (account) |
| `cloudflare_account_id` | | Cloudflare account id |
| `cloudflare_zone_id` | | Zone id of `domain` |
| `domain` | | Zone apex the staging hostnames live under |
| `image_prefix` | | Registry path, e.g. `group/project` |
| `acme_email` | | Let's Encrypt registration email |

### Optional

| Variable | Default | Notes |
|---|---|---|
| `aws_region` / `aws_profile` | `us-east-1` / null | Null profile uses your default AWS credentials |
| `name_prefix` | `fora-staging` | AWS resource naming |
| `instance_type` | `t3.large` | amd64 only — Graviton types are rejected |
| `image_registry` / `image_tag` | `registry.gitlab.com` / `latest` | Registry switch happens here |
| `registry_username` / `registry_password` | null | Only for private registries; password is **sensitive** |
| `exchange_subdomain` … `publisher_subdomain` | `exchange`, `broker`, `mcp`, `login`, `origin`, `demo` | Hostname labels under `domain`. `mcp` (`identity_subdomain`) also owns a wildcard record: agent directories are published at `<agent-slug>.mcp.<domain>` |
| `mcp_subdomain` | null | Optional label for the MCP endpoint when it should answer under a name of its own. The token audience (the OAuth protected resource the endpoint advertises) follows it; the issuer and the agent wildcard stay on `identity_subdomain`. Replaces the VM and invalidates every issued bearer |
| `billing_adapter` | `tigerbeetle` | Also controls whether the TigerBeetle container runs |
| `acme_staging` | `false` | Untrusted staging CA — use while iterating on apply/destroy |
| `exa_api_key` | null | **Sensitive.** Broker discovery |
| `smoke_agent_subdomain` / `catalog_contributor_subdomain` | `smoke-agent`, `catalog-contributor` | Hostname labels for the smoke identities' public key directories. The full hostname IS each identity's id — `gen-keys.sh` mints the matching kids, and seeding/smoke verify they agree with the stack |
| `resource_owner_id` | `staging-resource-owner` | Settlement payee in the publisher manifest |
| `worker_bundle_path` | in-repo `src/edge/dist/worker.mjs` | Override for out-of-repo bundles |
| `deploy_edge` | `true` | Staging's own edge worker on `demo.<domain>`. Off = backend + demo origin still run in full — the mode for fronting another publisher's hostname with a separately applied `stacks/edge` (see the publisher hand-off section in deploy-edge-standalone.md). The publisher is registered as a site listed on this stack's seat; pass its hostname as `PUBLISHER_DOMAIN` to `seed-staging.sh` |
| `fora_enforce_binding` | null | Agent-key proof-of-possession on staging's own edge worker (`deploy_edge = true`). Null = enforced, the production posture; `"false"` lets a signed URL be fetched without the bound-key proof. `smoke.sh` reads the posture back from the stack output |
| `create_waf_skip_rule` | `false` | See cloudflare-edge README before enabling |

Key material is not passed as variables — the stack reads
`keys/{ed25519-private.pem,rsa-private.pem,broker-relay-key.json}`
written by `gen-keys.sh staging-aws` (gitignored). The same run also writes
`agent-key.json` and `contributor-key.json`, which stay local: the smoke check
and the demo ingest sign with them directly. Their PUBLIC halves travel as
`keys/{smoke-agent-wba.json,catalog-contributor-wba.json}` — key directories
that `gen-keys.sh` signs for each identity's hostname with that identity's
key, as `{body, content_digest, signature_input, signature}`. The stack reads
them at apply time and passes them to compose-stack's `static_wba_directories`.
Caddy serves each `body` (a JWK Set) at the identity's hostname as
`application/http-message-signatures-directory+json`, with the three
signature headers, so the services can verify the smoke and ingest
signatures: a verifier under the Web Bot Auth profile takes a key only from a
directory response signed by that key for the host it fetched from. The
signatures last 90 days, like the keys' validity window; re-running
`gen-keys.sh` and applying renews both.

## stacks/demo-aws

Same variable set as `stacks/staging-aws`, with the differences below —
the stack swaps the Cloudflare edge for Route 53 + CloudFront/Lambda@Edge.
Key material follows the staging shape: the stack reads the same `keys/`
files, written by `gen-keys.sh demo-aws`.

### Changed

| Variable | Default | Notes |
|---|---|---|
| `route53_zone_id` | — (required) | Id of the EXISTING Route 53 hosted zone of `domain`; replaces the three `cloudflare_*` variables. The zone may be shared with other projects — the stack only adds its own records and refuses to overwrite records it did not create |
| `lambda_zip_path` | in-repo `src/edge/dist/lambda-edge.zip` | Replaces `worker_bundle_path`. Build the zip with `scripts/build-lambda-edge.sh` from the config the stack renders (see the `lambda_edge_config` output) |
| `name_prefix` | `fora-demo` | AWS resource naming |
| `resource_owner_id` | `demo-resource-owner` | Settlement payee in the publisher manifest |

### Added — the onboarding preview service

The server behind the public publisher onboarding page, run as one more
container on the demo VM and reached at
`https://<onboarding_subdomain>.<publisher_subdomain>.<domain>/onboarding`.
Off until an image is named; a demo without one renders exactly what it
rendered before.

| Variable | Default | Notes |
|---|---|---|
| `onboarding_image` | null (off) | Full image reference, by tag or by digest. Separate from `image_tag`: `build-push-images.sh` builds onboarding only when named (`build-push-images.sh onboarding`), and only from a tree that carries its source, and a live rollout pins a digest, which `<base>:<tag>` cannot express. The example tfvars shows both forms |
| `onboarding_subdomain` | `pub-onboarding` | Hostname label under the publisher label. Must match the address the public page was published with |
| `onboarding_allowed_origins` | `[]` | Every origin the public page is served from, exactly. Required when the image is set — a browser refuses the call from any origin not listed |
| `onboarding_contact_recipients` | `[]` (form off) | Mailboxes the page's contact form delivers to, sent as `noreply@<domain>` through the SES sender in `ses.tf` (the IAM policy allows no other sender; the visitor's address is Reply-To). While the SES account is sandboxed each recipient must be a verified SES identity in us-east-1 |

The stack also outputs `onboarding_url`, and the rendered `compose_yaml`
(sensitive) and `caddyfile`, which the in-place rollout in
[deploy-demo-aws.md](deploy-demo-aws.md) reads from a saved plan.

### Not present

`cloudflare_api_token`, `cloudflare_account_id`, `cloudflare_zone_id`, and
`create_waf_skip_rule` (no Cloudflare involvement).

## stacks/edge

### Required

| Variable | Sensitive | What it is |
|---|---|---|
| `cloudflare_api_token` | yes | Same scopes as above |
| `cloudflare_account_id` / `cloudflare_zone_id` | | Account + zone the worker runs on |
| `exchange_url` | | Exchange base URL |
| `provider_domain` | | Publisher domain in fora.json |
| `exchanges_json` | | Exchange entries advertised in fora.json |

### Optional

| Variable | Default | Notes |
|---|---|---|
| `route_patterns` | `["<provider_domain>/*"]` | Set for more (or other) patterns |
| `exchange_wba_url` | `<exchange_url>/.well-known/http-message-signatures-directory` | The path the Web Bot Auth spec fixes |
| `script_name` | `fora-edge` | Worker name |
| `worker_bundle_path` | in-repo `src/edge/dist/worker.mjs` | Override for out-of-repo bundles |
| `worker_hostname` | null | Creates the proxied placeholder DNS record |
| `origin_url`, `same_zone_origin`, `rsl_body`, `acme_tokens_json`, `catalog_contributors_json`, `fora_verify_keys`, `fora_enforce_binding`, `wba_keys_json`, `wba_revocation_url`, `bot_ua_allow_json`, `bot_ua_deny_json`, `licensed_paths_json` | null | Optional worker bindings — unset means the binding is not created. One of `origin_url` / `same_zone_origin = "true"` is required: the worker refuses to run without an origin mode |
| `create_waf_skip_rule` / `waf_skip_expression` | `false` / null | Zone protections exemption |

## Modules

Module inputs mirror the stack variables above; consult each module's
`variables.tf` (and `modules/cloudflare-edge/README.md`) when composing your
own stack. `modules/compose-stack` additionally exposes `extra_databases`
(multi-exchange topologies), `broker_id`, and `network_subnet` (override the
compose network's default 172.28.0.0/24 when it collides with the host's
existing networks; TigerBeetle takes host 10 of the subnet).

`modules/compose-stack.external_postgres` defaults to null (local servers).
When set to `{host, port, ca_pem}`, it renders scoped verified-TLS DSNs, a
CA-only mount, and the sensitive `database_config_json` output for provisioning
and host SQL. A stack on a managed PostgreSQL such as RDS sets it, and the
bundle then renders no local database servers and no init SQL.
`modules/aws-vm.public_subnet_cidr` optionally narrows the public subnet; null
retains the whole-VPC legacy default. A stack that also places private subnets
in the VPC, for a managed database for example, narrows it to leave them room.

Four more `modules/compose-stack` inputs select the production posture, and
every one of them defaults to what staging and demo render today:
`tigerbeetle_development` (`true`; `false` drops `--development` from the
ledger's `format` and `start`), `tigerbeetle_cache_grid` (`"256MiB"`; the
ledger's `--cache-grid`), `vault_mode` (`"dev"`; `"server"` runs Vault with
raft storage, TLS and host networking behind a Vault Proxy, and the Identity
Service reaches the Proxy over a Unix socket with no token of its own), and
`vault_kv_mount` (`"secret"`; the KV engine holding the agent keys — only
`secret` exists in dev mode). Server mode mounts `/opt/fora/vault/{server,
proxy,tls}` from the host and expects the calling stack to write them; what
each must contain is in the Vault deployment guide's
[server-mode section](../../storage/vault/DEPLOYMENT.md#server-mode-in-the-compose-bundle-what-the-host-provides).
The module also outputs the three side documents (`caddyfile`, `init_sql`,
`zitadel_init_steps`) for a stack that delivers the bundle some other way than
cloud-init.

Three more `modules/compose-stack` members belong to the automatic catalog
import: `importer_key_json` (the contributor keypair JSON; null, the default,
renders no importer service, timer or key), `importer_schedule` (the timer's
`OnCalendar`, `hourly` by default) and the `importer_units` output (the two
systemd units, for a stack that installs host units itself rather than
through the module's cloud-init).

Two more `modules/compose-stack` inputs set the denomination publishers'
licensing is priced in: `exchange_terms_currency` and `exchange_terms_unit`
(`EXCHANGE_TERMS_CURRENCY`, `EXCHANGE_TERMS_UNIT`). Each goes to both the
Exchange and the importer service, because the importer prices every imported
entry in the denomination the console previewed. A change re-prices every
imported page at the next run. Both default to null, which sets neither and
renders the bundle byte-identical; the services then fall back to EUR and
`fetches`. Both are required when the publisher console is on. The plan
checks their shape, because both are written into the compose file: the
currency is three upper-case letters (an ISO 4217 code such as `EUR`), and the
unit starts with a lower-case letter followed by lower-case letters, digits,
`_` or `-` (such as `fetches`).

Nine more `modules/compose-stack` inputs run the publisher console
(`src/console/`) as one more service of the bundle. `console_hostname` turns
it on; null, the default, renders nothing of it. `console_image`,
`console_signing_key_pem` (sensitive), `console_contributor_host`,
`console_support_email`, `console_login_hostname` (the publishers' Zitadel
instance) and `zitadel_system_api_public_key` (the System API user's public
key) are set together with it. `console_default_rate` is optional (null keeps
the console's own default, `0.01`), and so is
`zitadel_system_api_recovery_instance_id`, for a token recovery. The plan
refuses:

- a partial set of the seven, or `console_default_rate` without the console;
- a `console_login_hostname` equal to `zitadel_hostname` or `console_hostname`,
  and a recovery instance id without the System API key;
- the console without `exchange_terms_currency` and `exchange_terms_unit`;
- a signing key that is not PKCS#8 Ed25519, the format
  `openssl genpkey -algorithm ed25519` writes;
- a `console_contributor_host` other than the `kid` of `importer_key_json`,
  when the bundle also runs the importer;
- a malformed hostname, contributor host, support address or default rate.

With the console on, the module renders:

- the `console` service at `https://<console_hostname>`, with its settings
  derived from the bundle's own hostnames, and a Caddy site that passes every
  path to it;
- the Exchange's `EXCHANGE_CONSOLE_WELLKNOWN_URL` and
  `EXCHANGE_CATALOG_CONTRIBUTOR_HOST`; the second has the value of
  `console_contributor_host`, which the console gets too;
- the console's session key, generated once and kept in the state. Replacing
  `random_bytes.console_session_key` signs every publisher out once;
- a `console` database. On external PostgreSQL it gets a scoped password and
  an entry in `database_config_json`. On self-hosted PostgreSQL it gets a
  `CREATE DATABASE` in the init SQL, which PostgreSQL runs only when it first
  creates its data directory; on an existing data volume, create the database
  by hand;
- the signing key, which the module's cloud-init writes to
  `/opt/fora/keys/console/console-signing-key.pem`. Only the console mounts it;
- `console_login_hostname` as a second address of the Zitadel Caddy site,
  which answers 404 on both addresses for every route a system permission
  opens (Zitadel's System API and the system-wide features), and the System
  API user `fora-instance-admin` in Zitadel's environment;
- the publishers' instance's issuer, `https://<console_login_hostname>`, as
  the `console_login_url` output and as the console's `CONSOLE_OIDC_ISSUER`,
  both from one value;
- a password for the publishers' instance's `zadmin`, as the
  `console_login_admin_password` output; it is not written into the bundle.

The console reads its sign-in client from `/opt/fora/console-oidc` on the host,
a directory nothing else mounts. `scripts/bootstrap-identity.sh` creates the
publishers' instance and writes the client's two files there on a stack that
has the `console_login_url` output.
`console_hostname` needs a public DNS record that points at the VM: the
Exchange fetches the console's key directory from it and refuses a private
address.

`network_subnet` also becomes the Exchange's `ADMIN_ALLOWED_CIDRS`, which is
the only access control on the admin listener. The published admin port makes
Docker rewrite each caller's source address to the compose network's gateway,
so the allowlist has to cover the network's own range and `127.0.0.1` would
match nothing. In self-hosted stacks, both the admin listener (host port 8082)
and Postgres
(host port 5432) are published on the VM's loopback interface only, so they are
reachable through an SSH tunnel and not from the internet — the commands are
in [deploy-demo-aws.md](deploy-demo-aws.md).

A stack on a managed PostgreSQL reaches it at that service's private endpoint,
so it publishes no local PostgreSQL port at all.
