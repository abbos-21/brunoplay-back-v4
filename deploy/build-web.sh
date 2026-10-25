#!/usr/bin/env bash
# Build the game (Mini App) and the admin panel and put them where Caddy serves them. Node runs inside Docker, so nothing
# has to be installed on the server. Run it after every change to either app; no restart is needed.
#   ./build-web.sh            build both
#   ./build-web.sh app        only the game
#   ./build-web.sh admin      only the admin panel
# The apps are looked for next to this repository (../../brunoplay-front-v3 and ../../brunoplay-admin-v4);
# set FRONT_DIR / ADMIN_DIR to put them somewhere else.
set -euo pipefail
cd "$(dirname "$0")"
[ -f .env ] || { echo "deploy/.env is missing (copy .env.deploy.example)"; exit 1; }
set -a; . ./.env; set +a
: "${APP_DOMAIN:?set APP_DOMAIN in deploy/.env}"
WWW="${WWW_DIR:-/opt/brunoplay/www}"
FRONT_DIR="${FRONT_DIR:-../../brunoplay-front-v3}"
ADMIN_DIR="${ADMIN_DIR:-../../brunoplay-admin-v4}"
VERSION="$(git -C .. rev-parse --short HEAD 2>/dev/null || date -u +%Y%m%d%H%M)"
only="${1:-both}"

build() { # name dir
  local name="$1" dir
  dir="$(cd "$2" && pwd)" || { echo "$2 not found"; exit 1; }
  echo "== building $name from $dir"
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e VITE_APP_VERSION="$VERSION" -v "$dir":/w -w /w node:22-alpine \
    sh -c "npm ci --no-audit --no-fund && npm run build"
  mkdir -p "$WWW/$name"
  # replace the contents in place (Caddy has this directory mounted, so the directory itself must stay)
  find "$WWW/$name" -mindepth 1 -delete
  cp -r "$dir/dist/." "$WWW/$name/"
  echo "   -> $WWW/$name"
}

if [ "$only" = both ] || [ "$only" = app ]; then
  build app "$FRONT_DIR"
  # TON Connect needs a manifest that names this very address
  cat > "$WWW/app/tonconnect-manifest.json" <<JSON
{
  "url": "https://$APP_DOMAIN",
  "name": "BrunoPlay",
  "iconUrl": "https://$APP_DOMAIN/favicon.ico"
}
JSON
fi
if [ "$only" = both ] || [ "$only" = admin ]; then
  build admin "$ADMIN_DIR"
fi
echo "done: $VERSION"
