# Output names deliberately match stacks/staging-aws: seed-staging.sh,
# fund-staging-agent.sh, and smoke.sh read outputs through tf_out() and derive
# their key directory from STACK_DIR, so every name and path those scripts
# read exists here under the same meaning.

output "vm_public_ip" {
  description = "Elastic IP of the demo VM."
  value       = module.vm.public_ip
}

output "ssh_command" {
  # No -i here on purpose. Terraform owns the remote endpoint; the operator owns
  # their credential. State is local and gets copied between machines, and
  # terraform output replays a value the last apply stored — so a private-key
  # path baked in here would point at the last applier's workstation and be
  # wrong for everyone else. Select your key locally, with FORA_SSH_IDENTITY_FILE
  # or an ssh_config Host entry.
  description = "SSH command for the operator. Selects no identity file: your ssh client chooses the key."
  value       = module.vm.ssh_command
}

output "ssh_authorized_keys" {
  description = "The authorized_keys lines rendered for installation, by operator name: who is configured to SSH in, and from where. Public keys only — nothing here is secret."
  value       = module.vm.ssh_authorized_keys
}

output "exchange_url" {
  description = "Public Exchange URL."
  value       = "https://${local.exchange_fqdn}"
}

output "broker_url" {
  description = "Public Broker URL."
  value       = "https://${local.broker_fqdn}"
}

output "identity_url" {
  description = "Public Identity Service URL: the OAuth issuer, and its root is where developers sign up. The MCP endpoint is under mcp_url, which is this URL unless mcp_subdomain splits them."
  value       = "https://${local.identity_fqdn}"
}

output "mcp_url" {
  description = "Origin agents point an MCP client at; its /mcp path is the endpoint. smoke.sh checks that the endpoint advertises exactly this origin as its OAuth protected resource."
  value       = "https://${local.mcp_fqdn}"
}

output "zitadel_url" {
  description = "Public Zitadel URL (developer sign-in and the admin console)."
  value       = "https://${local.zitadel_fqdn}"
}

output "zitadel_admin_password" {
  description = "Generated password for the Zitadel console admin, username zadmin."
  value       = module.compose_stack.zitadel_admin_password
  sensitive   = true
}

output "publisher_hostname" {
  # Deliberately a bare hostname, unlike the *_url siblings: every consumer
  # wants the host, not a URL — seed-staging.sh inserts it as the tenant
  # domain (tenants.domain is a bare domain by contract) and the smoke test
  # composes per-article URLs from it.
  description = "Demo publisher hostname (bare, no scheme) served by CloudFront + Lambda@Edge (null while deploy_edge = false, i.e. during bootstrap)."
  value       = var.deploy_edge ? local.publisher_fqdn : null
}

output "smoke_agent_hostname" {
  # The services verify the smoke agent's signatures by fetching
  # https://<this hostname>/.well-known/http-message-signatures-directory, and
  # the id seeded into fora.agents must be this exact string. seed-staging.sh
  # and smoke.sh read it back (require_ids_match_stack) to prove the generated
  # agent key's kid matches before signing anything with it.
  description = "Hostname the smoke agent's public key directory is served at — also the smoke agent's identity id (the kid in agent-key.json must equal it)."
  value       = local.smoke_agent_fqdn
}

output "catalog_contributor_hostname" {
  # Same contract as smoke_agent_hostname, for the identity that pushes the
  # demo catalog.
  description = "Hostname the catalog contributor's public key directory is served at — also the contributor's identity id (the kid in contributor-key.json must equal it)."
  value       = local.catalog_contributor_fqdn
}

output "default_tenant_domain" {
  # The Exchange resolves its seat under this exact domain
  # (EXCHANGE_DEFAULT_TENANT), and seed-staging.sh must create the seat row
  # under the same string. Exporting the one derived value lets the seed
  # script read it back instead of deriving its own copy.
  description = "Domain the Exchange resolves its seat under; seed-staging.sh creates the seat row under this exact domain."
  value       = local.exchange_fqdn
}

output "resource_owner_id" {
  # Same contract as the staging stack's output of this name: the publisher
  # manifest attests this id and the seeding step registers a fora.publishers
  # row under it. Catalog push refuses an entry whose attested owner is not the
  # registered one, so the two are read from this one value.
  description = "Payee id the publisher manifest attests; seed-staging.sh registers the publisher under this exact id."
  value       = var.resource_owner_id
}

output "caddy_hostnames" {
  description = "Space-separated hostnames Caddy holds a certificate for from startup (the wildcard agent hostnames get theirs on demand and are left out). redeploy-demo.sh checks HTTPS on each before the identity bootstrap."
  value = join(" ", concat(
    [local.exchange_fqdn, local.broker_fqdn, local.identity_fqdn, local.zitadel_fqdn, local.origin_fqdn, local.smoke_agent_fqdn, local.catalog_contributor_fqdn],
    local.onboarding_enabled ? [local.onboarding_fqdn] : [],
    local.mcp_split ? [local.mcp_fqdn] : [],
  ))
}

output "origin_url" {
  description = "Demo publisher origin URL — the custom origin CloudFront fetches verified content from."
  value       = "https://${local.origin_fqdn}"
}

output "onboarding_url" {
  description = "Base of the onboarding preview service the public onboarding page calls (its preview route is <base>/v1/preview, its health route <base>/healthz). Null while onboarding_image is unset."
  value       = local.onboarding_enabled ? "https://${local.onboarding_fqdn}/onboarding" : null
}

# The rendered compose file and Caddyfile, at the root so a saved plan carries
# them: an additive change to a running demo (adding the onboarding service)
# is rolled out over SSH from the NEW rendering without applying the VM's user
# data change, and `terraform show -json <plan>` exposes only root outputs.
# `terraform output` reads stored state, not the new rendering — the rollout
# in deploy-demo-aws.md reads these from the plan file for that reason.

output "compose_yaml" {
  description = "Rendered docker-compose.yml, as cloud-init writes it to /opt/fora/docker-compose.yml. Embeds the Vault token and every generated password — sensitive."
  value       = module.compose_stack.compose_yaml
  sensitive   = true
}

output "caddyfile" {
  description = "Rendered Caddyfile, as cloud-init writes it to /opt/fora/Caddyfile. No secrets."
  value       = module.compose_stack.caddyfile
}

output "fora_enforce_binding" {
  # The smoke test reads this back so its bare-fetch expectation follows the
  # deployment. The value reflects what this stack RENDERS into
  # lambda_edge_config; it is true of the running edge only when the deployed
  # zip was built from the current rendered config — changing the variable
  # means re-rendering, rebuilding, and re-applying.
  description = "Agent-key proof-of-possession posture rendered into the Lambda config: \"true\" (the worker default) unless fora_enforce_binding = \"false\" was set. Null while deploy_edge = false."
  value       = var.deploy_edge ? coalesce(var.fora_enforce_binding, "true") : null
}

output "postgres_password" {
  description = "Generated Postgres password (for the documented one-time seed SQL over SSH)."
  value       = module.compose_stack.postgres_password
  sensitive   = true
}

# ── AWS-edge specifics (no staging counterpart) ──────────────────────────────

output "lambda_edge_config" {
  # The single source of truth for the baked Lambda config. Write it to a
  # file and feed it to the build script:
  #   terraform output -raw lambda_edge_config > lambda-edge-config.json
  #   ../../scripts/build-lambda-edge.sh lambda-edge-config.json
  # Derived only from variables, so it is available from the very first
  # apply — including the bootstrap apply with deploy_edge = false.
  description = "Per-deployment config JSON for build-lambda-edge.sh — the values baked into the Lambda bundle. Re-render, rebuild, and re-apply after changing any hostname variable, fora_enforce_binding or licensed_paths."
  value       = jsonencode(local.lambda_edge_config)
}

output "cloudfront_distribution_id" {
  description = "CloudFront distribution id, for cache invalidations and console lookups (null while deploy_edge = false)."
  value       = var.deploy_edge ? module.edge[0].distribution_id : null
}

output "lambda_qualified_arn" {
  description = "Published (versioned) ARN of the edge function — what CloudFront runs, and the name to search CloudWatch for (as /aws/lambda/us-east-1.<function-name>, in the region that served the request). Null while deploy_edge = false."
  value       = var.deploy_edge ? module.edge[0].lambda_qualified_arn : null
}

# ── Zitadel outbound mail (SES) ──────────────────────────────────────────────
# What the running Zitadel instance was configured with at first boot, so an
# operator can compare it against the provider stored in Zitadel's console
# without reading the state JSON. Changing any of them here does NOT
# reconfigure a running instance — see ses.tf.

output "zitadel_smtp_host" {
  description = "SMTP relay Zitadel was configured to send through, as host:port."
  value       = local.zitadel_smtp_host
}

output "zitadel_smtp_from" {
  description = "Sender address Zitadel sends notification mail as. SES rejects any send whose From differs from this."
  value       = local.zitadel_smtp_from
}

output "zitadel_smtp_username" {
  description = "SMTP username — the IAM access key id of the send-only SES user."
  value       = aws_iam_access_key.zitadel_smtp.id
}

output "zitadel_smtp_password" {
  # sensitive only redacts normal CLI output. The secret access key and this
  # derived password are both stored in plain text in the local state file,
  # and the password is also in the VM's user data — the same posture as every
  # other secret this stack generates.
  description = "SMTP password — the region-derived SES password, not the raw secret access key."
  value       = aws_iam_access_key.zitadel_smtp.ses_smtp_password_v4
  sensitive   = true
}
