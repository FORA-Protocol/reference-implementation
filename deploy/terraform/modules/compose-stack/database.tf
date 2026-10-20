variable "external_postgres" {
  description = "Optional pre-provisioned PostgreSQL endpoint and trusted CA. Null retains the self-hosted database containers."
  type = object({
    host   = string
    port   = optional(number, 5432)
    ca_pem = string
  })
  default = null

  validation {
    condition = var.external_postgres == null ? true : (
      can(regex("^[A-Za-z0-9][A-Za-z0-9.-]*[A-Za-z0-9]$", var.external_postgres.host)) &&
      strcontains(var.external_postgres.host, ".") && !can(regex("^[0-9.]+$", var.external_postgres.host)) &&
      var.external_postgres.port >= 1 && var.external_postgres.port <= 65535 &&
      floor(var.external_postgres.port) == var.external_postgres.port &&
      can(regex("-----BEGIN CERTIFICATE-----", var.external_postgres.ca_pem))
    )
    error_message = "external_postgres requires a DNS hostname, integer TCP port and PEM CA certificates."
  }
}

resource "random_password" "database" {
  # The console's database, and so its password, exists only while the
  # console is on.
  for_each = var.external_postgres == null ? toset([]) : toset(concat(["sor", "identity"], local.console_enabled ? ["console"] : []))
  length   = 32
  special  = false
}

locals {
  external_database = var.external_postgres != null
  database_ca_path  = "/db-ca/ca.pem"
  database_passwords = merge(
    {
      fora     = random_password.postgres.result
      sor      = local.external_database ? random_password.database["sor"].result : random_password.postgres.result
      identity = local.external_database ? random_password.database["identity"].result : random_password.postgres.result
      zitadel  = random_password.zitadel_db.result
    },
    local.console_enabled ? {
      console = local.external_database ? random_password.database["console"].result : random_password.postgres.result
    } : {},
  )
  database_users = { for name in keys(local.database_passwords) : name => local.external_database || name == "zitadel" ? name : "fora" }
  database_host  = local.external_database ? var.external_postgres.host : "postgres"
  database_port  = local.external_database ? var.external_postgres.port : 5432
  database_tls   = local.external_database ? "sslmode=verify-full&sslrootcert=${local.database_ca_path}" : "sslmode=disable"
  database_dsns = {
    for name, password in local.database_passwords : name =>
    "postgres://${local.database_users[name]}:${password}@${local.database_host}:${local.database_port}/${name}?${local.database_tls}"
  }
  database_config_json = local.external_database ? jsonencode({
    host         = local.database_host
    port         = local.database_port
    sslmode      = "verify-full"
    sslrootcert  = local.database_ca_path
    client_image = "postgres:16-alpine"
    databases = {
      for name, password in local.database_passwords : name => { username = local.database_users[name], password = password }
    }
  }) : null
}

output "database_config_json" {
  description = "Scoped external database connection contract for a protected release bundle; null for self-hosted mode. Never contains administrator credentials."
  value       = local.database_config_json
  sensitive   = true
}
