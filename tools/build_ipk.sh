#!/usr/bin/env bash
# Package the TV app with the real server addresses substituted for the placeholders in app/js/api.js.
# Reads TORRSERVER_BASE and SIDECAR_BASE from local.env (git-ignored). The source tree is left untouched.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$HOME/.local/node/bin:$PATH"
[ -f local.env ] && . ./local.env
: "${TORRSERVER_BASE:?set TORRSERVER_BASE=http://<host>:8090 in local.env}"
: "${SIDECAR_BASE:?set SIDECAR_BASE=http://<host>:8791 in local.env}"
tools/es5check.sh
STAGE=$(mktemp -d); trap 'rm -rf "$STAGE"' EXIT
cp -r app "$STAGE/app"
sed -i "s#http://TORRSERVER_HOST:8090#${TORRSERVER_BASE}#; s#http://SIDECAR_HOST:8791#${SIDECAR_BASE}#" "$STAGE/app/js/api.js"
grep -q "_HOST:" "$STAGE/app/js/api.js" && { echo "placeholder still present" >&2; exit 1; }
rm -rf dist && ares-package "$STAGE/app" -o dist | tail -1
ls dist
