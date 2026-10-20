# The catalog feed the Broker's catalog search indexes, reduced to what the
# index reads: each record's domain, path and fora-music-v1 keys (the
# music.<field> keys of ext), with the terms
# left out (the Exchange sells the terms from its own catalog). The result is
# still a valid catalog feed with the same resource URIs, about 15% of the
# size of the full feed once base64-encoded for cloud-init, which keeps it
# inside the 16 KB EC2 allows for user data as the catalog grows.
#
# The demo brand hosts are rewritten to publisher_host exactly as
# scripts/seed-staging.sh rewrites them before ingesting the feed, because the
# index must hold the URIs the Exchange stores. A change to that rule has to
# be made in both places.

variable "catalog_feed_path" {
  description = "Path to the JSONL catalog feed ingested into the Exchange."
  type        = string
  nullable    = false
}

variable "publisher_host" {
  description = "Host the seed ingests every demo brand host (music.demo.fora-protocol.org, sfx.demo.fora-protocol.org, demo.fora-protocol.org) as. Other hosts are kept."
  type        = string
  nullable    = false
}

locals {
  records = [for line in split("\n", file(var.catalog_feed_path)) : jsondecode(line) if trimspace(line) != ""]
  # The music.<field> keys of each record's ext, in feed order. A record without
  # ext, or without a music key, maps to an empty object.
  music = [for r in local.records : { for k, v in try(r.ext, {}) : k => v if startswith(k, "music.") }]
}

output "index_feed" {
  description = "One JSONL line per record that carries a music.<field> key in ext."
  value = join("", [
    for i, r in local.records :
    "${jsonencode({
      domain = replace(r.domain, "/^((music|sfx)\\.)?demo\\.fora-protocol\\.org$/", var.publisher_host)
      path   = r.path
      terms  = []
      ext    = local.music[i]
    })}\n"
    if length(local.music[i]) > 0
  ])
}
