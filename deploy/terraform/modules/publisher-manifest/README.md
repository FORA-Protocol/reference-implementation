# publisher-manifest

Resource-free module that renders a publisher's discovery document — the
`fora.json` its origin serves at `/.well-known/fora.json` — from a deployment's
hostnames and ids. The `compose-stack` module serves it on the demo publisher's
origin hostname. The edge in front of the publisher passes that address through
to the origin, so this is the document every reader gets.

Because the module creates no resources, it is safe to evaluate on every apply,
including a bootstrap apply that leaves the edge out.

## Inputs

| Name | Description |
|------|-------------|
| `publisher_domain` | The publisher's own hostname, the domain its discovery document names |
| `exchange_fqdn` | Full Exchange hostname; the endpoint URL is derived as `https://<exchange_fqdn>` |
| `resource_owner_id` | Settlement payee attested in the exchange entry's `ext` object |
| `catalog_contributor_id` | Identity authorized to push the publisher's catalog |

## Outputs

| Name | Description |
|------|-------------|
| `document` | The publisher's `/.well-known/fora.json`, as the JSON its origin serves |

## Tests

`tests/manifest_unit_test.tftest.hcl` pins the document to
`deploy/publisher-wellknown/fora.json.example`, the reference copy of the file a
publisher serves, and checks that a rendering names the deployment it was
rendered for. Run it with `terraform init -backend=false && terraform test` from
this directory; it is also wired into `deploy/terraform/scripts/test-terraform.sh`.
