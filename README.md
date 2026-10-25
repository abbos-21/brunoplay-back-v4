# BrunoPlay backend v4

The backend of the BrunoPlay Telegram Mini App, built from `docs/TOR.md`. One Go binary, PostgreSQL, no other moving parts.

```
brunoplay api            the HTTP API (player + admin)
brunoplay worker         background jobs and the bot's message sender
brunoplay all            both in one process (development)
brunoplay migrate        apply database migrations
brunoplay admin-create   create an admin account (--email, --name, --role; password from ADMIN_PASSWORD or a prompt)
brunoplay docs           regenerate docs/API.md and docs/SETTINGS.md from the code
```

## Quick start (development)

```bash
# PostgreSQL 15+ running somewhere, then:
export APP_ENV=dev DEV_AUTH=true
export DATABASE_URL='postgres://user:pass@localhost:5432/brunoplay?sslmode=disable'
export JWT_SECRET=$(openssl rand -hex 32) ADMIN_JWT_SECRET=$(openssl rand -hex 32)
go run ./cmd/brunoplay migrate
ADMIN_PASSWORD='a-strong-Passw0rd!' go run ./cmd/brunoplay admin-create --email you@example.com
go run ./cmd/brunoplay all          # http://localhost:8080
```

`DEV_AUTH=true` adds `POST /api/auth/dev` (sign in without Telegram). The process **refuses to start** in production with it on.

## Tests

```bash
go test ./...                     # needs PostgreSQL; each package gets its own database
# TEST_DATABASE_URL=postgres://postgres@localhost:5544/postgres?sslmode=disable   (the server; databases are created as needed)
go test -race ./internal/ledger ./internal/api
```

There are about 150 tests, and they run against a real PostgreSQL: money paths under concurrency (double taps, parallel workers), every rule of the ToR, both languages.

## Layout

| Path | What |
|---|---|
| `cmd/brunoplay` | the program; `services.go` lists every background job |
| `cmd/loadtest` | load-test tool (see `docs/OPERATIONS.md`) |
| `internal/ledger` | **the only place balances change**; append-only, atomic, idempotent |
| `internal/fishing` | the mining engine (exact integer maths) and the sessions |
| `internal/ads` `shop` `daily` `tasks` `referrals` `games` `tournaments` | the game modules |
| `internal/topup` `withdrawals` `ton` | Telegram Stars and TON in, TON out |
| `internal/notify` `bot` `telegram` | the bot: queue, templates (RU/EN), commands, broadcasts |
| `internal/admin` `settings` | admin accounts/roles/audit and every `{CONFIG}` value |
| `internal/stats` `risk` `ops` | dashboard numbers, anti-fraud holds, alerts, privacy deletion |
| `internal/api` | the routes and handlers; `*_test.go` are the integration tests |
| `migrations` | SQL migrations (embedded in the binary) |
| `deploy` | Docker Compose, Caddy, deploy, backup and restore-drill scripts |
| `docs` | ToR, generated API and settings references, operations, security, front-end guide |

## Rules the code keeps everywhere

1. **The server decides.** Mining, ads, prices, prizes, time: the client only displays.
2. **Every balance change is a ledger row** in the same transaction; a daily audit proves balances equal the ledger.
3. **A request that is repeated never pays twice** (unique keys, row locks, `Idempotency-Key`).
4. **A missing owner value is an error, never a guess** (`CONFIG_MISSING`); see `docs/SETTINGS.md`.
5. **Texts come in both languages**: errors, admin content and every bot message (RU/EN).
