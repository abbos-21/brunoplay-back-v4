#!/usr/bin/env bash
# Put the very latest code live: pull the three repositories, then deploy the API, build the game and build the admin panel.
#
#   ./update-all.sh              everything: api + game + admin
#   ./update-all.sh back         only the API (backup, migration, restart)
#   ./update-all.sh app admin    any mix of: back  app  admin
#
# The repositories are looked for next to this one (../../brunoplay-front-v3, ../../brunoplay-admin-v4);
# set FRONT_DIR / ADMIN_DIR to put them somewhere else. Nothing is changed on the server until ALL the pulls have worked.
set -Eeuo pipefail
cd "$(dirname "$0")"

BACK_DIR="$(cd .. && pwd)"
FRONT_DIR="$(cd "${FRONT_DIR:-../../brunoplay-front-v3}" && pwd)" || { echo "the game repository was not found (set FRONT_DIR)"; exit 1; }
ADMIN_DIR="$(cd "${ADMIN_DIR:-../../brunoplay-admin-v4}" && pwd)" || { echo "the admin repository was not found (set ADMIN_DIR)"; exit 1; }

want() { # does the list of parts to update include this one?
  [ "$#" -eq 0 ] && return 1
  local part="$1"; shift
  [ "${PARTS:-all}" = all ] && return 0
  case " $PARTS " in *" $part "*) return 0 ;; esac
  return 1
}
if [ "$#" -gt 0 ]; then
  for a in "$@"; do
    case "$a" in back|app|admin) ;; *) echo "unknown part '$a' (use: back app admin)"; exit 1 ;; esac
  done
  PARTS="$*"
else
  PARTS=all
fi

declare -A before after
pull() { # name dir
  local name="$1" dir="$2"
  before[$name]="$(git -C "$dir" rev-parse --short HEAD)"
  if [ -n "$(git -C "$dir" status --porcelain --untracked-files=no)" ]; then
    echo "!! $name has local changes on this server ($dir). Commit or discard them first (git status):"
    git -C "$dir" status --short --untracked-files=no | head
    exit 1
  fi
  echo "== pulling $name"
  git -C "$dir" pull --ff-only
  after[$name]="$(git -C "$dir" rev-parse --short HEAD)"
}

# 1. every pull first: if one fails, nothing was deployed yet
want back x && pull api "$BACK_DIR"
want app x && pull game "$FRONT_DIR"
want admin x && pull admin "$ADMIN_DIR"

# 2. the API first (backup, migration, restart, health check; it rolls itself back if it does not come up),
#    then the web apps: they talk to the new API, so they must not go live before it
if want back x; then
  echo; echo "== deploying the API"
  ./deploy.sh
fi
if want app x; then echo; ./build-web.sh app; fi
if want admin x; then echo; ./build-web.sh admin; fi

echo
echo "== updated"
for n in api game admin; do
  [ -n "${before[$n]:-}" ] && echo "   $n: ${before[$n]} -> ${after[$n]}"
done
echo "   Players: fully close and reopen the mini app. Admin: reload the page (Ctrl+F5)."
