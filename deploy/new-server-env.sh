#!/usr/bin/env bash
# Creates .env and deploy/.env on a new server: random secrets, the database password in both files, and your two addresses.
# The secrets are never printed. What only you know (bot token, wallet words, ...) is left empty for you to fill in.
#
#   ./deploy/new-server-env.sh brunoplay.duckdns.org brunoplay-admin.duckdns.org
set -euo pipefail
cd "$(dirname "$0")/.."

APP="${1:?give the address of the game, e.g. brunoplay.duckdns.org}"
ADMIN="${2:?give the address of the admin panel, e.g. brunoplay-admin.duckdns.org}"
BOT_USER="${BOT_USERNAME:-bruno_play_bot}"
APP_NAME="${BOT_APP_NAME:-play}"

if [ -e .env ] || [ -e deploy/.env ]; then
  echo ".env or deploy/.env already exists. Remove both first if you really want to start over."
  exit 1
fi
cp .env.example .env
cp deploy/.env.deploy.example deploy/.env
chmod 600 .env deploy/.env

# set_kv FILE KEY VALUE  (VALUE must not contain | or a line break)
set_kv() {
  local f="$1" k="$2" v="$3"
  if grep -q "^${k}=" "$f"; then
    sed -i "s|^${k}=.*|${k}=${v}|" "$f"
  else
    echo "${k}=${v}" >> "$f"
  fi
}

DB="$(openssl rand -hex 24)"
set_kv .env APP_ENV production
set_kv .env DATABASE_URL "postgres://brunoplay:${DB}@postgres:5432/brunoplay?sslmode=disable"
set_kv .env JWT_SECRET "$(openssl rand -hex 32)"
set_kv .env ADMIN_JWT_SECRET "$(openssl rand -hex 32)"
set_kv .env BOT_WEBHOOK_SECRET "$(openssl rand -hex 32)"
set_kv .env ADSGRAM_WEBHOOK_SECRET "$(openssl rand -hex 32)"
set_kv .env BOT_USERNAME "$BOT_USER"
set_kv .env BOT_APP_NAME "$APP_NAME"
set_kv .env PUBLIC_API_URL "https://${APP}"
set_kv .env WEB_APP_URL "https://${APP}"
set_kv .env CORS_ORIGINS "https://${APP},https://${ADMIN}"
set_kv .env TRUSTED_PROXY true
set_kv .env DEV_AUTH false
set_kv .env AUTO_PAYOUT false
set_kv .env PAYOUT_WALLET_VERSION v5r1

set_kv deploy/.env POSTGRES_PASSWORD "$DB"
set_kv deploy/.env COMPOSE_PROFILES ""
set_kv deploy/.env API_PORT 8085
set_kv deploy/.env APP_DOMAIN "$APP"
set_kv deploy/.env ADMIN_DOMAIN "$ADMIN"
set_kv deploy/.env WWW_DIR /opt/brunoplay/www

# a small server (under 3 GB of RAM) gets smaller database memory and fewer connections, so deploys and peaks do not run it dry
mem_mb="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)"
if [ "${mem_mb:-0}" -gt 0 ] && [ "$mem_mb" -lt 3000 ]; then
  set_kv deploy/.env PG_SHARED_BUFFERS 384MB
  set_kv deploy/.env PG_CACHE_SIZE 1GB
  set_kv .env DB_MAX_CONNS 10
  echo "Small server (${mem_mb} MB of RAM): database memory 384 MB, 10 connections per program."
fi

echo "Created .env and deploy/.env (permissions 600). Generated: the database password, JWT_SECRET, ADMIN_JWT_SECRET,"
echo "BOT_WEBHOOK_SECRET and ADSGRAM_WEBHOOK_SECRET (new values: update the Reward URL in Adsgram with the new secret)."
echo
echo "Still empty, fill them with:  nano .env"
for k in BOT_TOKEN ALERT_CHAT_ID TON_API_KEY PAYOUT_WALLET_SEED; do
  printf '  %-20s %s\n' "$k" "$(grep -q "^${k}=.\+" .env && echo "(filled)" || echo "<- empty")"
done
