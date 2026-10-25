# Security checklist and known limits

## Done (and tested)
- [x] Telegram `initData`: HMAC signature, 24 h age, not-from-the-future; wrong bot token / tampering / expiry rejected.
- [x] Session tokens: HS256 JWT, 12 h; separate secrets and token kinds for players and admins (a player token never opens the admin API).
- [x] Production refuses to start with `DEV_AUTH`, a missing secret, or equal player/admin secrets. `/api/auth/dev` is not even routed outside dev.
- [x] Admin: argon2id passwords, strong-password rules, lockout (5 failures → 15 min, doubling, max 12 h), same answer for unknown e-mail,
      15-min access tokens + rotating refresh tokens (reuse refused, deactivation signs out), roles/permissions on every route, full audit log,
      the last super admin cannot be removed. **Login is password-only (owner's decision)**; TOTP can be added without redesign.
- [x] Money: balances change only in `internal/ledger` (atomic update + append-only row; a database trigger forbids edits and deletes of ledger rows);
      overdraft impossible (`WHERE balance + amount >= 0`); idempotency keys; row locks; tested with parallel requests for upgrades, collects,
      claims, sells, withdrawals, payments, picks, payouts. A 6-hourly audit compares balances with the ledger.
- [x] Ads: proof comes only from Adsgram's server callback (shared secret, constant-time compare); a proof is bound to player and placement,
      expires, and is single-use; a refused action does not burn it. The no-fill fallback exists only for fishing start/collect and is logged.
- [x] Payments: Telegram pre-checkout validates amount/player/state; one charge id credits one order once; refunds reverse and hold the account;
      TON transfers are matched by unique comment + amount + time window and a transaction hash can credit only once; underpaid orders alert a human.
- [x] Withdrawals: coins leave in the same transaction as the request; rate/fee stored per request; address validated incl. CRC; one open request
      (database-enforced); automatic payout claims the request **before** sending, never resends an unclear result, hands a held or blocked
      player to a human; the payout key lives only in the environment.
- [x] Games: all draws use the system CSPRNG; chest prizes are drawn at payment (taps cannot steer them); Ride results are checked against
      server-measured time and rejected runs flag the account; the answer of the daily combo is never in any response.
- [x] Input: strict JSON (unknown fields refused, 1 MB limit), validation on every body, parameterised SQL everywhere, HTML-escaped bot texts.
- [x] Rate limits per address, per player, per login address, and per player on money routes; Telegram's own send limits respected.
- [x] No secrets in the repository: `.env` is ignored; the bot token, wallet seed and passwords are environment-only.
- [x] `govulncheck` is clean for the code paths used (one advisory in `golang.org/x/crypto` has no fix yet and is not reachable from this code; re-check on every dependency update).

## Before going live (owner's checklist)
- [ ] Rotate the bot token and the admin password that were pasted in chat during development.
- [ ] Set `BOT_WEBHOOK_SECRET`, `ADSGRAM_WEBHOOK_SECRET`, both JWT secrets, `POSTGRES_PASSWORD` to long random values.
- [ ] Keep the database port closed to the internet (the compose file publishes only Caddy's 80/443).
- [ ] Run the restore drill once; set up the off-site backup repository.
- [ ] Test automatic payouts on **testnet** before enabling `AUTO_PAYOUT` (or keep payouts manual).
- [ ] Keep the Finance role to as few people as possible: the admin login has no second factor.
- [ ] Legal: the app turns game coins into real money and sells paid game actions. That is regulated in many countries and the owner decided to
      include no terms/age/country controls in v4 (the data model has `terms_accepted_at` and `country` ready). Revisit before launch at scale.

## Known limits
- The payout guard re-rolls prizes invisibly; every intervention is logged (`guard_events`) but players are not told. The ToR recorded this risk.
- Rate limiting is in-process (one server). Behind several API servers the limits are per server; add a shared limiter then.
- Per-address limits assume the proxy sets the real client address (`TRUSTED_PROXY`); players behind one carrier address share a limit.
- Two TON top-ups with the same comment are impossible, but the project wallet must not receive other traffic with comments starting `bp`.
