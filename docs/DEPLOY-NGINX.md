# Backend on a VPS that already runs another site (nginx + DuckDNS + certbot)

This puts only the **backend** online: PostgreSQL, the API and the worker run in Docker, and the nginx that is already on the
server forwards `https://<name>.duckdns.org` to the API. Nothing of the other site (`recruting.uz`) is touched: the API listens on
`127.0.0.1:8085` only, so it cannot clash with its ports, and the database is not reachable from outside at all.

```
Internet ──► nginx (80/443, already there) ──► 127.0.0.1:8085 ──► API ──► PostgreSQL (inside Docker)
                                                                   worker (jobs, bot messages, TON watcher)
```

Plan about 45 minutes. Every command is typed on the server unless it says "on your PC". Each grey box can be copied and pasted as a whole, except where a line says otherwise (the editor `nano` and the password prompts need their own paste).

## 0. Look at what the server already has (5 min)

Nothing here changes anything. Send me the output if anything looks different from what is described.

```bash
lsb_release -ds; free -h | head -2; df -h / | tail -1          # system, memory, free disk
nginx -v; sudo nginx -T 2>/dev/null | grep -E "server_name|listen" | sort -u    # nginx and the sites it serves
docker --version; docker compose version                        # Docker installed? (if not: step 2)
certbot --version                                               # certbot installed? (if not: step 6)
sudo ss -ltnp | grep -E ':(80|443|5432|8085) '                  # who uses these ports
```

* Port **8085** must be free. If something uses it, pick another number and use it everywhere below instead of 8085.
* A PostgreSQL already running on the server (port 5432) is fine: ours runs inside Docker and is not published.
* You need about 1.5 GB of free memory and 5 GB of free disk. If memory is tight, set `PG_SHARED_BUFFERS=128MB` in step 4.

## 1. Get a DuckDNS name (3 min)

1. Open <https://www.duckdns.org>, sign in, and create a subdomain, for example `brunoplay-api` (it becomes `brunoplay-api.duckdns.org`).
2. Set its **current ip** to the server's public IPv4 address: `curl -4 ifconfig.me` on the server shows it. Press *update ip*.
3. Check from the server: `dig +short brunoplay-api.duckdns.org` must print that same IP (it can take a minute).

The server has a fixed address, so you do not need DuckDNS's update script. Certbot will use the normal HTTP check, so the DuckDNS token is not needed either.

## 2. Docker (skip if step 0 showed it)

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER && newgrp docker
docker run --rm hello-world | head -3
```

## 3. Get the code (5 min)

The repository is private, so give the server a read-only key:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/brunoplay_deploy -N "" -C "brunoplay-vps"
cat ~/.ssh/brunoplay_deploy.pub
```

On GitHub open the repository `brunoplay-back-v4` → **Settings → Deploy keys → Add deploy key**, paste the line, leave "Allow write access" **off**.

```bash
cat >> ~/.ssh/config <<'EOF'
Host github-brunoplay
  HostName github.com
  User git
  IdentityFile ~/.ssh/brunoplay_deploy
  IdentitiesOnly yes
EOF
sudo mkdir -p /opt/brunoplay && sudo chown $USER:$USER /opt/brunoplay
git clone git@github-brunoplay:abbos-21/brunoplay-back-v4.git /opt/brunoplay/brunoplay-back-v4
cd /opt/brunoplay/brunoplay-back-v4 && git log --oneline | head -3
```

## 4. Configure (10 min)

```bash
cd /opt/brunoplay/brunoplay-back-v4
cp .env.example .env && cp deploy/.env.deploy.example deploy/.env
chmod 600 .env deploy/.env
openssl rand -hex 32      # run it 4 times and keep the 4 values: JWT_SECRET, ADMIN_JWT_SECRET, BOT_WEBHOOK_SECRET, ADSGRAM_WEBHOOK_SECRET
openssl rand -hex 24      # the database password
nano .env
```

Fill in `.env` (leave the other lines as they are):

| Key | Value |
|---|---|
| `APP_ENV` | `production` |
| `DATABASE_URL` | `postgres://brunoplay:<database password>@postgres:5432/brunoplay?sslmode=disable` |
| `JWT_SECRET` | first random value |
| `ADMIN_JWT_SECRET` | second random value (must differ) |
| `BOT_TOKEN` | a **new** token from @BotFather: open the bot, *API Token → Revoke current token*. The old one was pasted in chat and must not be used |
| `BOT_USERNAME` | the bot's name without `@` |
| `BOT_WEBHOOK_SECRET` | third random value |
| `ADSGRAM_WEBHOOK_SECRET` | fourth random value |
| `PUBLIC_API_URL` | `https://brunoplay-api.duckdns.org` |
| `WEB_APP_URL` | for now the same address (change it when the game has its own address) |
| `CORS_ORIGINS` | for now the same address (later add the game's and the admin panel's addresses, comma separated) |
| `TRUSTED_PROXY` | `true` |
| `DEV_AUTH` | `false` (the server refuses to start if it is on) |
| `AUTO_PAYOUT` | `false` for now (see section 10) |
| `TON_API_KEY` | a free key from the toncenter bot @tonapibot (optional now, needed for TON top-ups) |
| `ALERT_CHAT_ID` | your own Telegram id, so the bot can alert you. Send the bot `/start`, then ask @userinfobot for your id |

```bash
nano deploy/.env
```
Set `POSTGRES_PASSWORD` to the same database password, leave `COMPOSE_PROFILES=` **empty** (that keeps Caddy off, nginx is used) and `API_PORT=8085`.
If memory is tight add `PG_SHARED_BUFFERS=128MB`. Leave the backup lines for now (section 9).

## 5. Start the backend (10 min)

```bash
cd /opt/brunoplay/brunoplay-back-v4/deploy
./deploy.sh
```

It builds the image (a few minutes the first time), makes a database, applies the migrations, starts the API and the worker and waits until the API is healthy.
At the end it prints `healthy: <version>`. If it does not, run `docker compose logs api` and send me the last lines.

```bash
docker compose ps                         # postgres, api, worker: all "Up" / "healthy"
curl -s http://127.0.0.1:8085/readyz      # {"status":"ready", ...}
```

## 6. nginx and the certificate (10 min)

First an HTTP-only site, so certbot can prove you own the name:

```bash
sudo nano /etc/nginx/sites-available/brunoplay-api
```
```nginx
server {
    listen 80;
    listen [::]:80;
    server_name brunoplay-api.duckdns.org;

    client_max_body_size 2m;

    location / {
        proxy_pass http://127.0.0.1:8085;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        # the API trusts the FIRST address of X-Forwarded-For, so it is set (replaced), never appended to
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 30s;
    }
}
```
```bash
sudo ln -s /etc/nginx/sites-available/brunoplay-api /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
curl -s http://brunoplay-api.duckdns.org/readyz        # works over plain HTTP already
```

Then the certificate (install certbot first if step 0 said it is missing: `sudo apt -y install certbot python3-certbot-nginx`):

```bash
sudo certbot --nginx -d brunoplay-api.duckdns.org --redirect -m you@yourmail.com --agree-tos -n
sudo certbot renew --dry-run                           # renewal is automatic; this proves it works
curl -s https://brunoplay-api.duckdns.org/readyz
curl -s https://brunoplay-api.duckdns.org/api/app-info
```

The bot's webhook is registered only when the API starts, and it needs working HTTPS. The API was started before that, so restart it once now:

```bash
cd ~/Desktop/bruno-stuff/brunoplay-back-v4/deploy && docker compose restart api && sleep 8 && docker compose logs api | grep -i "bot setup" || echo "bot setup: no error logged"
```

Certbot rewrites that file to add the HTTPS part and the redirect; it does not touch the `recruting.uz` sites.
Telegram and Adsgram can only call a valid HTTPS address, which is why this step matters.

## 7. Create the admin account (2 min)

```bash
cd /opt/brunoplay/brunoplay-back-v4/deploy
docker compose run --rm -e ADMIN_PASSWORD='a-long-unique-password' api admin-create --email you@yourmail.com
```

Keep that password in a password manager. The admin panel itself is not online yet; the API accepts the admin login already, and the next step uses it.

## 8. Load the economy (5 min, on your PC)

Needs Node 20 or newer and a copy of the repository on your PC.

```bash
cd "/c/Users/user/Desktop/bruno stuff/brunoplay-back-v4"
```
Paste this line, then type the admin password and press Enter (nothing is shown while you type). Replace `you@yourmail.com`, the wallet, the block id and the links in the commands below with your own values.
```bash
read -s -p "admin password: " ADMIN_PASSWORD; export ADMIN_PASSWORD
```
A dry run first: it shows what would be written and writes nothing.
```bash
node scripts/production-seed.mjs --api https://brunoplay-api.duckdns.org --email you@yourmail.com
```
The real run (add what you already have; whatever you leave out is listed at the end and can be added later by running it again):
```bash
node scripts/production-seed.mjs --api https://brunoplay-api.duckdns.org --email you@yourmail.com --apply \
    --wallet UQ...your_ton_wallet --block-id YOUR_ADSGRAM_BLOCK --channel @your_channel --chat @your_chat \
    --support https://t.me/your_support --news https://t.me/your_channel
```

It reads the live TON price, sets the coin and star rates from it, and writes all the economy values (`docs/ECONOMY.md`) and the starter tasks.
The last line must say `Still missing on the server: nothing`. Without the Adsgram block id nobody can start fishing yet (the server only believes Adsgram's own callback), which is fine while only the backend is online.
Run it again with `--apply` whenever TON moves more than about 20%.

## 9. Telegram bot, ads and backups

**The bot.** The API sets its own webhook when it starts. Check it (type the token yourself, do not paste it anywhere else):

```bash
read -s -p "bot token: " T; echo; curl -s "https://api.telegram.org/bot$T/getWebhookInfo"; unset T
```
`url` must be `https://brunoplay-api.duckdns.org/api/webhooks/telegram` and `last_error_message` empty.

**Adsgram.** For each ad block set the reward URL to
`https://brunoplay-api.duckdns.org/api/webhooks/adsgram?userid=[userId]&blockid=<the block id>&secret=<ADSGRAM_WEBHOOK_SECRET from .env>`.

**Backups (do this today).** Follow `docs/BACKUPS.md`: hourly database dumps, a copy off the server, and a restore drill that proves they work.
The first start (`SKIP_BACKUP=1 ./deploy.sh`) has no database to back up yet; every start after that backs up first.

## 10. Automatic payouts: leave them off for now

The economy is set so payouts up to 0.075 TON are sent by the server, but that needs `AUTO_PAYOUT=true` and a payout wallet seed in `.env`, and that code has never been tried on the real network.
Until you have followed `docs/DEPLOY-VPS.md` section 3b (testnet first), keep `AUTO_PAYOUT=false`: every request then waits in the admin panel and you pay by hand.

## 11. Updating later

```bash
cd /opt/brunoplay/brunoplay-back-v4 && git pull
cd deploy && ./deploy.sh              # builds, backs up, migrates, restarts; rolls back by itself if the API does not become healthy
./deploy.sh rollback                  # go back to the previous version by hand
```

## 12. If something is wrong

| Symptom | Check |
|---|---|
| `deploy.sh` fails at the build | `docker compose build` output; disk full (`df -h`) |
| The API is "unhealthy" | `docker compose logs api` shows the reason; a missing or short secret in `.env` is the usual cause |
| `502 Bad Gateway` from nginx | The API is not running (`docker compose ps`), or `API_PORT` in `deploy/.env` differs from `proxy_pass` |
| Certbot says "connection refused / timeout" | DNS not pointing at the server yet (`dig`), or the firewall blocks port 80 (`sudo ufw status`) |
| Certbot says the name is not allowed | A typo in the DuckDNS name |
| `getWebhookInfo` shows an error | The address is not valid HTTPS yet, or `BOT_TOKEN` is wrong |
| "Too many requests" for everybody | nginx is not replacing `X-Forwarded-For` as above, so all users look like one address |

Logs: `cd /opt/brunoplay/brunoplay-back-v4/deploy && docker compose logs -f api worker` · nginx: `/var/log/nginx/error.log`.

## What comes next

When the backend works: the game and the admin panel (their own addresses and nginx sites, plus `CORS_ORIGINS`), Adsgram, the TON wallets,
automatic payouts on testnet, and the full backup. Tell me when you are ready for each.
