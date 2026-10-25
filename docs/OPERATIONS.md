# Operations guide

## 1. First deployment (one VPS, Docker)

> A step-by-step version with the web apps, the economy seed, Adsgram, BotFather and a launch checklist is in `docs/DEPLOY-VPS.md`. The economy values are explained in `docs/ECONOMY.md`.

1. Install Docker and the Compose plugin. Point the API domain (e.g. `brunoplay-api.duckdns.org`) at the server.
2. `cp .env.example .env` and fill it in: `APP_ENV=production`, two different random `JWT_SECRET` / `ADMIN_JWT_SECRET`
   (`openssl rand -hex 32`), `BOT_TOKEN`, `BOT_WEBHOOK_SECRET`, `ADSGRAM_WEBHOOK_SECRET`, `PUBLIC_API_URL`, `WEB_APP_URL`,
   `DATABASE_URL=postgres://brunoplay:<POSTGRES_PASSWORD>@postgres:5432/brunoplay?sslmode=disable`.
   **Production refuses to start** if `DEV_AUTH` is on or a required secret is missing.
3. `cp deploy/.env.deploy.example deploy/.env` and set `POSTGRES_PASSWORD`, `APP_DOMAIN`, `ADMIN_DOMAIN`, and the off-site backup repository.
4. `cd deploy && ./deploy.sh`. It builds, takes a quick backup, migrates, restarts, waits for `/readyz`, and rolls back if the API does not come up.
5. Create the first super admin: `docker compose -f deploy/docker-compose.yml run --rm -e ADMIN_PASSWORD=... api admin-create --email you@example.com`
   (use a strong password you then keep in a password manager; never put it in a file).
6. The bot's webhook and command menus are registered automatically when the API starts.
7. In the admin panel, fill the values listed in `docs/SETTINGS.md` marked **required** (the panel's "missing" view lists them).
   Until a value is set its feature answers `CONFIG_MISSING`: nothing is guessed.

### Adsgram
Set the reward URL of each ad block to
`https://<APP_DOMAIN>/api/webhooks/adsgram?userid=[userId]&blockid=<block id>&secret=<ADSGRAM_WEBHOOK_SECRET>`
and put the block ids in the setting `ads.block_ids` (`{"FISHING_START":"...","COLLECT":"...", ...}`).

### TON
* Top-ups: set `topup.ton.wallet_address` (the project wallet) and `topup.ton.stars_per_ton`. The worker reads incoming transfers
  with `TON_API_URL` (toncenter; put an API key in `TON_API_KEY` for a higher rate limit).
* Withdrawals are **manual by default**: the admin sends the TON and presses *Complete* with the transaction hash.
* Automatic payouts need all of: `AUTO_PAYOUT=true`, `PAYOUT_WALLET_SEED` (24 words, server environment only), and the settings
  `withdrawal.auto_payout_max_ton` and `withdrawal.auto_payout_daily_cap_ton`. **Test on testnet first** (`TON_TESTNET=true`)
  with small amounts; this code moves real money and could not be exercised against a live network in the build environment.

## 2. Daily life

| Task | How |
|---|---|
| Pay a withdrawal | Admin → Withdrawals → *Processing* → send TON → *Complete* with the hash. *Reject* returns the coins. |
| An automatic payout "needs review" | The bot alerts you. Look at the payout wallet's history for the comment `bpw<ID>`. If the transfer exists, *Complete* it with its hash; if you are sure nothing was sent, *Reject* with `force`. **Never resend before checking.** |
| Gift claim | Admin → Gift claims → send the gift in Telegram → *Deliver* (or *Reject* with a reason). |
| Tournament ended | Admin → Tournaments → review → disqualify cheaters → *Pay* (Finance or Super). |
| Suspicious player | Admin → Risk. Hold is automatic above `risk.hold_threshold`; *Clear* lifts it. |
| Change a price, rate, odds, text | Admin settings. Every change is stored with history and audited. |
| Maintenance | Setting `maintenance.enabled=true` (players see the "back soon" screen, admin keeps working). |
| Force an app reload | Setting `app.min_version`; apps sending an older `X-App-Version` get `426 UPGRADE_REQUIRED`. |
| Delete a player (privacy) | Super admin → `POST /api/admin/players/{id}/delete`. Personal data is erased, the money trail stays anonymised. |

## 3. Alerts (Telegram admin chat)

Set `ALERT_CHAT_ID`. You are told about: an overdue message queue, withdrawals waiting over 48 h, payouts stuck over 30 min,
payouts needing a human, unpaid TON orders, paid Telegram invoices without an order, refunds, automatic holds,
**ledger mismatches** (balances vs ledger, checked every 6 h; should never happen).

## 4. Backups and recovery

* PostgreSQL archives every WAL segment (`archive_mode=on`), so you can restore to any minute.
* `deploy/backup.sh` (cron nightly) takes a base backup and sends base + WAL to an **off-server, encrypted restic repository**.
* **Restore drill** (monthly): `deploy/restore-drill.sh ["2026-10-24 12:30:00+00"]` restores into a throwaway container and checks that the books balance.
  A backup you never restored is not a backup.
* Real disaster: new server → install Docker → `deploy.sh` with an empty database stopped → restore the base backup + WAL as the drill does → start.
* The backup scripts were written for this stack but **not executed in the build environment (no Docker there)**: run the drill once on the real server before relying on them.

## 5. Load test

```bash
# staging copy, DEV_AUTH=true, relaxed limits: IP_RATE=100000 IP_BURST=100000 PLAYER_RATE=100000 PLAYER_BURST=100000 LOGIN_RATE=100000 LOGIN_BURST=100000
go build -o loadtest ./cmd/loadtest
LOADTEST_ADMIN_PASSWORD=... ./loadtest -url http://localhost:8080 -players 500 -concurrency 64 -duration 60s -admin you@example.com
```

Reference run on a developer PC (Windows, local PostgreSQL, 64 parallel players, mixed reads and money actions):
**≈3,500 requests/s, p50 15 ms, p95 40 ms, p99 59 ms, zero 5xx**, and afterwards the ledger total equalled the sum of balances.
That is the machine the build ran on, not your VPS: agree the target numbers for the VPS and run the tool there (ToR 10.3).

The limits you see in production (`IP_RATE`, `PLAYER_RATE`, `LOGIN_RATE`, and 3 requests/s per player on money actions) are per client address,
so keep `TRUSTED_PROXY=true` only behind the bundled Caddy (it sets the real client address).

## 6. Where things are decided

`docs/SETTINGS.md` (every owner value) · `docs/API.md` (every route) · `docs/FRONTEND-INTEGRATION.md` (what the app must change) ·
`docs/SECURITY.md` (checklist and known limits) · `docs/TOR.md` (the specification).
