#!/usr/bin/env bash
# Puts the hourly backup in this user's crontab. Safe to run again: it replaces its own line and touches nothing else.
set -euo pipefail
dir="$(cd "$(dirname "$0")" && pwd)"
line="7 * * * * cd $dir && ./backup.sh >> \$HOME/brunoplay-backup.log 2>&1 # brunoplay-backup"
{ crontab -l 2>/dev/null | grep -v '# brunoplay-backup' || true; echo "$line"; } | crontab -
echo "installed:"
crontab -l | grep '# brunoplay-backup'
