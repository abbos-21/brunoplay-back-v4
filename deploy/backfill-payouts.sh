#!/usr/bin/env bash
# Post every payout that was paid BEFORE the payouts channel existed (paid TON withdrawals and delivered star withdrawals), oldest first.
#   ./backfill-payouts.sh            only counts them (a dry run: nothing is written)
#   ./backfill-payouts.sh --apply    queues the posts; the worker sends one every 5 seconds, each with the date of its payout
# Needs Settings -> payouts.channel to be set and the bot to be an admin of that channel. Safe to run again: a payout is never posted twice.
set -Eeuo pipefail
cd "$(dirname "$0")"
docker compose run --rm api payouts-backfill "$@"
