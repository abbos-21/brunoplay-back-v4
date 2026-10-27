# Deploying BrunoPlay to a VPS

One server runs everything: PostgreSQL, the API, the background worker, and Caddy (HTTPS, the game and the admin panel).
Plan about 2 hours the first time. Commands are for Ubuntu 24.04; they work the same on Debian 12.

```
Telegram ──► https://play.example.com   Caddy ──► game files   (the Mini App)
                                               └─► /api/*  ──► API ──► PostgreSQL
Adsgram, Telegram ──► https://play.example.com/api/webhooks/*          ▲
You ──► https://admin.example.com       Caddy ──► admin panel files ──► the same API
                                         worker (jobs, bot messages, TON watcher) ──┘
```

## 0. What you need before you start

| Thing | Notes |
|---|---|
| A VPS | 2 vCPU, 4 GB RAM, 40 GB SSD is enough for the first tens of thousands of players. Europe is fine. Hetzner, DigitalOcean, Contabo or similar. |
| A domain with two names | `play.<yourdomain>` and `admin.<yourdomain>`. Two DNS **A records** to the server's IP. (A free DuckDNS name works for testing but is a poor choice for a real launch.) |
| The bot | Make a **new** token with @BotFather (`/revoke` the old one: it was shared in chat and must not be used). |
| An Adsgram account | Create the ad blocks (section 7). |
| A TON wallet | A wallet you control (Tonkeeper etc.) that receives star top-ups. Write its address down. |
| A place for backups | An off-server S3-compatible bucket (Backblaze B2, Wasabi, Hetzner Storage Box...). Without it a dead server means a dead game. |
| A password manager | For the admin password, the database password and the restic password. |

## 1. Prepare the server (10 min)

```bash
ssh root@SERVER_IP
adduser deploy && usermod -aG sudo deploy          # then log in as deploy from now on, with an SSH key
# on your own PC:  ssh-copy-id deploy@SERVER_IP

sudo apt update && sudo apt -y upgrade
sudo apt -y install ufw fail2ban unattended-upgrades git rsync curl
sudo ufw allow OpenSSH && sudo ufw allow 80 && sudo ufw allow 443 && sudo ufw --force enable
# once your key works, turn off passwords:  sudo sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config && sudo systemctl restart ssh

curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker deploy && newgrp docker
sudo mkdir -p /opt/brunoplay && sudo chown deploy:deploy /opt/brunoplay
```

If the server has 2 GB of RAM or less, add `PG_SHARED_BUFFERS=256MB` to `deploy/.env` (step 3) and a swap file.

## 2. Put the code on the server (5 min)

The three projects sit next to each other in `/opt/brunoplay`:

```
/opt/brunoplay/brunoplay-back-v4     the API (this repository) and the deploy files
/opt/brunoplay/brunoplay-front-v3    the game
/opt/brunoplay/brunoplay-admin-v4    the admin panel
```

Easiest is a **private** GitHub repository for each (push from your PC, `git clone` on the server). Never push `.env` files: they are in `.gitignore`.
Today the game has changes that are not committed or pushed, the API repository has no commits yet, and the admin panel is not a git repository at all, so commit and push first (or use the copy method below).
Without GitHub, copy them from your PC (skip `node_modules`, `dist`, `bin`):

```bash
rsync -av --exclude node_modules --exclude dist --exclude bin --exclude .git --exclude '.env*' \
  "/c/Users/user/Desktop/bruno stuff/brunoplay-back-v4/"  deploy@SERVER_IP:/opt/brunoplay/brunoplay-back-v4/
# the same for brunoplay-front-v3 and brunoplay-admin-v4
```

## 3. Configure (10 min)

```bash
cd /opt/brunoplay/brunoplay-back-v4
cp .env.example .env && cp deploy/.env.deploy.example deploy/.env
openssl rand -hex 32          # run this four times: JWT_SECRET, ADMIN_JWT_SECRET, BOT_WEBHOOK_SECRET, ADSGRAM_WEBHOOK_SECRET
openssl rand -hex 24          # the database password
nano .env
nano deploy/.env
```

**`.env`** (the application):

| Key | Value |
|---|---|
| `APP_ENV` | `production` |
| `DATABASE_URL` | `postgres://brunoplay:<database password>@postgres:5432/brunoplay?sslmode=disable` (use the hex password: no symbols to escape) |
| `JWT_SECRET`, `ADMIN_JWT_SECRET` | two different random values |
| `BOT_TOKEN` | the **new** token |
| `BOT_USERNAME` | your bot's name without `@` |
| `BOT_WEBHOOK_SECRET`, `ADSGRAM_WEBHOOK_SECRET` | random values |
| `PUBLIC_API_URL`, `WEB_APP_URL` | `https://play.<yourdomain>` (both the same) |
| `CORS_ORIGINS` | `https://play.<yourdomain>,https://admin.<yourdomain>` |
| `TRUSTED_PROXY` | `true` |
| `DEV_AUTH` | `false` (the server refuses to start if it is on in production) |
| `AUTO_PAYOUT` | `true` sends payouts up to 0.075 TON by itself (needs `PAYOUT_WALLET_SEED`, step 3b). Start with `false` and pay by hand while you test, then switch on |
| `TON_API_KEY` | a free key from toncenter (the bot @tonapibot) so the top-up watcher is not rate limited |
| `ALERT_CHAT_ID` | your own chat id with the bot (send it `/start`, then ask @userinfobot for your id). You get alerts about payouts, errors and ledger mismatches |

**3b. Automatic payouts (the payout wallet).** The rules pay requests up to 0.075 TON by themselves from a separate "hot" wallet.
1. Make a **new** TON wallet used for nothing else (not your main one). Write its 24 words in your password manager.
2. **Test first on testnet**: set `TON_TESTNET=true`, fund the wallet from a testnet faucet, and make small test withdrawals until you have seen several complete, with the right transaction hash. This code moves real money and could not be tried on a live network when it was built.
3. Then switch to the real network (`TON_TESTNET=false`) and put only a small float in the wallet, for example 20 TON. The daily cap in the settings is 10 TON, so a bug or an attack can never send more than that in a day.
4. In `.env` on the server: `AUTO_PAYOUT=true` and `PAYOUT_WALLET_SEED=word1 word2 ... word24`. Never anywhere else. Then `./deploy.sh`.
5. A payout that gets stuck or cannot be confirmed is not re-sent; the bot alerts you and it waits in Admin → Withdrawals (see `docs/OPERATIONS.md`, "needs review").
6. Top the wallet up when the alerts say it is low; if it is empty, requests simply wait for you.

**`deploy/.env`** (Docker): `POSTGRES_PASSWORD` (the same hex password), `APP_DOMAIN=play.<yourdomain>`, `ADMIN_DOMAIN=admin.<yourdomain>`, and the backup repository (step 9).

## 4. Build and start (10 min)

```bash
cd /opt/brunoplay/brunoplay-back-v4/deploy
./build-web.sh        # builds the game and the admin panel with Node in Docker, nothing to install
./deploy.sh           # builds the API, migrates the database, starts everything, waits until healthy
```

Check: `docker compose ps` shows `api`, `worker`, `postgres`, `caddy` running, and `curl https://play.<yourdomain>/readyz` answers `{"status":"ready"}`.
Caddy gets the HTTPS certificates by itself the first time (the DNS records must already point at the server). The bot's webhook is registered when the API starts.

## 5. Create the admin (2 min)

```bash
docker compose -f /opt/brunoplay/brunoplay-back-v4/deploy/docker-compose.yml run --rm \
  -e ADMIN_PASSWORD='a-long-password-only-you-know' api admin-create --email you@yourmail.com
```

Use a long password from your password manager; it is not stored anywhere in clear text. Open `https://admin.<yourdomain>` and sign in.
Add a second admin with the **Finance** role only for people who pay out, and give **Support** to people who answer players.

## 6. Load the economy (5 min)

From your own PC (it needs Node 20+; the script is in the repository):

```bash
cd "/c/Users/user/Desktop/bruno stuff/brunoplay-back-v4"
read -s -p "admin password: " ADMIN_PASSWORD; export ADMIN_PASSWORD

# 1) look first; nothing is written
node scripts/production-seed.mjs --api https://admin.<yourdomain> --email you@yourmail.com \
  --wallet UQ...your_ton_wallet --block-id YOUR_ADSGRAM_BLOCK --channel @your_channel --chat @your_chat \
  --support https://t.me/your_support --news https://t.me/your_channel

# 2) happy with it? add --apply
node scripts/production-seed.mjs ... --apply
```

It reads the live TON price and sets all 76 values (see `docs/ECONOMY.md`): coin and star rates, cards, boosters, games, withdrawals, bot timing, plus the starter tasks.
It never invents the wallet, the Adsgram blocks or the links: whatever you leave out is printed at the end. It is safe to run again. Run it again with `--apply` whenever TON moves a lot.
The final line should say `Still missing on the server: nothing`.

Look at everything once in the admin panel under **Settings**, and change anything you disagree with there: every change is saved with its history.

## 7. Adsgram (10 min)

In the Adsgram publisher dashboard, create a block for your app (one block for all placements is fine to start; separate blocks give you better numbers per placement).
For each block set the **reward URL** to

```
https://play.<yourdomain>/api/webhooks/adsgram?userid=[userId]&blockid=<the block id>&secret=<ADSGRAM_WEBHOOK_SECRET from .env>
```

Then give the block ids to the seed (`--block-id`, or `--block-ids '{"FISHING_START":"1","COLLECT":"2",...}'`).
Without a working reward URL nobody can start fishing, because the server only believes Adsgram, never the phone.

## 8. Telegram (10 min)

In @BotFather, for your bot:

1. `/mybots` → your bot → **Bot Settings → Menu Button → Configure menu button** → URL `https://play.<yourdomain>` → title `Play`.
2. For a shareable link `t.me/<bot>/app`: `/newapp` (or **Bot Settings → Configure Mini App**), choose the same URL.
3. `/setdescription`, `/setabouttext`, `/setuserpic` for the bot page.
4. Payments with Telegram Stars need nothing extra: the bot creates the invoices.
5. Open the bot in Telegram with a **different account than the owner's**, press Start, and play (checklist below).

Open `https://play.<yourdomain>` in a normal browser to check: it should show the "open this in Telegram" screen, not a blank page.

## 9. Backups (15 min). Do not skip

Follow `docs/BACKUPS.md`: install restic, choose the off-server place, run `./backup.sh` once, run `./restore-drill.sh` and `./restore-drill.sh --offsite` (both must pass), then `./install-backup-cron.sh`.

## 10. Before you tell anyone (30 min)

With your second Telegram account, check each of these:

- [ ] The game opens inside Telegram and shows your name.
- [ ] **Fishing**: the start ad plays, fishing starts; a bit later Collect works after the second ad and the balance goes up.
- [ ] The admin shows the ad views as completed (Adsgram's callback reached the server).
- [ ] **Daily reward**, **combo**, **bonus ad** work; the **menu** opens; Russian text works.
- [ ] Buy the smallest pack with Telegram Stars: the balance rises, and the payment is listed in Admin → Top-ups. Then refund it in Telegram and see the stars removed.
- [ ] Buy a small pack with TON (a tiny amount) and see it credited after the wait.
- [ ] Upgrade a card; level up; spin once; open a chest.
- [ ] Ask a friend to open your invite link: they appear under Friends.
- [ ] Earn enough to try a withdrawal in a test, or temporarily lower `withdrawal.min_coins`, and complete the whole flow: request → Admin → Withdrawals → Processing → send the TON from your wallet → Complete with the hash. **Put the setting back afterwards.**
- [ ] Switch `maintenance.enabled` on and off: players see the "back soon" screen.
- [ ] Block and unblock a test player.
- [ ] A backup ran and the drill passed.

## 11. Launch week

* Announce small first (a few hundred people), watch the admin dashboard, then grow.
* Daily for the first two weeks: Admin → Dashboard, Withdrawals, Risk, and the alerts in your chat. Compare with section 5 of `docs/ECONOMY.md` and change settings if the real numbers differ from the assumed ones.
* Automatic payouts are on for requests up to 0.075 TON (see 3b). Look at every payout in Admin → Withdrawals for the first weeks, pay the bigger ones by hand, and watch the hot wallet balance.

## 12. Updating and rolling back

```bash
cd /opt/brunoplay/brunoplay-back-v4 && git pull          # (and the other two repositories)
cd deploy
./build-web.sh app          # the game changed          (./build-web.sh admin for the panel)
./deploy.sh                 # the API changed: builds, backs up, migrates, restarts, rolls back by itself if it does not become healthy
./deploy.sh rollback        # go back to the previous API image by hand
```

Game files are replaced in place, so players get the new version the next time they open the game. To make everyone reload at once after an incompatible change, raise the setting `app.min_version` (and build the game with the same version in `VITE_APP_VERSION`).

## 13. Security checklist

- [ ] The old bot token (the one that was shared in chat) is revoked and `.env` holds the new one.
- [ ] `DEV_AUTH=false`, `APP_ENV=production`. `PAYOUT_WALLET_SEED` is only in `.env` on the server and the wallet holds only a small float.
- [ ] `.env` and `deploy/.env` are `chmod 600`, owned by `deploy`, and not in git. `git status` shows neither.
- [ ] Password login is off for SSH; `ufw status` shows only 22, 80 and 443 open; `fail2ban` runs.
- [ ] The admin password is long and unique. Only people who need it have the Finance role.
- [ ] Optional: in `deploy/Caddyfile`, restrict the admin site to your own IP (the commented lines).
- [ ] The database port is not published (it is not: only Caddy has ports).
- [ ] Alerts arrive in your chat (send a test by stopping the worker for 10 minutes, then start it again).

## 14. When something is wrong

| Symptom | Look at |
|---|---|
| The site does not open / certificate error | DNS records point at the server? `docker compose logs caddy` |
| "Server unreachable" screen in the game | `curl https://play.<yourdomain>/readyz`; `docker compose logs api` |
| Fishing will not start (ad shows but nothing happens) | The Adsgram reward URL or `ADSGRAM_WEBHOOK_SECRET` is wrong: Admin → ad views stay "pending"; `docker compose logs api` for the webhook lines |
| Stars paid with Telegram do not arrive | `docker compose logs api` for `successful_payment`; is the webhook registered (`BOT_TOKEN`, `PUBLIC_API_URL`)? |
| TON top-up stays pending | `TON_API_KEY` set? `docker compose logs worker`; check the order in Admin → Top-ups |
| No bot messages | `docker compose logs worker`; players switched them off; quiet hours (20:00–04:00 UTC) |
| "Ledger mismatch" alert | Do not pay anything out. Admin → Ledger check. This must never happen: keep the logs and the latest backup |
| Disk filling up | `docker system df` and `du -sh ~/brunoplay-backups`; the dumps are pruned by `backup.sh` and Docker logs rotate |

Logs: `docker compose logs -f api worker`. Everything is in `docs/OPERATIONS.md`, `docs/SECURITY.md` and `docs/SETTINGS.md`.
