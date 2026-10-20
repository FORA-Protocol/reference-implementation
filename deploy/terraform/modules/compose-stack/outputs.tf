output "user_data" {
  description = "Rendered cloud-init user data for the VM module. Contains key material — sensitive."
  value       = local.user_data
  sensitive   = true
}

output "user_data_without_comments" {
  description = "user_data with every full-line comment removed, for a stack whose user data would otherwise exceed EC2's 16 KB limit. Contains key material — sensitive."
  value       = local.user_data_without_comments
  sensitive   = true
}

output "compose_yaml" {
  description = "Rendered docker-compose.yml — the reference bundle an own-DC publisher runs. Contains the database password — sensitive."
  value       = local.compose_yaml
  sensitive   = true
}

output "postgres_password" {
  description = "Generated Postgres password (also embedded in the DSNs inside the compose file)."
  value       = random_password.postgres.result
  sensitive   = true
}

output "zitadel_admin_password" {
  description = "Generated password for the Zitadel console admin (username zadmin). Needed to reach the console at the Zitadel hostname; the identity bootstrap script does not use it — it authenticates with the machine user's PAT."
  value       = random_password.zitadel_admin.result
  sensitive   = true
}

output "console_login_url" {
  description = "Issuer URL of the publishers' Zitadel instance, https://<console_login_hostname>: the one issuer the console trusts, as its CONSOLE_OIDC_ISSUER names it. Null while the console is off."
  value       = local.console_login_url
}

output "console_login_admin_password" {
  description = "Generated password for the publishers' Zitadel instance's console admin, username zadmin. The bootstrap sets it when it creates that user and never resets it. Null while the console is off."
  value       = one(random_password.console_login_admin[*].result)
  sensitive   = true
}

output "vault_root_token" {
  description = "Generated root token of the bundled Vault in dev mode. For operator inspection of custodied agent keys; the Identity Service reads it from its own environment. Unused in server mode, where the operator initialises Vault and the Identity Service holds no token."
  value       = random_password.vault_root_token.result
  sensitive   = true
}

# The three side documents next to the compose file. A caller that delivers
# the bundle some other way than this module's cloud-init (a secrets store
# fetched at boot, say) needs each of them as its own file.

output "caddyfile" {
  description = "Rendered Caddyfile. No secrets: hostnames, the ACME email and proxy rules only."
  value       = local.caddyfile
}

output "compose_yaml_without_comments" {
  description = "compose_yaml with every full-line comment removed, for a stack that ships the bundle in a size-capped store. The same pattern as user_data_without_comments. Contains the database password — sensitive."
  value       = local.compose_yaml_without_comments
  sensitive   = true
}

output "caddyfile_without_comments" {
  description = "caddyfile with every full-line comment removed, for the same reason and with the same pattern as compose_yaml_without_comments."
  value       = local.caddyfile_without_comments
}

output "init_sql" {
  description = "Rendered first-cluster-init SQL (the CREATE DATABASE statements Postgres runs once)."
  value       = local.init_sql
}

output "zitadel_init_steps" {
  description = "Rendered Zitadel FirstInstance steps. Carries the console admin password — sensitive."
  value       = local.zitadel_init_steps
  sensitive   = true
}

output "importer_units" {
  description = "The catalog importer's systemd units, unit file name => content (fora-import.service, fora-import.timer), for a stack that delivers the host some other way than this module's cloud-init. Empty when importer_key_json is null. No secrets: the units name the compose command and the schedule."
  value       = local.importer_units
}

output "static_wba_files" {
  description = "The body of every static_wba_directories entry, path under /opt/fora => content (wba/<hostname>.json), for a stack that delivers the bundle some other way than this module's cloud-init. The Caddyfile serves each file with the response signatures made over exactly these bytes, so a stack writes this content, never the pre-signed envelope it came in. Empty when static_wba_directories is. No secrets: public keys only."
  value       = local.static_wba_files
}

output "mcp_url" {
  description = "Origin MCP clients connect to: https:// plus mcp_hostname when set, otherwise the Identity hostname."
  value       = "https://${local.mcp_hostname}"
}
