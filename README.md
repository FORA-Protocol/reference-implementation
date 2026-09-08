# FORA — Reference Implementation

A working implementation of the [FORA protocol](https://github.com/FORA-Protocol/protocol)
for licensed AI content access.

FORA lets software agents find, pay for, and download licensed content from
publishers. This repository implements the whole path, end to end:

```
Agent ── MCP ──► Identity ──► Broker ──► Exchange ──► (signed URL) ──► Edge ──► Origin
```

1. **Ask.** An agent asks for content. It can call the Identity service's MCP
   tools (`fora_register`, `fora_discover`, `fora_execute`, `fora_report`,
   `fora_status`), which sign each call with the agent's stored key, or it can
   use the protocol SDK directly and sign its own requests.
2. **Discover.** The Broker finds the exchange that licenses the content. It
   reads the publisher's `/.well-known/fora.json`, and falls back to the plain
   URL when there is no such file.
3. **Buy.** The agent accepts an offer. The Exchange verifies the signatures,
   charges the agent's prepaid balance, records the transaction, and returns a
   signed URL.
4. **Download.** The agent fetches the signed URL. The Edge verifies the
   signature and passes the request to the publisher's own origin.
5. **Report.** The agent reports its usage back to the Exchange that sold it.

The agent never sees the underlying delivery URL, and no secret is shared
between tenants: every signature is asymmetric.

## Components

| Path | Language | Role |
|------|----------|------|
| [`src/identity/`](src/identity/) | Go (Connect-Go, Postgres, Vault) | Agent registry and Web Bot Auth directory. Developer sign-up, per-agent signing keys, and the MCP endpoint that turns each tool call into a key-signed FORA request. |
| [`src/broker/`](src/broker/) | Go (Connect-Go, Postgres + Redis) | Discovery and routing. Resolves a URL to the exchange that licenses it, applies per-agent spending limits, and relays the signed request. |
| [`src/exchange/`](src/exchange/) | Go (Connect-Go, Postgres, TigerBeetle) | Catalog, offer signing, transaction execution, signed-URL minting (Ed25519 or CloudFront RSA, chosen per tenant), and usage reporting. |
| [`src/edge/`](src/edge/) | TypeScript (Hono, multi-runtime) | Signed-URL verification at the CDN. One runtime-agnostic core with adapters for Cloudflare Workers, Fastly Compute, and AWS Lambda@Edge. |
| [`internal/`](internal/) | Go | Shared packages: database setup, HTTP server harness, HTTP message signatures, cost arithmetic, structural guards. |
| [`tests/e2e/`](tests/e2e/) | Python (pytest, `uv`) | End-to-end suite driving the full docker-compose stack. |

The wire format is not defined here. It comes from the protocol module
`github.com/FORA-Protocol/protocol`, pinned in `go.mod` and in the Python and
TypeScript manifests, so every language in this tree speaks one revision.

Underneath, the services need PostgreSQL (one database each), Redis (replay
protection and spending counters), TigerBeetle (the financial ledger), Vault
(agent signing keys), and an OIDC provider.

## Quick start

Requires Docker, Go 1.26, Node 22 (the version every image in this tree builds
with), and [`uv`](https://docs.astral.sh/uv/) with Python 3.12 for the
end-to-end harness.

```bash
make test-fast          # fast tests across Go, TypeScript, and Python
make test-integration   # Go integration tests against testcontainers Postgres and Redis
make test-e2e           # the full docker-compose stack, end to end
make quality            # format, lint, typecheck, fast tests, and the structural gates
```

`make help` lists the rest. `make install-tools` fetches the linters and code
generators the gates expect.

## Reconciling a transaction

Every successful purchase leaves evidence at three independent parties. To
check one transaction across all of them:

```bash
make ledger TX=<transaction-id>
```

The tool reads the Exchange's admin API, the Broker's database, and the Edge's
delivery log, then reports whether the bytes each party stored agree with each
other. It re-derives nothing — an agreement is a comparison of what was
actually recorded, so tampering at any single party shows up as a mismatch.

## Container images

Each release publishes three images to the GitHub Container Registry:

```
ghcr.io/fora-protocol/exchange
ghcr.io/fora-protocol/broker
ghcr.io/fora-protocol/identity
```

Every build gets exactly one immutable tag — the version, with no leading `v`.
There is no `latest` and no moving major or minor pointer, so a pull with no tag
fails on purpose rather than quietly running whatever was pushed last. Each
service's `DEPLOYMENT.md` names the version to pull. In production, deploy the
`@sha256:` digest instead. Our tags are written once and never moved, but that
is a policy of ours; a digest is content-addressed and nobody can move it. The
reasoning is in
[ADR-024](docs/architecture/adr-024-container-image-distribution.md).

The Edge worker is not a container. It deploys to the CDN — see
[`src/edge/DEPLOYMENT.md`](src/edge/DEPLOYMENT.md).

## Documentation

- [`docs/HANDOFF-operator.md`](docs/HANDOFF-operator.md) — start here to run
  this yourself: what the platform is made of, what to prepare, how the network
  must be laid out, and where each component's own guide lives.
- [`docs/architecture/`](docs/architecture/) — the architecture decision
  records. They carry the reasoning behind the auth layers, the two-Biscuit
  model, key rotation, the licensing vocabulary, and the testing rules.
- Per service, under `src/<service>/`: `CONFIGURATION.md` (every setting),
  `DEPLOYMENT.md` (how to run it), and `RUNBOOK.md` (what to do when it breaks).
- Backing services are documented the same way under
  [`deploy/storage/`](deploy/storage/) and [`deploy/zitadel/`](deploy/zitadel/).
- [`deploy/terraform/`](deploy/terraform/) — modules for a staging deployment on
  AWS.

## License

Apache License 2.0. See [LICENSE](LICENSE).
