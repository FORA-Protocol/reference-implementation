# Deploying the edge worker standalone (publisher path)

This is the guide for a publisher deploying ONLY the FORA
edge worker to their own Cloudflare account. Nothing else from this package
is needed, and no other FORA infrastructure has to exist in your Terraform
state — but the worker does not work alone. It verifies URLs signed by an
Exchange, so the other FORA components (Exchange, Broker) must be running
somewhere. Usually your Exchange operator runs them and gives you the
values below. If you need to run them yourself, their sources live under
`src/exchange/` and `src/broker/`, and [deploy-staging-aws.md](deploy-staging-aws.md)
in this folder deploys the full stack.

## What the worker does

It runs in front of your content hostname and:

- serves `/.well-known/fora.json` (your FORA publisher manifest),
- verifies signed delivery URLs minted by the Exchange and forwards valid
  requests to your origin,
- rejects everything else with the FORA guidance headers.

It stores no secrets — all configuration is plain text.

## What you need

- Terraform >= 1.8, node + npm (to build the worker bundle once).
- Your Cloudflare **account id** and the **zone id** of the content domain —
  both on the zone Overview page ("API" block).
- An **API token** with: Workers Scripts:Edit (account), Workers Routes:Edit
  and DNS:Edit (the zone). Scope the token to that one account and that one
  zone (Account Resources → your account; Zone Resources → Specific zone),
  not "All accounts" / "All zones" — the token sits in a gitignored file and
  in Terraform state, so keep its blast radius small.
- From your Exchange operator: the Exchange base URL and the values for
  your publisher manifest (see below).

## Steps

```bash
# 1. build the worker bundle (writes src/edge/dist/worker.mjs)
deploy/terraform/scripts/build-cloudflare-edge.sh

# 2. configure
cd deploy/terraform/stacks/edge
cp terraform.tfvars.example terraform.tfvars           # fill in
cp secrets.auto.tfvars.example secrets.auto.tfvars     # the API token
# (or skip the secrets file and export TF_VAR_cloudflare_api_token)

# 3. apply
terraform init
terraform apply
```

## Verify

```bash
curl https://<your-hostname>/.well-known/fora.json
```

You should see your publisher manifest (your domain + the exchange entries).

Compare it field by field against the reference copy in
[../../publisher-wellknown/](../../publisher-wellknown/), which also covers the second
discovery document (your Web Bot Auth key directory) and the payee id that has to
be inside `exchanges_json` before your content can be sold.

## Inputs you will actually set

| Variable | What it is |
|---|---|
| `worker_hostname` | Set to the hostname if it has no DNS record yet; leave out if the hostname already resolves through Cloudflare |
| `exchange_url` | The Exchange base URL (from your Exchange operator) |
| `provider_domain` | Your content domain — the identity in fora.json |
| `exchanges_json` | The exchange entries advertised in fora.json (from your Exchange operator) |
| `origin_url` | Where the worker forwards verified requests (your origin) |

Two values are derived for you and only need setting when your setup is
unusual: `route_patterns` defaults to `["<provider_domain>/*"]` (set it for
more or other patterns), and `exchange_wba_url` defaults to
`<exchange_url>/.well-known/http-message-signatures-directory` (the path the
Web Bot Auth spec fixes).

Everything else is optional; the full list with descriptions is in
`deploy/terraform/modules/cloudflare-edge/variables.tf` and the module
README.

## Zone protections — important for production zones

WAF rules, Bot Fight Mode, and similar features run BEFORE the worker.
AI agents fetching signed URLs look like bots to those features, so they must
not act on the worker's hostnames. Options and limits are described in the
module README (`modules/cloudflare-edge/README.md`) — coordinate with
whoever manages the zone's security settings before go-live.

## Updating

- **New worker version**: re-run `build-cloudflare-edge.sh`, then `terraform apply` —
  the worker re-deploys only when the bundle content changed.
- **Config change** (manifest values, routes): edit tfvars, `terraform apply`.

## Deploying on top of the staging stack (publisher hand-off rehearsal)

This is how WE rehearse the publisher hand-off: the staging stack
(`stacks/staging-aws`) plays the Exchange operator running the FORA backend, and
this stack plays a publisher deploying only the edge worker for their own
hostname.

1. **Backend side** — deploy (or re-apply) staging with its own worker turned
   off, in `stacks/staging-aws/terraform.tfvars`:

   ```hcl
   deploy_edge = false
   ```

   The backend and the demo origin still run in full; only staging's worker,
   its route, and the `demo.<domain>` placeholder record are skipped (on a
   re-apply: destroyed — that hostname stops serving).

   Nothing else changes here. A tenant row is the Exchange seat, keyed on the
   Exchange's own hostname, so the seat is the same whichever publisher it
   sells. The publisher is a separate record, registered in step 3.

2. **Publisher side** — fill this stack's `terraform.tfvars` with a hostname of
   your choice on the zone and the staging backend as the Exchange operator.
   With `news` on a staging domain `staging-zone.example`:

   ```hcl
   worker_hostname = "news.staging-zone.example"
   provider_domain = "news.staging-zone.example"
   exchange_url    = "https://exchange.staging-zone.example"
   exchanges_json  = "[{\"domain\":\"exchange.staging-zone.example\",\"endpoint\":\"https://exchange.staging-zone.example\",\"supported_profiles\":[\"fora-news-v1\",\"fora-comp-v1\",\"fora-music-v1\"],\"ext\":{\"resource_owner_id\":\"staging-resource-owner\"}}]"
   origin_url      = "https://origin.staging-zone.example"
   ```

   (`route_patterns` and `exchange_wba_url` derive automatically;
   `resource_owner_id` must match staging's `resource_owner_id` tfvar — the
   Exchange refuses a push whose attested payee is not the registered one.)
   Then `terraform apply` as usual.

3. **Onboard the publisher** in the staging Exchange — the same step an
   Exchange operator performs for a real one. It registers the site and lists it
   on the existing seat, so pass the hostname and, if it differs from staging's
   own, the payee id:

   ```bash
   PUBLISHER_DOMAIN=news.staging-zone.example \
   deploy/terraform/scripts/seed-staging.sh
   ```

4. **Verify** — fund the smoke agent if not yet done
   (`deploy/terraform/scripts/fund-staging-agent.sh`), then run the full
   proof against the publisher hostname:

   ```bash
   PUBLISHER_DOMAIN=news.staging-zone.example \
   deploy/terraform/scripts/smoke.sh
   ```

Notes: pick a hostname that is NOT `demo.<domain>` (staging owns that name
whenever its own worker is on, and two stacks must never manage the same
hostname). Leave `create_waf_skip_rule = false` here if the staging stack
already manages the zone's custom-firewall ruleset — a zone can hold only one.
