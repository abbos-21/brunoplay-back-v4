# Backups

The database holds everything that matters (balances, the ledger, withdrawals), so it is backed up on purpose in the simplest way that can be proven to work: **a compressed dump of the whole database every hour**, kept on the server and copied off it.

* **Most you can lose:** up to one hour. (A heavier setup with continuous archiving can bring that to a minute; it can be added later, when real money flows. Do not add it before this one works.)
* **What runs:** `deploy/backup.sh`, from cron, at minute 07 of every hour. It dumps the database, checks that the dump can be read and lists enough tables, keeps the last 48 hourly dumps and the first dump of each of the last 14 days, copies the dump to an off-server repository, and messages you (`ALERT_CHAT_ID`) if anything fails or a disk goes above 80%.
* **Before every deploy:** `deploy.sh` runs `backup.sh --quick`, a dump that takes seconds.
* **Why it cannot grow without limit:** the database does not archive its transaction log; the dumps are pruned by the script; Docker logs rotate.

## Set it up (once, about 15 minutes)

1. **Install restic** (the tool that copies dumps off the server, encrypted):
   ```bash
   sudo apt install -y restic
   ```
2. **Choose the off-server place** and put its address in `deploy/.env`. Any of these works; the data is small (well under a gigabyte for a long time):

   | Place | `RESTIC_REPOSITORY` | Extra lines in `deploy/.env` |
   |---|---|---|
   | Backblaze B2 (about $0.006 per GB a month, 10 GB free) | `b2:YOUR-BUCKET:brunoplay` | `B2_ACCOUNT_ID=...` and `B2_ACCOUNT_KEY=...` (an application key limited to that bucket) |
   | Another server you own | `sftp:USER@HOST:/backups/brunoplay` | none; the server's SSH key must be allowed there |
   | Any S3 storage (Wasabi, AWS...) | `s3:https://ENDPOINT/YOUR-BUCKET/brunoplay` | `AWS_ACCESS_KEY_ID=...` and `AWS_SECRET_ACCESS_KEY=...` |
   | Hetzner Storage Box | `sftp:uXXXXXX@uXXXXXX.your-storagebox.de:brunoplay` | none; add the server's SSH key to the box |

3. **Give the repository a password** (it encrypts everything; without it the copies cannot be read, so keep a second copy of this password OFF the server, for example in a password manager):
   ```bash
   openssl rand -base64 32 > ~/.brunoplay-restic-password && chmod 600 ~/.brunoplay-restic-password
   ```
   and in `deploy/.env`: `RESTIC_PASSWORD_FILE=/home/YOUR-USER/.brunoplay-restic-password`
4. **Run it once by hand** and look at the result:
   ```bash
   cd ~/bruno-stuff/brunoplay-back-v4/deploy && ./backup.sh
   ```
   It should end with `copied off the server` and `backup finished`.
5. **Prove that a restore works** (this is the step that makes it a backup):
   ```bash
   ./restore-drill.sh             # the newest dump on this server
   ./restore-drill.sh --offsite   # the newest copy from the off-server repository
   ```
   Both must end with `restore drill passed`. Repeat the drill every month and after any change to this setup.
6. **Turn on the hourly job:**
   ```bash
   ./install-backup-cron.sh
   ```
   Its output goes to `~/brunoplay-backup.log`.

Until `RESTIC_REPOSITORY` is set the backup still runs, and the bot tells you once a day that the copies exist only on this server.

## Restore for real (the server or the data is lost or damaged)

Take the newest good dump: from `~/brunoplay-backups/hourly/`, or from the off-server repository with `restic restore latest --tag brunoplay --target /tmp/restore`.

```bash
cd ~/bruno-stuff/brunoplay-back-v4/deploy
docker compose stop api worker                       # nothing may write while you restore
docker compose exec -T postgres psql -U brunoplay -d postgres \
  -c "drop database brunoplay with (force)" -c "create database brunoplay owner brunoplay"
docker compose exec -T postgres pg_restore -U brunoplay -d brunoplay --no-owner --exit-on-error < /path/to/brunoplay-XXXX.dump
docker compose up -d api worker
```

On a brand-new server: set it up as in `DEPLOY-NGINX.md` up to the first start, then do the same restore.

**Before you switch automatic payouts back on after a restore:** the database may be up to an hour behind the TON wallet. Open the payout wallet's history in an explorer and compare it with Admin, Withdrawals: a withdrawal paid on the network but missing here must be marked as completed (with its hash), never paid again.

## Looking at it

| What | How |
|---|---|
| Is it running? | `tail -n 20 ~/brunoplay-backup.log`: one "backup finished" per hour |
| What is stored here? | `ls -lh ~/brunoplay-backups/hourly ~/brunoplay-backups/daily` |
| What is stored off the server? | `cd deploy && set -a && . ./.env && set +a && restic snapshots --tag brunoplay` |
| Did the nightly check pass? | The 03:07 run prints nothing unusual; a failure messages you |
