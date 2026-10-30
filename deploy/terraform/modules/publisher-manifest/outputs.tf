# The publisher's discovery document, the file its origin serves at
# /.well-known/fora.json, rendered once for every deployment that runs a demo
# publisher origin (stacks/staging-aws, stacks/demo-aws). The edge in front of
# the publisher passes that address through to the origin, so this document is
# the one every reader gets.
#
# The test pins it to deploy/publisher-wellknown/fora.json.example, the
# reference copy of the document a publisher serves, which the Go suite reads
# through the Exchange's own parser.

output "document" {
  description = "The publisher's /.well-known/fora.json, as the JSON its origin serves."
  value = jsonencode({
    ver    = "1.0"
    role   = "ROLE_PUBLISHER"
    domain = var.publisher_domain
    exchanges = [{
      domain       = var.exchange_fqdn
      endpoint     = "https://${var.exchange_fqdn}"
      relationship = "PROVIDER_RELATIONSHIP_DIRECT"
      ext          = { resource_owner_id = var.resource_owner_id }
    }]
    supported_profiles = ["fora-news-v1", "fora-comp-v1", "fora-music-v1"]
    catalog_contributors = [{
      domain       = var.catalog_contributor_id
      relationship = "operator"
    }]
  })
}
