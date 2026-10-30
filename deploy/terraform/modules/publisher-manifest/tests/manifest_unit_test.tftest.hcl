# The document output, pinned to deploy/publisher-wellknown/fora.json.example:
# the reference copy of the file a publisher serves at /.well-known/fora.json,
# which operators are told to compare their own against and which the Go suite
# reads through the Exchange's own parser. The module and the example are two
# producers of one document, and nothing else keeps them agreeing. It compares
# through jsonencode(jsondecode(...)) rather than raw strings because jsonencode
# rewrites the angle brackets around the payee placeholder as unicode escape
# sequences. The round-trip lets the fixture keep the readable form an operator
# needs while both sides are still compared after identical escaping.

run "document_is_the_reference_copy_a_publisher_serves" {
  command = plan

  variables {
    publisher_domain       = "www.publisher.example"
    exchange_fqdn          = "exchange.example"
    resource_owner_id      = "<the payee id the Exchange operator assigns — fill in>"
    catalog_contributor_id = "catalog-contributor.example"
  }

  assert {
    condition = jsonencode(jsondecode(output.document)) == jsonencode(jsondecode(
      file("../../../publisher-wellknown/fora.json.example")
    ))
    error_message = "document no longer matches deploy/publisher-wellknown/fora.json.example. That file is the reference copy of the discovery document a publisher serves, and the Go suite reads it through the Exchange's own parser; a deployment rendering something else serves a document nobody checked. Change both together."
  }
}

run "document_names_the_deployment_it_is_rendered_for" {
  command = plan

  variables {
    publisher_domain       = "demo.publisher.example"
    exchange_fqdn          = "exchange.demo.publisher.example"
    resource_owner_id      = "example-resource-owner"
    catalog_contributor_id = "catalog-contributor.demo.publisher.example"
  }

  assert {
    condition     = jsondecode(output.document).domain == "demo.publisher.example" && jsondecode(output.document).exchanges[0].endpoint == "https://exchange.demo.publisher.example" && jsondecode(output.document).exchanges[0].ext.resource_owner_id == "example-resource-owner" && jsondecode(output.document).catalog_contributors[0].domain == "catalog-contributor.demo.publisher.example"
    error_message = "document must name this deployment's publisher, its Exchange at https://<exchange_fqdn>, its payee and its contributor"
  }
}
