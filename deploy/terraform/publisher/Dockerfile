# Demo publisher origin for the staging stack: nginx + the demo content trees
# the e2e stack serves via volume mounts. Staging has no bind mounts (the VM
# only pulls images), so the content is baked in.
#
# Build from the repository root:
#   docker build -f deploy/terraform/publisher/Dockerfile -t fora/publisher:dev .

FROM nginx:1.27-alpine

# The same nginx config the local e2e compose mounts: one server that routes
# the two content trees below by path prefix. It also listens on the e2e
# stack's per-publisher ports, whose discovery documents only that stack
# mounts; this image carries none, so on those ports both document addresses
# answer 404.
COPY deploy/publisher/nginx.conf /etc/nginx/nginx.conf

# Baseline mock-publisher tree at the default docroot (/premium, /public,
# /paid, /.well-known — article-42, licensed-paper, ...). Its fora.json is the
# e2e stack's mock document, which names an Exchange no deployment runs. The
# deployment's Caddy answers /.well-known/fora.json on the origin hostname with
# the deployment's own document, so a request for it never reaches this image
# (deploy/terraform/modules/compose-stack, origin_discovery_document).
COPY tests/e2e/mock-publisher/content /usr/share/nginx/html

# Demo publisher trees (stoa-press, harmonia-records, foleyworks) under their
# own docroot, reached via the /articles, /lyrics, /tracks, /sfx prefixes.
# The same tree carries index.html, the human-facing landing page nginx
# serves at the site root.
COPY deploy/content/demo /usr/share/nginx/demo
