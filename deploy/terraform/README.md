# FORA Terraform deployment package

Everything needed to run the full FORA stack on AWS — a staging and a demo
environment, and the modules a production deployment of your own is composed
from — plus the one piece a publisher applies themselves: the Cloudflare edge
worker module.

Three ways to use this package:

1. **All-inclusive staging** (`stacks/staging-aws`) — one EC2 VM runs the
   whole stack with Docker Compose (Postgres, Redis, TigerBeetle, Exchange,
   Broker, the Identity Service with its key store and OIDC provider, Caddy
   for TLS, and a demo publisher origin). Cloudflare provides DNS and runs the
   edge worker in front of the demo publisher hostname. This is what we use to
   test the complete flow before handing anything to a publisher.
2. **All-AWS demo** (`stacks/demo-aws`) — the all-AWS sibling of staging:
   the same backend VM, but DNS lives in Route 53 and the publisher hostname
   is fronted by CloudFront + Lambda@Edge running the same Ed25519 edge
   worker. No Cloudflare account, zone, or token is involved anywhere. See
   `docs/deploy-demo-aws.md`.
3. **Standalone edge** (`stacks/edge`) — ONLY the Cloudflare edge worker,
   applied by a publisher against their own Cloudflare
   account. It needs nothing else from this package. See
   `docs/deploy-edge-standalone.md`.

The Compose file rendered for staging is also the reference bundle for a
publisher running the backend in their own data center — staging tests the
exact artifact we hand over. It carries the agent-identity plane too, which
such a publisher does not need; see "Deferred scope". The `compose-stack`
module can also run the publisher console as an optional service of the
bundle; its inputs are described in [docs/variables.md](docs/variables.md),
under Modules.

## Layout

```
modules/                 reusable building blocks (flat, purpose-named)
  compose-stack/         renders compose file + Caddyfile + cloud-init (no cloud provider)
  aws-vm/                VPC + EC2 + Elastic IP (+ optional durable data volume) for the VM
  cloudflare-dns/        A records for the service hostnames
  route53-dns/           the Route 53 counterpart of cloudflare-dns
  cloudflare-edge/       the edge worker (the publisher handoff module)
  aws-cloudfront-edge/   the CloudFront + Lambda@Edge counterpart of cloudflare-edge
  publisher-manifest/    renders the publisher-manifest env values both edge modules consume
stacks/                  root configurations you actually apply
  staging-aws/           the all-inclusive staging environment
  demo-aws/              the all-AWS demo environment (Route 53 + CloudFront/Lambda@Edge)
  edge/                  standalone edge worker (publisher-facing)
scripts/                 build, key-generation, bootstrap, seed, smoke and release helpers
  tests/                 shell guard tests for the scripts (run by test-terraform.sh)
docs/                    step-by-step guides + variables reference
publisher/               Dockerfile for the staging demo origin image
```

Modules follow the Terraform community layout: each is self-contained
(`main.tf` / `variables.tf` / `outputs.tf` / `versions.tf`), nothing is shared
between sibling modules, and `cloudflare-edge` stays independently applyable
because it is the handoff artifact. Stacks are compositions — they hold
environment wiring and values, nothing reusable.

## Deployment

Each guide starts with its own prerequisites list and explains every step —
the guides are the single home of the procedures, nothing is repeated here:

- **Staging on AWS** (`stacks/staging-aws`):
  [docs/deploy-staging-aws.md](docs/deploy-staging-aws.md) — keys → images →
  worker bundle → apply → bootstrap identity → seed + smoke.
- **All-AWS demo** (`stacks/demo-aws`):
  [docs/deploy-demo-aws.md](docs/deploy-demo-aws.md) — covers what differs
  from staging (Route 53, CloudFront + Lambda@Edge) and points at the staging
  guide for everything shared; the operator scripts select this stack via
  `STACK_DIR`.
- **Release helpers**: `scripts/release-guard.sh` and
  `scripts/release-manifest.sh` are the first two steps of a release
  pipeline. The guard refuses to publish a version that is already published;
  the manifest records every image of the release with its digest read back
  from the registry.
- **Standalone edge worker** (`stacks/edge`, what a publisher applies):
  [docs/deploy-edge-standalone.md](docs/deploy-edge-standalone.md) — needs
  only a Cloudflare account, no AWS, no registry.
- **Variables reference**:
  [docs/variables.md](docs/variables.md).
- **Removing everything**: [docs/teardown.md](docs/teardown.md).

## Tests

Every module ships a `terraform test` suite (`modules/*/tests/*.tftest.hcl`).
All suites run **without any cloud account**: the AWS and Cloudflare providers
are mocked, assertions run against the plan, and the compose-stack suite's
apply only creates the stack's in-memory random secrets (database passwords,
the Vault root token, Zitadel's master key and admin password, the Identity
Service's session and token-signing keys, the publisher console's session
key) — no credentials, no API calls, no resources, no cost.

```bash
deploy/terraform/scripts/test-terraform.sh   # all modules, the shell guard tests, every stack validated
# or per module:
cd modules/compose-stack && terraform init && terraform test
```

What they cover: template rendering (TigerBeetle static-IP wiring, optional
publisher/registry-login/RSA blocks, the optional publisher console and the
Exchange settings that trust it, the identity plane and its generated
secrets, on-demand TLS for agent subdomains, ACME staging toggle, the
no-insecure-overrides guarantee, the production arms — the ledger without
`--development`, server-mode Vault with raft, TLS and the proxy socket
mounted by the proxy and the Identity Service only), input validation
(Graviton and open-SSH rejection, billing-adapter and route-pattern checks,
AMI id and cache-grid shapes), and resource wiring (security-group rules,
gzipped user data, the durable data volume and instance profile, worker
bindings mirroring the `config.ts` contract, DNS-only vs proxied records
including the wildcard).

`test-terraform.sh` also runs the shell guard tests under `scripts/tests/`,
each against stubbed tools on `PATH`: among them the staging id/hostname
guard, the SSH command builder, the `IMPORT_DEMO_USERS=0` opt-out of the
Zitadel provisioning, the image build and the release helpers. A deployment
that keeps its own production stack in this tree keeps that stack's tests in
a suite of their own, which `test-terraform.sh` runs when the stack is
present; a tree that carries the stack without its suite, or the suite
without its stack, fails the run. It then runs `terraform validate` on every
stack with placeholder keys.

One test file is a budget rather than a behaviour:
`modules/compose-stack/tests/user_data_size_unit_test.tftest.hcl`. EC2 caps
user data at 16 KB (16384 bytes), and `aws-vm` sends it gzipped. The module
renders the cloud-init twice: `user_data` keeps every comment, and
`user_data_without_comments` drops the full-line comments, which are most of
the text. A stack with few optional blocks ships `user_data`, as the staging
stack does; the file's staging-shaped run measures about 13.2 KB gzipped
(about 40 KB raw). With every optional block on, the publisher console
included, `user_data` is about 19 KB gzipped and over the limit. A stack that
turns many optional blocks on therefore ships `user_data_without_comments`, as
the demo stack does. The file's fully-loaded run measures that document at
about 7.6 KB gzipped (about 26 KB raw, so it fits only because it is
compressed). Its documents are small; a real stack's terms documents and
catalog feed add their own size. If the comment-free document ever trips the
assertion, the answer is to stop inlining the bundle and fetch it at boot — a
deliberate design change, not something to trim the templates until it fits.

Run the suites before every commit that touches this package. Anything that
needs real infrastructure is covered by the staging rehearsal
(`docs/deploy-staging-aws.md`), not by these tests.

## Why one VM and not ECS Fargate

TigerBeetle (the billing ledger) needs the `IPC_LOCK` capability,
`seccomp=unconfined`, and io_uring. Fargate allows none of these (only
`SYS_PTRACE` can be added, no privileged mode, no custom seccomp). A plain
EC2 VM with Docker Compose runs TigerBeetle with the same flags as local
development — and it means staging runs the exact Compose bundle we hand to
own-DC publishers.

## Secrets — read this

- The Terraform **state files are secrets**: they contain the rendered
  cloud-init — the signing keys, the database passwords, every secret the
  identity plane generates (the Vault root token, Zitadel's master key and
  console admin password, the Identity Service's session and token-signing
  keys), and the publisher console's session key. State is local and
  gitignored in both stacks. Do not move it to a shared backend without
  encryption.
- The staging VM receives its whole configuration — **every secret above
  included** — as EC2 user data. Anyone with EC2 read access in the AWS
  account can read user data back through the API
  (`ec2:DescribeInstanceAttribute`); IMDSv2 only protects the on-instance
  path.

  This was accepted when the bundle carried only staging signing keys, which
  are trivially regenerated. The identity plane widened it: the Vault root
  token unlocks every agent key the service custodies, and the Zitadel admin
  password controls the provider that gates developer sign-up. Whoever reads
  the user data can therefore sign FORA requests as any agent registered on
  the instance. Still accepted for staging — the agents are test identities on
  a throwaway VM — but it is impersonation exposure now, not just key
  disclosure, and the AWS account's EC2 read access is what bounds it.

  Before any production use of this pattern, secret delivery must move to a
  secrets store (SSM Parameter Store / Secrets Manager) fetched at boot with
  an instance role.
- `terraform.tfvars` / `*.auto.tfvars` and every stack's `keys/` are
  gitignored. Only the `.example` files are committed.
- Follow-up (not baseline) for staging and demo: SOPS-encrypted tfvars or an
  encrypted remote backend.

## Deferred scope

Recorded here so nobody hunts for the missing pieces:

- **S3 object-storage export**: design only, no code — nothing to deploy.
- **Durable agent key custody**: `compose-stack`'s server mode
  (`vault_mode = "server"`) runs Vault as a server behind a Vault Proxy, so
  the Identity Service holds no token. The stack that turns it on provides the
  server, proxy and TLS configuration the module mounts; the
  [Vault deployment guide](../storage/vault/DEPLOYMENT.md) describes what they
  must contain. Staging and demo keep the dev-mode Vault: its store is
  in-memory and a restart loses every custodied agent key there.
- **Zitadel runs as root in the bundle** (`user: "0"`), in every stack. Running
  it as a non-root user is an open follow-up.
- **Splitting the Compose bundle**: it now carries the agent-identity plane
  (Identity Service, Vault, Zitadel) alongside the publisher-facing services.
  A publisher running only an Exchange and Broker does not need those three,
  and cannot currently switch them off.
- **External PostgreSQL**: implemented through `compose-stack.external_postgres`,
  for a deployment on a managed service such as RDS; the staging and demo
  stacks keep their local servers.
