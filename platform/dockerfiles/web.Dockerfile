# A web frontend (runtime "web", from urbex.yaml's frontend): built once
# per commit of main; the image holds the built site and what deploys it
# to a Cloudflare Worker with static assets. Its Stack, one per
# environment, runs it: it uploads the site to the environment's Worker on
# the environment's hostname, then stays up - healthy once the upload
# worked. See docs/decisions/0022-cloudflare-tunnel-access-workers.md in
# urbforge/urbex.
FROM docker.io/library/node:22 AS build
ARG BUILD_COMMAND
ARG OUTPUT_DIR
WORKDIR /src
COPY . .
RUN sh -c "$BUILD_COMMAND" \
 && test -d "$OUTPUT_DIR" \
 && mkdir -p /site && cp -r "$OUTPUT_DIR"/. /site/

FROM docker.io/library/node:22-alpine
RUN npm install -g --no-fund --no-audit wrangler@4.146.0
COPY --from=build /site /site
RUN cat > /usr/local/bin/urbex-deploy <<'URBEX_DEPLOY'
#!/bin/sh
set -eu
: "${WORKER_NAME:?}" "${WORKER_HOSTNAME:?}" "${CLOUDFLARE_API_TOKEN:?}" "${CLOUDFLARE_ACCOUNT_ID:?}"
rm -f /tmp/deployed
cat > /tmp/wrangler.json <<CONFIG
{
  "name": "${WORKER_NAME}",
  "compatibility_date": "2026-09-01",
  "assets": { "directory": "/site", "not_found_handling": "single-page-application" },
  "routes": [{ "pattern": "${WORKER_HOSTNAME}", "custom_domain": true }],
  "workers_dev": false
}
CONFIG
cd /tmp
HOME=/tmp wrangler deploy --config /tmp/wrangler.json
touch /tmp/deployed
echo "Deployed ${WORKER_NAME} on https://${WORKER_HOSTNAME}"
exec sleep infinity
URBEX_DEPLOY
RUN chmod +x /usr/local/bin/urbex-deploy
ENTRYPOINT ["/usr/local/bin/urbex-deploy"]
