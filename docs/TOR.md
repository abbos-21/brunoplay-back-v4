# BrunoPlay Backend v4 — Terms of Reference (ToR)

A brand-new backend, built from scratch from this document. The old `brunoplay-back-v3` is **not** a base and is not
reused (its code, database design and mechanics are ignored).

Status: **COMPLETE (v2.0) — the owner declared the explanation finished.** The backend is built from this document.
Reading order: sections 0–9 are the base specification; **Appendix A (screen-by-screen decisions) is the latest word and wins
where it differs** (see A.16); section 10 is the build plan and acceptance; section 11 lists every value the owner still has to supply.

Conventions
- **[CONFIRMED]** — decided by the owner.
- **[PROPOSAL]** — my suggestion, needs the owner's OK.
- **{CONFIG: name}** — a value (price, rate, limit, level table…) the owner will supply **after the backend is built**.
  The backend must never hard-code these: every one is a setting, editable without a redeploy, and the code ships with
  empty/placeholder defaults that fail safe (see 9.2).

Version history
- v2.0 — complete: Appendix A (14 more areas decided screen by screen), consolidated API/data model/admin, build plan, values checklist
- v1.2 — section 7: daily streak and daily combo
- v1.1 — stack chosen for performance (Go + PostgreSQL), no ad/session limits, Telegram bot with notifications (RU/EN)
- v1.0 — fresh ToR. Section 1: core game (fishing/mining, Adsgram ads, shop cards, boosters, coins → TON).

---

## 0. Product

BrunoPlay is a Telegram Mini App idle game. The player "fishes" (mines) coins into a tank over time, collects them, and can
convert coins into real **TON**. **Adsgram** ads gate starting fishing and collecting. The player improves earning with
shop **cards** (upgrades) and **boosters**.

Principle: **the backend is the single source of truth.** The client never decides how many coins were mined, whether an ad
was watched, what anything costs, or what the time is.

### 0.1 Technology stack — chosen for performance [CONFIRMED: owner asked me to choose the fastest]
**Go + PostgreSQL**, one codebase that builds one small binary run in two modes: `api` (HTTP) and `worker` (jobs, bot sender).

Analysis behind the choice:
- Mining is computed from timestamps, so there is no per-second server work. The load is many short, safe database writes
  (start, collect, upgrade, buy), bursts of ad webhooks, and exactly-timed notifications for many players. The real limits are
  database transactions and the Telegram send limit (about 30 messages/second overall, 1/second per chat), not raw CPU.
- Go gives very low latency and memory, handles tens of thousands of simultaneous connections without tuning, and its
  goroutines suit the many parallel, rate-limited workers (notification sender, autocollect, webhooks). It deploys as a single
  static binary: easy and cheap on the owner's VPS, fast start, no runtime to maintain.
- Compared: Node/TypeScript (same language as the front end, but slower under load and heavier on memory); Rust (fastest, but
  much slower to build and change, for no visible gain here because the database is the bottleneck); Java/Kotlin and .NET (fast,
  but heavier memory and deployment). Go is the best balance of speed, safety and simplicity for this workload.
- Components: Go (current stable) · HTTP router `chi` (or the standard library) · `pgx` + `sqlc` (hand-written, type-checked
  SQL, no ORM overhead) · PostgreSQL (primary store, ledger, job queue via `FOR UPDATE SKIP LOCKED` or River) · `goose` for
  migrations · structured logging (`slog`) · metrics ready for OpenTelemetry/Prometheus · Telegram bot via a maintained Go
  library in **webhook** mode · automated tests with the standard `testing` package against a real PostgreSQL (containerized).
- **No Redis at the start** (fewer moving parts, no extra server). Rate limiting is in-process, per IP and per player, and the
  code keeps a cache/limiter interface so Redis can be added later if one server is no longer enough.
- The front end stays Vue/TypeScript; it only talks to the new API.

### 0.2 Performance design rules
- Reads such as `GET /fishing` are computed in memory from a few columns (no heavy joins, no writes).
- Hot state sits in one row per player; writes use short transactions that lock only that player's rows.
- Indexes are designed for every query path (player id, session status, `notify_at`, ad view id); no unbounded table scans.
- Background work is batched (autocollect and notifications pick due rows in batches with `SKIP LOCKED`), so several worker
  processes can run in parallel without double processing.
- Tables that grow forever (ledger, ad views, sent notifications) are designed for partitioning/archiving.
- A load test (target numbers agreed with the owner) is part of the acceptance of the build.

---

## 1. Core game: fishing (mining)

### 1.1 Terms
| Term | Meaning |
|---|---|
| Fishing / mining | Coins accumulate in the tank over time at the player's mining speed. |
| Tank | Holds the mined, not-yet-collected coins. Its size is the **coin capacity**. |
| Mining speed | Coins per second. |
| Collect | Moves the tank's coins to the player's coin balance. |
| Session | One fishing run: from start until collect. |

### 1.2 Player flow [CONFIRMED]
1. The player enters the game and taps **Fishing**.
2. An **Adsgram ad** is shown first. **If the ad is not watched, fishing does not start.**
3. Fishing runs. Coins fill the tank by time. When the tank is full, mining stops (nothing beyond capacity is earned).
4. The player taps **Collect**:
   - only possible when the tank holds **at least 10 % of its capacity** (below that: refused);
   - an **Adsgram ad** must be watched first. **If the ad is not watched, the coins cannot be collected.**
5. The tank's coins are added to the coin balance and the tank is emptied. **The session ends**; to fish again the player
   taps Fishing and watches a new start ad. One full cycle = 2 ads (start + collect).
6. Coins can be converted into real TON (section 1.8).

Mining is a **simulation**: the amount in the tank is calculated from time, not from the player doing anything.

### 1.3 Server rules for mining
- Amount in tank at any moment = `min(capacity, Σ over time segments of (seconds × mining speed in that segment))`.
  Segments exist because mining speed or capacity can change during a session (card upgrade, booster start/end, see 1.5–1.6).
- Server time is the only clock. Client time is ignored.
- Mining continues while the player is away from the app (the tank keeps filling until full).
- The 10 % threshold is evaluated by the server at the moment of the collect request, against the capacity at that moment.
- Only **one running session per player**. Starting while one is running is rejected.
- Start and collect are **idempotent and atomic**: a retried or double-tapped request must never credit coins twice or
  consume two ads for one action (DB transaction + unique constraints).
- Every change of the coin balance writes an append-only **ledger** row (who, amount, reason, related session/order,
  balance after, time). The balance is never changed without a ledger row.
- Money-like values are stored as exact integers / fixed-point (no floating point). The precision (decimals of a coin) is
  decided with the owner's values; the code uses a single money module.

### 1.4 Ads — Adsgram, verified on the server [CONFIRMED]
- Core placements: **fishing start** and **collect** (more placements for bonus, ×2, extra attempts: see A.13).
- The client is never trusted for "ad watched". Proof comes from **Adsgram's server-to-server reward callback**.
- Flow:
  1. Client asks the backend for an ad (`placement`). The backend creates an **ad view** record (status `PENDING`, expiry)
     and returns what the Adsgram SDK needs.
  2. Client shows the ad with the Adsgram SDK.
  3. Adsgram calls the backend's reward webhook (authenticated); the backend marks that ad view `COMPLETED`.
  4. Client sends start/collect together with the ad view id. The backend accepts it only if the ad view: belongs to this
     player, matches the placement, is `COMPLETED`, is not expired, and has not been used. It is then marked `USED` in the
     **same transaction** as the action.
- One ad view pays for exactly one action.
- Abuse protection: rate limits on ad requests; expiry of unused ad views; full history kept for audit.
- Settings: `{CONFIG: ads.blockId.start}`, `{CONFIG: ads.blockId.collect}`, `{CONFIG: ads.webhookSecret}`,
  `{CONFIG: ads.viewExpirySeconds}`, and an `ADS_REQUIRED` switch that **must be on in production** (off only for local
  development).

### 1.5 Shop — cards (upgrades) [CONFIRMED]
Two cards, **maximum level 10 each**:
| Card | Effect |
|---|---|
| **Mining rate** | Raises the mining speed (coins per second). |
| **Coin capacity** | Raises the tank size. |

- Both start at level 1.
- The level tables (value per level, upgrade cost per level, and the currency of the cost) are `{CONFIG: cards.miningRate.levels}`
  and `{CONFIG: cards.capacity.levels}` — **supplied by the owner later**.
- Upgrading is atomic: check balance → charge (ledger row) → level +1, in one transaction; never above level 10; safe against
  double requests.
- If a session is running when a card is upgraded, the mined amount up to that moment is **settled first** (kept as a
  segment), then the new value applies from that moment on.

### 1.6 Shop — boosters [CONFIRMED]
Two boosters, **each lasts 30 days** (`{CONFIG: boosters.durationDays}` = 30):
| Booster | Effect |
|---|---|
| **Autocollect** | When the tank is full, the server collects the coins automatically: no need to open the game, tap Collect or watch an ad. |
| **1.5× mining speed** | Multiplies the current mining speed by 1.5 while active. |

Rules:
- A player with an **active** booster of a kind **cannot buy the same kind again** until it expires. The two kinds are
  independent (both can be active together).
- Price and its currency: `{CONFIG: boosters.<type>.price}` — **supplied later**.
- Purchase is atomic: check balance → charge (ledger row) → create the player's booster with start and expiry.
- Expiry is computed from server time on every read/settle (correctness does not depend on a cron job).
- **1.5×:** mining is calculated in segments, so a booster starting or ending mid-session only affects the seconds it was
  active.
- **Autocollect:** a background job finds players whose Autocollect is active and whose tank is full, credits the coins
  (ledger reason "autocollect"), and **immediately starts a new session** with no ad, so mining continues hands-free while
  the booster lasts. The player sees the result on next opening the app. If the booster expires, the normal ad flow applies
  again from the next session. Edge: if Autocollect expires while the tank is full, the coins stay in the tank and are
  collected the normal way (with ad).

### 1.7 Players
- Players are Telegram users, authenticated by validating Telegram Mini App `initData` on the server (signature and age).
- A player record holds: Telegram id, name, username, language, coin balance, created/last seen times.
- Balance fields are changed only by backend logic that writes the ledger.

### 1.8 Coins → TON (real money) [CONFIRMED in principle, values later]
Mined coins are converted into real TON. **The rules are fully defined in A.10** (fixed admin rate and fee, one open request,
cooldown, minimum level/age, limits, automatic payout below a limit and manual above, refunds). The numbers are `{CONFIG: withdrawal.*}`
values supplied later. In short the backend provides:
- a withdrawal request (player, coin amount, TON amount, TON address, status, timestamps),
- coins deducted with a ledger row when the request is created and returned if it is failed/cancelled,
- all numbers and rules read from settings, nothing hard-coded.

---

## 2. API surface (JSON over HTTPS). 2.1 Section-1 endpoints; the complete index is 2.2
| Endpoint | Purpose |
|---|---|
| `POST /auth/telegram` | Validate `initData`, create/find the player, return a session token. |
| `GET /me` | Player profile and balances. |
| `GET /fishing` | State: running or not, tank amount now, capacity, speed (boosters included), can-collect, time until full. |
| `POST /ads` `{placement}` | Create an ad view; returns Adsgram parameters. |
| `POST /webhooks/adsgram` | Adsgram server callback (secret-authenticated). |
| `POST /fishing/start` `{adViewId}` | Start a session (ad required). |
| `POST /fishing/collect` `{adViewId}` | Collect (ad required, tank ≥ 10 %). |
| `GET /shop/cards` | Both cards: level, current → next value, cost, can-upgrade. |
| `POST /shop/cards/{card}/upgrade` | Upgrade one level. |
| `GET /shop/boosters` | Boosters, price, active one with expiry, can-buy. |
| `POST /shop/boosters/{type}/buy` | Buy a booster. |
| `POST /withdrawals`, `GET /withdrawals` | Create / list withdrawal requests. |

Errors carry a stable machine code plus a message **already in the player's language (RU/EN)** (A.12), e.g. `AD_NOT_COMPLETED`,
`TANK_BELOW_MINIMUM`, `SESSION_ALREADY_RUNNING`, `SESSION_NOT_RUNNING`, `CARD_MAX_LEVEL`, `INSUFFICIENT_BALANCE`,
`BOOSTER_ALREADY_ACTIVE`.

### 2.2 Complete player API index (grouped; every endpoint requires the player token except auth and webhooks)
| Area | Endpoints (summary) |
|---|---|
| Auth / profile | `POST /auth/telegram`, `GET /me`, `PATCH /me/language`, `GET /app-info` (links, version, maintenance, top-up bonus %) |
| Fishing | `GET /fishing`, `POST /fishing/start`, `POST /fishing/collect` |
| Ads | `POST /ads`, `POST /ads/failed`, `POST /webhooks/adsgram`, `POST /ads/bonus/claim` |
| Shop | `GET/POST /shop/cards…`, `GET/POST /shop/boosters…` |
| Stars top-up | `GET /topup/packs`, `POST /topup/telegram-invoice`, `POST /topup/ton-order`, `GET /topup/orders`, payment webhooks (Telegram, TON watcher) |
| Daily | `GET /daily/streak`, `POST /daily/streak/claim`, `GET /daily/combo`, `POST /daily/combo/submit`, `POST /daily/combo/claim` |
| Games | Spin: `GET /spin/tiers`, `POST /spin`, `GET /spin/winners`; Chest: `GET /chest`, `POST /chest/round`, `POST /chest/pick`; Ride: `POST /ride/start`, `POST /ride/finish`, `GET /ride/me` |
| Gifts | `GET /gifts`, `POST /gifts/{id}/sell`, `POST /gifts/{id}/claim` |
| Tournaments | `GET /tournaments`, `POST /tournaments/{id}/join`, `GET /tournaments/{id}/leaderboard` |
| Tasks | `GET /tasks`, `POST /tasks/{id}/check`, `POST /tasks/{id}/ad-progress` |
| Friends | `GET /friends`, `GET /friends/rewards`, `GET /friends/link` |
| Levels | `GET /levels` (names, rewards, player progress) |
| Withdrawals | `GET /withdrawals/info`, `POST /withdrawals`, `GET /withdrawals`, `POST /withdrawals/{id}/cancel` |
| Music | `GET /music` |
| Bot | `POST /webhooks/telegram` (updates), notification preferences are changed through the bot and `PATCH /me/notifications` |
Rate limits, idempotency keys on every money-moving POST, and the version header (A.18) apply to all.

### 2.3 Admin API (separate, own auth, RBAC — A.11)
Modules: dashboard · players (view, ledger, sessions, flags, block/unblock, hold, notes, delete-for-privacy) · settings/`{CONFIG}`
editor with history · cards and level tables · boosters · shop/top-up packs and payments · ads (placements, views, failed loads) ·
combo (images, patterns by date, rewards) · streak rewards · tasks · spin/chest/ride settings, odds, guard, gifts · gift claims ·
tournaments (create, leaderboard review, pay) · referrals (settings, per-inviter bonus, fraud view) · withdrawals (queue, send,
reject, payout limits, auto-payout switch) · notifications (templates, categories, broadcasts, send logs) · content (links, music
playlist, FAQ/notice texts) · admins and roles · audit log · maintenance and minimum version.

## 3. Data model (outline; the build produces the full schema)
Core: `players` (+ risk flags, hold, blocked, referral fields) · `ledger_entries` (append-only, per currency) · `settings` + history ·
`admins`, `admin_roles`, `audit_log`.
Fishing: `fishing_sessions` (+ segments) · `player_cards` · `card_levels` · `card_requirements` · `player_boosters` · `boosters`.
Ads: `ad_views` · `ad_failures`.
Money: `topup_packs` · `topup_orders` (Telegram Stars / TON, status) · `withdrawals` (+ stored rate/fee, tx hash, payout id).
Daily: `daily_streaks` · `streak_reward_levels` · `combo_images` · `combo_patterns` · `combo_attempts` · `combo_claims`.
Games: `spin_tiers` · `spin_odds` · `gifts` · `player_gifts` (status OWNED/SOLD/CLAIM_REQUESTED/DELIVERED) · `spin_draws` · `chest_rounds`
(+ stored prizes) · `chest_pool` · `ride_runs` · `guard_events`.
Social: `referrals` (+ snapshot join bonus) · `referral_payments` · `tasks` · `task_progress` · `tournaments` · `tournament_entries` ·
`tournament_prizes`.
Bot: `notification_types`, `notification_templates` (RU/EN) · `notifications_scheduled` · `notification_log` · `broadcasts` ·
`player_notification_prefs`.
Content: `music_tracks` · `levels` (names, rewards).

## 4. Admin needs
Defined in A.11 (roles, login, audit) and the modules listed in 2.3. The admin **UI** is built separately later; this project
delivers the complete admin API with documentation (OpenAPI).

## 5. Quality requirements
- All balance-changing operations in DB transactions; ledger append-only; unique constraints against double processing.
- Strict input validation on every endpoint (typed request structs + a validator library); rate limiting on ad, start, collect, upgrade, buy, withdraw and every money-moving POST; idempotency keys.
- Automated tests at least for notifications (due-time scheduling, cancel/recalculate on change, cooldown, daily cap, quiet hours, blocked bot, RU/EN templates) and for: tank maths with segments and capacity cap, 10 % rule, single-use ad proof, one session per
  player, upgrade cap at level 10, booster same-kind lock and expiry, autocollect job incl. restart and expiry edge,
  double-request idempotency.
- Further tests: top-up (Telegram + TON) idempotency and refunds, withdrawal rules and refund of failed requests, payout job
  never sending twice, spin/chest draws (odds, guard, pre-drawn chest prizes, resume), gift sell/claim races, referral rules (level 2,
  caps, per-inviter snapshot), tournament entry fee / freeze / payout, task repeat periods, Ride plausibility and daily cap,
  ad-view single use and the no-fill fallback, admin RBAC, migrations, and a load test (10.3).
- Production safety: ads required, no dev auth bypass, no mock payments, no auto payout until the owner enables it.

## 6. Telegram bot and notifications [CONFIRMED: owner wants a proper bilingual bot]
The bot is **@ImTomsBot** (the token is a secret kept in the server environment, never in the repository). It works in
**Russian and English**, runs in **webhook** mode, and is part of the same backend.

### 6.1 Bot features
- `/start` (with an optional referral parameter `ref_<id>`, see A.9): welcome message in the player's language and a
  button that opens the Mini App.
- `/language` (switch RU/EN, in sync with the language chosen inside the app), `/notifications` (turn categories on or off),
  `/help`, `/stop` and `/resume`. Inline buttons open the Mini App on the right page (e.g. **Collect**, **Fishing**).
- Language: the player's saved language; if none, Telegram's `language_code` (Russian → RU, otherwise EN). The in-app language
  switch and the bot language are one setting.

### 6.2 Notification principles
- **Event-driven and automatic.** Every game event that matters produces a notification through one central notification
  service. When the owner explains a new game section, its catalog entries are added to this ToR and implemented the same way;
  nothing is hand-wired per feature.
- Each notification has a **type**, a **category** (so players can switch categories off), an RU and an EN **template** (text
  with placeholders and an optional button), a **trigger rule**, a **cooldown** and a **priority**. Templates and timings are
  editable in the admin panel (`{CONFIG: notifications.*}`), no redeploy needed.
- **Exact timing** for time-based events: when something will happen at a known time (the tank becomes full, a booster
  expires), the backend stores a due time (`notify_at`) and a worker sends it when due. If the situation changes before that
  (the player collects, a card upgrade changes the time to full), the pending notification is **recalculated or cancelled**,
  so nobody gets a wrong "your tank is full".
- **No spam:** per-type cooldowns, a daily cap `{CONFIG: notifications.dailyCap}`, quiet hours `{CONFIG: notifications.quietHours}`
  (UTC, learned best hours per A.17), and merging of duplicates. A notification is never sent for something the player
  has already done.
- **Delivery:** a queued sender honours Telegram's limits (global and per chat), retries temporary errors with back-off, and
  when Telegram says the player blocked the bot, marks the player unreachable and stops sending (resumes when the player
  starts the bot again). Every send is logged (sent, failed, skipped and why).
- Players can switch categories off; mandatory ones (e.g. money/withdrawal status) cannot be switched off.
- Messages are never the source of truth: they only point the player to the app.

### 6.3 Notification catalog for section 1 (starting list)
| Type | Trigger | Message idea (EN) | Button |
|---|---|---|---|
| `tank_full` | The tank has just become full. | "Your tank is full! Collect your coins." | Collect |
| `collect_reminder` | Tank still uncollected `{CONFIG: N hours}` after being full. | "Your coins are waiting in the tank." | Collect |
| `fishing_idle` | No session running `{CONFIG: N hours}` after the last collect. | "Time to start fishing and mine more coins!" | Fishing |
| `autocollect_done` | Autocollect credited coins (with the amount). | "Autocollect gathered X coins for you and started a new run." | Open game |
| `booster_expiring` | A booster ends in `{CONFIG: 3 days}` and `{CONFIG: 1 day}`. | "Your Autocollect / 1.5x booster ends soon." | Shop |
| `booster_expired` | A booster has just expired. | "Your booster has expired. Renew it in the shop." | Shop |
| `card_upgrade_ready` | Optional (`{CONFIG: on/off}`): the player can afford the next card level. | "You can upgrade Mining rate!" | Shop |
| `withdrawal_status` | A withdrawal changes status (created, processing, paid, failed, cancelled). | "Your withdrawal of X TON was sent." | History |
| `welcome` | First `/start`. | Welcome and how to play. | Play |

Further entries for later sections are listed in **6.4**.

### 6.4 Notification catalog for the other sections (all RU/EN, categories per A.19)
| Type | Category | Trigger | Button |
|---|---|---|---|
| `topup_status` | Payments | Stars/TON payment pending / credited / failed / refunded | Open game |
| `friend_joined` · `friend_level_reward` | Friends | A friend became counted (level 2) · reward paid for a friend's level | Friends |
| `level_up` | Fishing | The player reached a new level (with reward) | Levels |
| `task_new` | Daily | A new task is available | Tasks |
| `tournament_start` · `tournament_ending` · `tournament_result` | Games & tournaments | Start, last hours, final place/prize | Tournament |
| `gift_claim_update` | Games & tournaments | Claim delivered or rejected | Collection |
| `chest_round_open` | Games & tournaments | A paid chest round was left unfinished (once, after N hours) | Chest |
| `account_blocked` · `security_alert` | Payments | Account blocked/unblocked, admin-set address anomalies | Support |
| `broadcast` | News | Admin announcement (A.17) | Button from admin |

## 7. Daily activities: streak and combo [CONFIRMED by the owner, values later]
Two daily tasks. Both use the same **game day**.

### 7.1 Game day [CONFIRMED]
- A day is **00:00–23:59 UTC for every player**. The server computes it; the client time is ignored.
- The app shows a countdown to the next reset (`resetsAt`).

### 7.2 Daily streak (daily login reward) [CONFIRMED]
- The player **taps Claim** once per game day to get that day's coins. The streak counts a day only when it is claimed.
- The streak is a ladder of **10 days**; each day has its own reward `{CONFIG: streak.rewards[1..10]}` (edited in admin).
- **Missed day → the streak resets to day 1.** (Claiming on consecutive game days keeps it; skipping a whole game day breaks it.)
- **After day 10 the ladder loops:** day 11 pays the day 1 reward, day 12 the day 2 reward, and so on; the streak counter keeps
  growing (the player sees e.g. "streak 14, today's reward = day 4 reward").
- Rules: one claim per game day (second tap rejected, never pays twice); atomic with a ledger row (reason "daily streak"); the
  claim response returns the reward, new streak and new balance.
- Status for the app: current streak, `canClaim`, today's reward, the 10 reward cells with `claimed` / `isToday`, `resetsAt`.

### 7.3 Daily combo [CONFIRMED]
A memory/ordering puzzle. The player arranges pictures into slots; each slot is either right or wrong.
- **Picture pool: 8 images in total**, managed in the admin panel (image file + name). `{CONFIG: combo.images}`
- **The daily pattern** is an ordered list of pictures (the correct picture for slot 1, slot 2, …). The slot count is shown by
  the app (currently 4). It is the **same pattern for all players** that day.
- **Attempts (final rules in A.13 and A.17):** one free attempt per player per game day, plus ad-unlocked extra attempts up to an
  admin daily limit; only the **best attempt** counts; the reward is **claimed** (Claim or Claim ×2) before the reset or is lost.
- **Result:** the server compares position by position and returns, **for every slot, correct or wrong**. The app shows a
  **tick (✔) badge** on correct pictures and a **cross (✘) badge** on wrong ones, at the top-right of each placed picture.
  Example: pattern *apple, banana, pineapple, orange*, the player submits *pineapple, banana, apple, orange* → slots 2 and 4
  are correct, slots 1 and 3 are wrong → 2 correct.
- **Reward = number of correct slots × coins per correct slot** `{CONFIG: combo.coinsPerCorrect}`. (All the same amount;
  no extra bonus.) 0 correct = 0 coins. It is credited when the player **claims** (ledger reason "daily combo"; doubled with an ad).
- **The correct pattern is never revealed.** The API returns only the per-slot correct/wrong result and the coins won, never
  the answer pattern — so it cannot be read from the network or shared as a cheat. Today's pattern is not sent to the client
  before the attempt either (the client receives only the 8 pictures to choose from).
- After the free attempt the status shows `alreadyAttempted = true`, the best result so far, attempts left (ad-unlocked) and the
  countdown; further attempts without an ad view are refused with `COMBO_ALREADY_ATTEMPTED`; after claiming, the combo is locked
  until the next game day.
- Validation of the submission: exactly 4 slots; every picture belongs to the pool; **no picture twice** (A.17).

### 7.4 Admin: preparing the combo [CONFIRMED]
- The admin **schedules a pattern for specific dates** in advance (calendar/list view; edit until the day starts; read-only once
  the day has started).
- **Automatic fallback:** if a date has no pattern, the server generates a random one at the start of that game day (from the
  8 images, valid under the pattern rules) and stores it, so the game never breaks. The admin sees which days were auto-generated.
- The admin also edits the 8 images and the combo / streak reward values. All changes are audited (who, what, when).
- The admin can view how many players attempted each day and the average result.

### 7.5 API for section 7 (draft)
| Endpoint | Purpose |
|---|---|
| `GET /daily/streak` | Streak status (see 7.2). |
| `POST /daily/streak/claim` | Claim today's reward. |
| `GET /daily/combo` | The 8 pictures, `alreadyAttempted`, last result, `resetsAt`, number of slots. |
| `POST /daily/combo/submit` `{items:[pictureId…]}` | One attempt; returns per-slot result (`correct: bool[]`), `correctCount`, `reward`. |
Admin: CRUD for pictures, pattern by date, and the reward settings.
Error codes: `STREAK_ALREADY_CLAIMED`, `COMBO_ALREADY_ATTEMPTED`, `COMBO_INVALID_ITEMS`.

### 7.6 Data model additions
`daily_streaks` (player, current streak, last claimed game day, best streak) · `streak_reward_levels` (config, 10 rows) ·
`combo_images` (8 rows) · `combo_patterns` (game day unique, ordered picture ids, source: admin/auto) · `combo_attempts`
(player, game day unique together, submitted order, per-slot result, coins won).
Unique constraints guarantee one claim and one attempt per player per game day, even with simultaneous requests.

### 7.7 Notifications for section 7 (bot, RU/EN)
| Type | Trigger | Message idea (EN) | Button |
|---|---|---|---|
| `streak_ready` | New game day started and the player has not claimed (sent at `{CONFIG: hour}` after reset). | "Your daily reward is ready!" | Claim |
| `streak_at_risk` | `{CONFIG: N hours}` before the reset, player has an active streak (≥ 2) and has not claimed. | "Don't lose your streak of N days — claim today's reward!" | Claim |
| `streak_lost` | The streak was reset because a day was missed (once). | "Your streak was reset. Start a new one today!" | Claim |
| `combo_ready` | New game day, the player has not tried the combo. | "Today's Daily Combo is ready — find the right order!" | Play combo |
| `combo_reminder` | `{CONFIG: N hours}` before reset, not attempted. | "Last chance to play today's Daily Combo." | Play combo |
Same rules as in 6.2 (categories, cooldown, daily cap, quiet hours, cancel when the player has already done it).

### 7.8 Tests for section 7
Game-day boundaries at 23:59:59 / 00:00:00 UTC; streak continue / reset / loop after day 10; double-claim race; combo
position-by-position scoring (including the apple/banana example); one attempt per day under concurrent requests; combo pattern
never present in any API response; auto-generated pattern when none is scheduled; notification scheduling and cancellation.

## 8. Scope
**Everything the owner described is in this document** (sections 0–7 and Appendix A). Anything not mentioned is out of scope for
v4 (for example: wallet binding, currency exchange, Premium perks, legal screens, global Ride leaderboard, data migration from v3).
New ideas later are added as new numbered sections and built as separate, versioned changes.

## 9. Open points
### 9.1 Questions for the owner
| # | Question |
|---|---|
| Q1 ✅ | Stack: owner delegated the choice; Go + PostgreSQL chosen (0.1). |
| Q2 ✅ | **No limits** on fishing sessions or ads per player. (Technical rate limits against bots still apply; they never stop normal play.) |
| Q3 ✅ | Yes: a full Telegram bot with notifications in Russian and English (section 6). |
| Q4 ✅ | Game day resets at 00:00 UTC for everyone. |
| Q5 ✅ | Missed day → streak resets to day 1. |
| Q6 ✅ | Combo: same pattern for all; one free attempt per day (+ ad-unlocked extras, A.13). |
| Q7 ✅ | After day 10 the reward ladder loops back to the day 1 reward; the streak counter keeps counting. |
| Q8 ✅ | Combo coins = same amount for each correct picture. |
| Q9 ✅ | Admin schedules patterns per date; server auto-generates one if missing. |
| Q10 ✅ | The API returns only tick/cross per slot, never the correct pattern. |
| Q11 ✅ | The streak reward is claimed by the player tapping Claim. |
| D1 ✅ | Pattern is always 4 slots (A.17). |
| D2 ✅ | No repeats anywhere (A.17). |
| D3 ✅ | Daily messages near the player's own best hour; warnings 6 h and 1 h before reset (A.17). Hours are settings. |
| D4 ✅ | Streak and combo are free for everyone, no requirements (A.17). |
| D5 ✅ | An unclaimed combo reward is lost at the reset (A.17). |

### 9.2 Behaviour while `{CONFIG}` values are missing
Until the owner supplies values, the backend starts with a clearly marked **"unconfigured"** state: endpoints that need a
missing value return a configuration error instead of guessing, and the admin panel highlights what is still empty.

---

## Appendix A — Decisions from the frontend review (screen by screen)
Collected by walking through every screen of the Mini App and asking the owner. Each entry is **[CONFIRMED]** unless noted.
Order: Home → menu → Shop → Games (Chest, Spin, Ride) → Tasks → Friends → Levels → Withdraw / history → Stars top-up → Admin.

### A.1 Home
- **Live tank:** the server returns a snapshot (start time, effective speed, capacity, tank amount at that moment); the app
  animates the number locally and only re-syncs on app open, after actions and occasionally. **No 2.5-second polling.**
  The backend must therefore make `GET /fishing` cheap and cache-friendly.
- **Below 10 %:** the main button reads **Collect** but is locked (greyed) and shows a hint of what is still needed
  (e.g. "Collect from 10 %"). At or above 10 % it becomes active (then the collect ad is shown).
- **Player level** = the lower of the two card levels: `level = min(miningRateLevel, capacityLevel)`, range 1–10 (matches
  the 10 level badges). Recomputed on every card upgrade, in the same transaction.
- **No countdown:** only the bar and the "coins / capacity" numbers are shown (no "full in…" timer, no percentage).

### A.2 Menu and account
- **New player:** starts with nothing: 0 coins, 0 stars, both cards at level 1 (player level 1). No welcome bonus.
- **Support / News / FAQ links:** stored as **admin-editable settings** (DB), changed from the admin panel without a redeploy.
  A button whose link is empty is disabled. (`{CONFIG: links.support}`, `{CONFIG: links.news}`, `{CONFIG: links.faq}`)
- **Music:** a **playlist managed in the admin panel**: the admin uploads tracks (file, title, order, active on/off); the app
  gets the list and the next / previous buttons work. Tracks are served as static files with long cache headers (CDN-ready).
  Players keep their own play/pause choice on the device.
- **Blocked player:** the admin can block a player; the app then shows only an **"Your account is blocked"** screen with a
  Support link. The server rejects every game action for that player (`ACCOUNT_BLOCKED`), the bot stops sending notifications,
  and nothing pending is paid out (e.g. Autocollect, withdrawals are frozen). Admin can unblock; every block/unblock is audited.

### A.3 Stars top-up (the panel opened by the green "+" on the stars balance)
Stars are the premium currency. They are bought with Telegram Stars or with TON.
- **Telegram Stars:** the player pays through a Telegram invoice (Stars/XTR). **1 Telegram Star = 1 game star** by default;
  the ratio is an admin setting `{CONFIG: topup.telegramStars.ratio}`.
- **TON:** the player pays with **TON Connect**: confirms a transfer to the project wallet in their wallet app. The server
  creates a payment order with a unique comment/payload, watches the blockchain (TON indexer/API) for that exact transfer and
  credits the stars **automatically** after the required confirmations. The player sees "pending" until then.
  `{CONFIG: topup.ton.walletAddress}`, `{CONFIG: topup.ton.confirmations}`, `{CONFIG: topup.ton.orderExpiryMinutes}`.
  Stars-per-TON conversion is an admin setting `{CONFIG: topup.ton.starsPerTon}` (the screen shows the TON price per pack).
- **Packs:** a fixed list of packs (the screen shows 200 / 500 / 1,000 / 2,500 / 5,000 / 10,000 / 25,000 / 50,000 stars),
  **managed in the admin panel** (add / edit / remove / order / enable). Values are supplied later.
- **Bonus:** the **TON tab bonus (shown as "+10%")** applies to **TON payments only**. The percentage is **editable in the
  admin panel** at any time (e.g. change 10 % to 20 %) and the app shows whatever the admin set, so the badge and the real
  bonus always match. `{CONFIG: topup.ton.bonusPercent}`; the Telegram Stars method has `{CONFIG: topup.telegramStars.bonusPercent}`
  = 0 by default. The bonus is part of what is credited and shown in the ledger as separate lines (purchase + bonus).
- **Failures and delays:** a payment that is not yet confirmed stays **PENDING** and is re-checked by the server until it is
  confirmed, failed or expired; the player can see the status. Credits are idempotent (one payment = one credit, unique
  payment id). If Telegram **refunds** a payment, the stars that came from it are deducted; if they were already spent the
  balance may go negative, the account is **flagged for admin review** and the admin decides (block, forgive, recover).
- Every purchase and bonus is a ledger row; the admin panel lists payments with status and can re-check one manually.

### A.4 Shop (cards and boosters) — additions to sections 1.5 and 1.6
- **Card price currency is chosen per level in admin:** each level row has `cost` and `currency` (coins or stars).
  The screen shows the right icon and checks the right balance. `{CONFIG: cards.*.levels[n].{cost,currency}}`
- **Upgrade conditions are admin-defined per level**, in addition to the price. A condition has a type and a value; supported
  types (extensible): invite N friends · player level at least N · the other card at least level N · complete a given task.
  The server evaluates all conditions on every `GET /shop/cards` and on upgrade; the response lists each condition with its
  description (RU/EN text from admin) and `met` true/false, so the screen can show "Needs: …". An upgrade is rejected with
  `REQUIREMENT_NOT_MET` while any condition is unmet. `{CONFIG: cards.*.levels[n].requirements[]}`
- **Boosters are bought with stars** (price per booster set in admin, `{CONFIG: boosters.<type>.priceStars}`).
- **A booster takes effect immediately.** The 1.5× boost applies from the purchase second to a session that is already
  running (the server splits mining into segments with the old and new speed). Autocollect is active from the purchase
  moment on. The 30 days run from the purchase time.

### A.5 Prize games: Fortune Spin, Mega Chest, gift collection, payout guard
All draws are made **on the server** with a cryptographically secure random generator; the client only plays the animation.
Every draw is stored (player, game, tier, prize, stars spent, time) and creates ledger rows.

**Fortune Spin**
- 5 price tiers (stars per spin, `{CONFIG: spin.tiers[].priceStars}`), pack sizes 1× / 3× / 5× / 10×. A pack is charged **in one
  transaction before the draws**; every spin in the pack is an independent draw; the response lists all results in order.
- The wheel shows **8 gifts** (Frog, Heart, Cap, Panda, Apple, Cat, Ring, Watch), each with a **star value**
  (`{CONFIG: gifts[].starValue}`, admin-editable together with name and picture).
- **Same gifts on every tier; higher tiers have better odds** for the rare, valuable gifts: each tier has its own odds table
  (`{CONFIG: spin.tiers[].odds[gift]}`, weights editable in admin).
- Winners and Prizes lists: **Prizes** returns the tier's gifts with value (and the odds the admin chooses to publish);
  **Winners** is a live feed of recent big wins (username or anonymous, prize, stars spent, time).

**Mega Chest**
- A chest can contain **coins, stars and small gifts** (never the most valuable gifts); the prize pool and weights are
  admin-editable `{CONFIG: chest.pool[]}`. Round rules (cost, number of picks) are in A.6.

**Gift collection** (new page, opened from the **hamburger menu**)
- A won gift is stored in the player's **collection** (one row per gift, status `OWNED`).
- **Sell:** the player can sell an owned gift for its **full star value**; stars are credited with a ledger row; the gift
  becomes `SOLD`. One gift can be sold or claimed only once (atomic status change).
- **Claim real Telegram gift:** the player presses Claim → status `CLAIM_REQUESTED`. An **admin sends the gift by hand** in
  Telegram and marks it `DELIVERED` in the admin panel; the player gets a bot message. The admin can also `REJECT` (with a
  reason) and the gift returns to `OWNED`. The player may need a Telegram username/account that can receive gifts.
- The collection page needs: list of gifts with status, Sell and Claim buttons, history. (**New screen for the front end.**)
- Admin: list of claim requests, mark delivered/reject, see every player's collection.

**Payout guard (automatic)** `{CONFIG: guard.maxPayoutRatio, guard.windowHours}`
- The server tracks the real payout ratio (value paid out ÷ stars spent) per game over a rolling window.
- While the ratio is **above the limit**, a draw that lands on a big prize is **re-rolled toward the next smaller prize**.
- Owner decision. **Risk note:** because players cannot see this, the published odds are not the real odds while the guard is
  active. Recommended: publish only the base odds as "typical", keep the guard limit high enough to rarely trigger, and log
  every guard intervention (player, original prize, replaced prize) so it can be audited and, if needed, disclosed.
- Admin shows the live payout ratio per game and tier and every guard intervention.

### A.6 Mega Chest rounds and Bruno's Ride
**Mega Chest**
- One **round** = the player pays once (stars, `{CONFIG: chest.roundCostStars}`) and opens **3 chests** (`{CONFIG: chest.picksPerRound}`).
- **All prizes are drawn when the player pays** and stored on the server in a hidden order; each pick reveals the next stored
  prize. What the player taps (which chest, when) cannot change the outcome. The draw and the charge are one transaction.
- **An unfinished round stays open.** If the player leaves (closes the app, loses connection), the server keeps the paid
  round with its remaining picks; when they return the app resumes it (a still-open round is returned by `GET /chest`).
  Optional expiry `{CONFIG: chest.roundExpiryHours}` (empty = never). A new round cannot be paid while one is open.
- Each prize is credited when the chest is opened (ledger row); coins/stars go to the balance, gifts to the collection.
- The "unpicked chests" shown at the end are decorative (random values, not real prizes).

**Bruno's Ride**
- The game runs on the player's phone. Each run costs stars (`{CONFIG: ride.runCostStars}`), charged when the run starts.
- `POST /ride/start` charges the star(s) and creates a **run id** with an expiry; `POST /ride/finish {runId, coins, score,
  duration}` is accepted **once per run id**, only before expiry, and only if the result is **plausible** (minimum duration,
  at most N coins per second after the spawn delay, score ≤ duration × max points per second; all limits are admin settings
  `{CONFIG: ride.*}`). Implausible results are rejected and logged (and flagged for the admin if repeated).
- **Daily cap:** each player can earn at most `{CONFIG: ride.maxCoinsPerDay}` coins from Ride per game day (UTC); once reached,
  further runs still can be played for score but pay 0 coins (the app shows a notice).
- **Reward:** the **coins collected** in the run are credited to the **coin balance** (they count towards withdrawals) with a
  ledger row. The **score** is stored; the player's **best score** is kept and shown as "Top score".
- A run that is paid but never finished simply expires (the star is not refunded).

### A.7 Winners feed and Tasks
**Winners feed (Spin):** lists **every gift win on any tier**, newest first, with the Telegram **@username** (or "Anonymous" when
the player has no username), the gift, stars spent and time. Only real wins, never invented entries. Paged (infinite scroll).
The feed endpoint is cheap: it reads from a small pre-built recent-wins table, not from the full draw history.

**Tasks** (screen with Main / Others tabs, "NEW" tags, a red badge on the Tasks tab when something new is available)
- Task **types supported**: (1) **join a Telegram channel/chat** — verified by the bot (`getChatMember`; the bot must be an admin
  of that channel/chat); (2) **invite N friends** — counts the player's referred friends, verified from referral data;
  (3) **open a link / visit a website** — completes after the player taps Open and a check call is made after a minimum delay
  (no real verification is possible, so the delay `{CONFIG: tasks.linkMinSeconds}` is the only guard); (4) **watch N ads** —
  verified by the server through the Adsgram proofs (same mechanism as section 1.4), progress counted per task.
- Each task has: type, category (Main / Others), RU and EN title and description, link/channel, required count, **reward**
  (coins and/or stars, `{CONFIG: per task}`), sort order, active on/off, optional **start/end dates**, and **repeat mode chosen by
  the admin per task: one-time, daily, or weekly**. Repeat modes reset on the game-day boundary (00:00 UTC; weekly = Monday).
- The server stores per-player progress and completion with a unique key per (player, task, period) so a reward is never paid
  twice. `GET /tasks` returns tasks with `isCompleted`, `progress`, `isNew` (created after the player last viewed the list) and
  the count of unseen new tasks (for the badge).
- Completing a task credits the reward with a ledger row and may fire a bot notification (new task available).

### A.8 Tournaments (general system; new screen)
A tournament is created in the admin panel. Players are ranked by one **metric** chosen per tournament:
**stars spent** (in Spin, Chest, Ride, during the tournament) · **best Ride score** · **coins mined** (collected from fishing) ·
**friends invited** (new friends who joined via the player's link). More metrics can be added later without changing the
structure (each metric = a counter updated by the same domain events that already write the ledger).
- **Joining requires an entry fee** paid in **stars** (`{CONFIG: tournament.entryFeeStars}`, set per tournament; 0 = free).
  Only joined players are ranked; only progress **after joining** counts. The fee is charged in one transaction with the join
  (ledger row); fees are **not refunded** unless the admin cancels the tournament (then all fees are refunded).
- **Prizes:** fixed prize per place (1st, 2nd, 3rd …): coins, stars and/or a gift, set per tournament in admin.
- **Payout needs admin confirmation:** when the tournament ends the leaderboard is **frozen** and appears in the admin panel for
  review (anti-cheat); the admin presses **Pay** (or edits/removes a suspicious entry first). Payment then credits all prizes
  in one batch (idempotent, ledger rows) and the bot notifies the winners. Ties are broken by who reached the score first.
- **Where players see it:** a **banner card at the top of the Games page** (tournament name, metric, time left, entry fee or "joined",
  your rank). Tapping it opens a leaderboard screen (**new screen**): top places with @username and value, the player's own rank,
  prize list, Join button. Live leaderboard reads come from a maintained ranking structure (not a heavy query on every view).
- Open details (proposals): several tournaments at the same time are allowed (one per metric at a time); the leaderboard is
  refreshed in near-real-time; tournament start/end/last-hours/results are bot notifications (RU/EN).

### A.9 Friends and referrals
- **Links (both accepted, same attribution):** Mini App link `https://t.me/ImTomsBot/app?startapp=ref_<playerId>` (the one the app
  shares) and bot link `https://t.me/ImTomsBot?start=ref_<playerId>`. Attribution happens **only on a brand-new player's first
  entry**; an existing player can never be attributed later; self-invite is ignored; **the first inviter is permanent**.
- **A friend counts as "invited"** (for the counter, tasks, tournaments and rewards) **only once they reach player level 2**
  (both cards upgraded at least once). Before that they may be listed as "pending" but do not count.
- **Inviter earns (all in coins):**
  1. a **level reward** each time a counted friend reaches level 2, 3 … 10, **once per friend per level** (unique key); amounts
     per level `{CONFIG: referral.levelRewards[2..10]}` (admin-editable);
  2. a **share of the friend's mining**: on every collect / autocollect of that friend the inviter gets `{CONFIG: referral.miningPercent}`
     % of the collected amount as extra coins created by the system (the friend's coins are not reduced); **capped per friend**:
     once the inviter has earned `{CONFIG: referral.miningCapPerFriend}` coins from that friend, the share stops.
- **Friend's one-time join bonus** (coins): paid **when the friend reaches level 2**, once. **Default 0 for everyone.** The admin
  can set a **different bonus for one specific inviter** (e.g. 100 for a YouTuber's link): stored on the inviter's record
  (`inviteBonusCoins`, null = use default). The bonus that applies is the inviter's value **at the moment the friend joined**
  (snapshotted on the friend), so later changes do not affect existing friends. Only the join bonus is overridable per inviter;
  level rewards and the mining % are global.
- **Friends screen data:** total counted friends, total coins earned from friends, the list of friends (name, level, joined date),
  the reward table (level → coins), and the invite link. All reads are paged.
- Ledger rows for every referral payment (reason, friend id, level) and an admin view of the referral tree, totals per inviter,
  and a **fraud view** (many accounts from one inviter in a short time, same-device signals if available).
- Bot notifications: "A friend joined", "Your friend reached level N — you earned X coins" (category *friends*).

### A.10 Withdrawals (coins → TON) and history — details of section 1.8
Numbers (rate, fee, minimum, maximum, limits, cooldown) are supplied later as `{CONFIG}`; the rules are decided:
- **Rate and fee are fixed values set by the admin:** `{CONFIG: withdrawal.coinsPerTon}`, `{CONFIG: withdrawal.feeTon}` (fixed fee in TON,
  deducted from the payout). The player receives `coins ÷ coinsPerTon − fee`; the request is rejected if that is ≤ 0. The rate and
  fee **valid at the moment of the request are stored on the request** (later changes never alter an existing request).
- **Address:** typed/pasted by the player each time; the server validates the TON address format (raw and user-friendly forms) and
  rejects invalid ones. (No wallet binding.) Bot message on every withdrawal so a stolen-account payout is noticed by the owner.
- **Who can withdraw (all enforced by the server, each limit admin-editable):**
  - **only one open request at a time** (pending/processing) → `WITHDRAWAL_ALREADY_OPEN`;
  - **cooldown** between requests `{CONFIG: withdrawal.cooldownHours}`;
  - **minimum player level and/or account age** `{CONFIG: withdrawal.minLevel, withdrawal.minAccountDays}`;
  - **per-request minimum and maximum** and a **daily / total limit per player** `{CONFIG: withdrawal.min, .max, .dailyLimit, .totalLimit}`;
  - the account is not blocked and not flagged.
- **Coins are deducted when the request is created** (ledger row, in the same transaction as the request). If the request ends
  **FAILED or CANCELLED**, the coins are returned (ledger row).
- **Payout: automatic below a limit, manual above.** Requests with `amountTon ≤ {CONFIG: withdrawal.autoPayoutMaxTon}` are sent by a
  server payout job from a hot wallet; larger ones wait in the admin panel until an admin sends the TON by hand and pastes the
  transaction hash (or presses Reject). Safety: a **master switch** (`AUTO_PAYOUT`, off by default), a **daily total cap** for
  automatic payouts, a **single hot-wallet key kept only in the server environment/secret store**, never in the repo or the
  database, idempotent sending (one request = at most one transfer, tracked by a stored payout id before the transfer) and a
  reconciliation job that confirms the transfer on-chain and stores the **tx hash**. On any doubt the request stays in review
  instead of being re-sent.
- **Statuses** (as the screen): PENDING → PROCESSING → COMPLETED (with `txHash`) / FAILED / CANCELLED. The player can cancel
  only while PENDING (coins returned).
- **History screen:** paged list (amount in coins and TON, address, status, tx hash, date) and the notice texts (processing
  time, share-your-payout-in-chat) which are admin-editable RU/EN texts.
- Bot notifications (mandatory category): request created, sent (with the hash/link), failed/cancelled (coins returned).
- Admin: queue of requests with filters, approve/send/reject, bulk actions, payout totals per day, wallet balance (read-only
  from the chain) and the automatic-payout switch and limits.

### A.11 Levels and the admin panel
**Levels**
- Levels 1–10 with the colour badges (names Ember, Meadow, Lagoon, Silver, Gold, Ruby, Amethyst, Emerald, Obsidian, Legend).
- **Level-up reward:** reaching each level 2…10 pays **coins and/or stars** (either may be 0) **once per player per level**, set per
  level in admin `{CONFIG: levels[n].rewardCoins, levels[n].rewardStars}`. The reward is credited in the same transaction as the card
  upgrade that caused the level-up (ledger row, unique key per player+level) and returned in the upgrade response so the level-up
  popup can show it. (Level-up reward for the player is separate from the inviter's referral reward in A.9.)

**Admin panel**
- **The admin UI is rebuilt from scratch later.** This project provides a complete, documented **admin API** (versioned, separate
  from the player API, own authentication). The old admin app is not reused.
- **Roles:** a **super admin** (everything, can create admins and assign roles) plus limited roles — **Finance** (withdrawals, payments,
  payout settings), **Support** (view players, block/unblock, gift claims, notes), **Content** (tasks, combo, music, texts, links,
  notification templates). Permissions are checked per endpoint (RBAC) and are editable.
- **Login: password only** (owner decision) with hardening that costs the owner nothing: strong-password rules, hashed with a
  modern algorithm (argon2id), login-attempt limiting and temporary lockout, short-lived sessions with refresh/revoke, the admin
  API reachable only over HTTPS, a bot alert on every admin login from a new device/IP, and a complete **audit log** (who, what,
  before/after, IP, time). A second factor (TOTP) can be switched on later without changing the design. **Risk note:** a password-only
  admin that can approve payouts is the most attractive target of the system; recommended to keep the Finance role small.
- Every setting marked `{CONFIG}` in this document is edited in the admin API with validation, change history and (for money-like
  settings) an optional "apply from date" so a change never alters running sessions or open requests.

### A.12 Launch, hosting, reliability and texts
- **No data migration:** v4 launches with an **empty database**. v3 is not touched or imported (it may be kept read-only for
  reference). All balances, levels and history start new.
- **Hosting:** the owner's **VPS with Docker Compose**: services `api` and `worker` (the same Go image, different command),
  `postgres`, and a reverse proxy with automatic HTTPS (Caddy or nginx). Deploy by one command/script (build image → migrate →
  rolling restart), health checks (`/healthz`, `/readyz`), automatic restart on crash, graceful shutdown (in-flight requests and
  jobs finish). Secrets only in environment/secret files on the server, never in the repository. Separate staging and production
  configs; staging uses the Telegram test environment.
- **Backups and monitoring (the database holds money-like balances):** automated PostgreSQL backups with **point-in-time
  recovery** (continuous WAL archiving) plus a daily base backup, **copies stored off the server** (encrypted), and a tested
  **restore procedure** (a restore drill is part of the acceptance). Metrics and logs with **alerts to a Telegram admin chat**:
  API errors and latency, worker lag, notification queue age, failed or stuck payouts and payments, database size/disk space,
  backup failures, the payout guard triggering, unusual spikes (withdrawals, referrals, ad views).
- **Language of server texts:** every error has a stable **code** and the server returns the message **already in the player's
  language (RU/EN)** (`Accept-Language` or the saved language). All admin-written content (tasks, FAQ/notices, notification
  templates, level/gift/card names, combo image names) is stored **in both languages** and the admin API requires both.

### A.13 Ads everywhere (Adsgram) — extends section 1.4
All ad uses share **one mechanism** (1.4): request an ad view with a `placement` → Adsgram reward webhook marks it completed →
the action consumes it exactly once. Placements: `FISHING_START`, `COLLECT`, `BONUS_COINS`, `DOUBLE_STREAK`, `DOUBLE_COMBO`,
`COMBO_EXTRA`, `RIDE_FREE`, and task ads (`TASK_ADS`). Every placement can be switched on/off in admin.
- **No ad available (no fill / unsupported region / blocker):** the player must try again, but **after N consecutive failed ad
  loads** (`{CONFIG: ads.maxFailedLoads}`) the action is **allowed without an ad** for a short window; it is flagged in the log and
  counted in admin so abuse of this fallback is visible. The app reports a failed load to the server (`POST /ads/failed`).
- **Bonus ad ("free coins"):** a button on the **Tasks page** (card at the top with a daily counter, e.g. 2/5). Each verified ad
  pays **coins** `{CONFIG: ads.bonus.coins}`, at most `{CONFIG: ads.bonus.maxPerDay}` per game day (UTC). (**New front-end card.**)
- **Double reward — chosen before claiming:** for the **daily streak** and the **daily combo** the screen offers **Claim** and
  **Claim ×2 (watch an ad)**. Exactly one is taken. ×2 requires a verified ad view first and pays double the reward in a single
  credit (ledger notes the doubling). No second chance to double after claiming a plain reward.
- **Extra Daily Combo attempt (ad):** shown **only after the free daily attempt is used**. Allowed `{CONFIG: ads.comboExtra.maxPerDay}`
  times per day. **Only the best attempt of the day counts and the reward is paid once** (the best number of correct pictures); an
  extra attempt cannot pay twice. The correct pattern is still never sent; only the per-slot tick/cross.
  - Reward flow with the claim choice: after each attempt the player sees the result and the reward of the **best attempt so far**;
    they press **Claim** or **Claim ×2 (ad)**, which locks the combo for that day. **[PROPOSAL — to confirm, D5]** a reward not claimed
    by the end of the game day is credited automatically (single, not doubled) by a server job.
- **Free Ride run (ad):** the ad option appears **only when the player cannot pay the star price**; one verified ad pays one run
  instead of the stars; limited to `{CONFIG: ads.rideFree.maxPerDay}` per game day. The daily Ride coin cap (A.6) still applies.
- **Watch N ads (task type):** each verified ad counts toward the task (A.7).
- Anti-abuse for all: one ad view = one use; rate limits per player and per IP; the daily limits above; ad history for audit; a
  player with an unusual ad pattern is flagged for the admin (A.14).

### A.14 Coin precision and anti-fraud
- **Coins are whole numbers.** The tank accumulates with decimals (e.g. 0.025 coins/s); on **Collect the amount is rounded down to a
  whole number** and credited; balances, prizes, costs, rewards and withdrawal amounts are always whole coins (stored as integers).
  Stars are whole numbers too. TON amounts are stored in nanoTON (integer, 9 decimals). The tank amount is stored/transported with
  enough precision (fixed-point, e.g. micro-coins) so it never drifts.
- **Anti-fraud: flag plus automatic holds.** The server records signals per player: IP and IP changes, device/browser hints,
  Telegram account age and Premium flag, referral clusters (many accounts under one inviter, same IP/device), abnormal ad and action
  rates, implausible Ride results, many gift sells/claims, rapid withdrawal after account creation. Signals produce a **risk score
  and flags** visible in the admin player page. Clearly suspicious accounts get an **automatic temporary hold** on withdrawals and on
  referral/tournament payouts until an admin reviews (the player is not told why; the app shows a neutral "under review" for the
  affected action). **No automatic permanent bans** — only a human blocks (A.2). Thresholds are admin settings `{CONFIG: risk.*}`;
  every automatic hold and its reason is logged and reversible by the admin.

### A.15 Front-end changes this ToR requires (the app is "ready" but these are new)
1. **Real ad flow** (Adsgram SDK) before Fishing and Collect; failure / no-fill handling; locked Collect button with the 10 % hint (A.1).
2. **Gift collection page** (hamburger menu): owned gifts, Sell, Claim, history (A.5).
3. **Tournament banner on Games + leaderboard/join screen** with entry fee (A.8).
4. **Stars top-up:** real Telegram Stars invoice and TON Connect payment, pending state, bonus % read from the server (A.3).
5. **Claim / Claim ×2 (ad)** on the daily streak and the daily combo; combo extra-attempt (ad) and best-attempt result (A.13).
6. **Bonus-ad card on the Tasks page** with daily counter; **Free Ride (ad)** option when stars run out (A.13).
7. **Blocked-account screen**, "under review" notices for held actions (A.2, A.14).
8. **Card conditions ("Needs …")** and per-level currency icons in the Shop (A.4); **level-up popup shows the reward** (A.11).
9. **Chest round resume** (an open paid round is restored) (A.6); **music next/previous** enabled (A.2).
10. **Server-translated error texts** and notification settings link to the bot (A.12, section 6).
11. Remove polling of the fishing state; use the local animation with a server snapshot (A.1).

### A.16 Precedence
Where this Appendix differs from sections 0–8, **the Appendix wins** (it is the later, screen-by-screen decision). Known updates:
daily combo extra attempts and claim flow (A.13 replaces the single-attempt payment in 7.3), withdrawal rules (A.10 extends 1.8),
Autocollect/boosters unchanged (A.4), tech stack unchanged.

### A.17 Daily combo finalised, and bot timing and extras
**Daily combo (settles the open points of 7.3 and A.13)**
- **Always 4 slots** out of the 8 pictures (`{CONFIG: combo.slots}` = 4; the admin cannot change it per day).
- **No repeats anywhere:** a pattern uses 4 different pictures and the player can use each picture once, so the answer is an ordered
  selection of 4 distinct pictures; the server rejects duplicates (`COMBO_INVALID_ITEMS`) and the admin pattern editor refuses them.
- **An unclaimed combo reward is lost** at the daily reset (decision D5). The result and the best attempt of the day are shown until
  the reset; the player must press **Claim** or **Claim ×2 (ad)** before 00:00 UTC. The server does not auto-credit it. (The streak
  works the same way: a missed claim means a missed day.)
- **Streak and combo are free for everyone:** no minimum level, no first-fishing requirement, no mandatory ad (only the optional
  ×2 / extra-attempt ads of A.13).

**Bot timing (extends 6.2–6.3 and 7.7)**
- **"Reward ready" messages** (streak, combo, tasks, etc.) are sent near **each player's own best hour**: the server learns the
  usual hour(s) a player opens the app (from their last N sessions, in UTC) and schedules the message for that hour; players with
  no history get a default hour `{CONFIG: notifications.defaultHourUtc}`. A message is skipped if the player already did the thing.
- **Warnings before the reset:** two messages, **6 hours** and **1 hour before 00:00 UTC** (`{CONFIG: notifications.warnHours = [6,1]}`),
  for streak at risk and combo last chance; the second one is sent only if the player still has not acted.
- **Admin broadcasts:** the admin can send announcements (news, events, maintenance) **to everyone or to a segment** (language, player
  level, inactive for N days, has an active booster, has an open gift claim, referral count, etc.). The text is written in RU and EN
  (with optional picture and a Mini App button), can be scheduled, is sent slowly within Telegram limits through the same queue, and
  shows delivery statistics (sent, failed, blocked). Segments are evaluated when the broadcast starts. A test-send to the admin first
  is required.
- **Free text to the bot:** replies in the player's language with a short help message and buttons **Open game / Support / Language**.

### A.18 Legal, dashboard, maintenance and privacy
- **Legal:** none in v4 for now — no terms/age screen and no country restrictions. (The data model keeps an optional
  `terms_accepted_at` and a country-from-IP field so they can be switched on later without migrations; the owner is responsible for
  compliance outside the app. **Risk note:** coins convert to real money and games use paid stars, which is regulated in many
  countries; recommended to revisit before large-scale launch.)
- **Admin dashboard API** provides: **players** (new per day, DAU/MAU, retention after 1/7/30 days, by language); **money in and out**
  (stars bought via Telegram Stars and TON, TON paid out, pending withdrawals, wallet balance, net per day); **ads** (requested /
  completed per placement, failed loads, no-ad fallback uses, estimated income from an entered rate); **game economy health** (coins
  and stars in circulation, coins mined vs spent, payout ratio per game and tier, guard interventions, gifts held by players).
  Numbers come from pre-aggregated daily tables built by a worker job (the dashboard never scans the big tables live).
- **Maintenance:** an admin **maintenance switch** (friendly RU/EN "Back soon" screen with an optional end time; the player API answers
  `503 MAINTENANCE`, admin API keeps working) and a **minimum app version** setting; the app sends its version in a header and the
  server answers `426 UPGRADE_REQUIRED` for older versions so cached apps reload.
- **Privacy / retention:** a player can **ask for account deletion** (through Support); an admin runs "delete player": personal data
  (name, username, photo reference, language, IPs, device hints) is erased and the player is replaced by an anonymous id, while
  money records (ledger, payments, withdrawals) are kept **anonymised** for accounting. Technical logs, expired ad views and old
  notification-send logs are **purged automatically** after `{CONFIG: retention.logsDays}`, `{CONFIG: retention.adViewsDays}`.

### A.19 Currencies, rankings, notification categories, Premium
- **Currency flows (final):** **coins** are earned by mining, Ride, chests, tasks, referrals, daily rewards and ad bonuses; coins are
  spent on cards (where the admin sets coins) and are the **only path to real TON** (withdrawal, A.10). **Stars** are bought (Telegram
  Stars / TON, A.3) or won (Spin, Chest, level and tournament rewards, gift sales) and are spent on boosters, games, tournament fees and
  card levels priced in stars. **Stars cannot be withdrawn and cannot be converted** into coins or TON, and coins cannot be converted
  into stars. There is no exchange feature.
- **Ride ranking:** only the personal **Top score** (best score per player). No global leaderboard; rankings exist only inside
  tournaments (A.8).
- **Notification categories** (the player's /notifications list): (1) **Fishing** — tank full, collect reminder, start fishing,
  autocollect; (2) **Daily** — streak and combo messages; (3) **Boosters** — expiring/expired; (4) **Friends** — joined, level
  reward; (5) **Games & tournaments** — tournament start/end/results, gift claim updates; (6) **News** — admin broadcasts;
  (7) **Payments & withdrawals** — top-up status, withdrawal status, security alerts — **always on, cannot be switched off**.
  Everything else can be switched off per category; `/stop` stops everything except category 7.
- **Telegram Premium:** no special treatment (no bonus, no separate setting).

---

## 10. Build plan, deliverables and acceptance

### 10.1 Deliverables
1. A Go backend (API + worker + bot) in the new repository `brunoplay-back-v4`, with migrations, tests, Dockerfile and Docker Compose.
2. OpenAPI documentation of the player API and the admin API; a Postman/HTTP collection.
3. A deployment guide and scripts (build, migrate, deploy, rollback), backup/restore scripts and the restore drill (A.12).
4. A front-end integration guide: what changes in the Vue app (A.15), error codes, snapshot-based fishing state.
5. The values checklist (section 11) as a ready-to-fill admin settings screen/file, with the "unconfigured" state (9.2).

### 10.2 Milestones (each is deployable and tested before the next)
| # | Milestone | Content |
|---|---|---|
| M1 | Foundation | Project skeleton, config, migrations, auth (Telegram initData), players, ledger, settings + history, admin auth/RBAC/audit, health, maintenance/version gate |
| M2 | Core game | Fishing sessions and segments, ads (requests, webhook, single use, fallback), cards + conditions, boosters, levels + rewards, autocollect job |
| M3 | Money | Stars top-up (Telegram Stars, TON watcher), withdrawals (rules, manual + automatic payout, refunds), reconciliation |
| M4 | Daily + tasks + friends | Streak, combo (+ ads, claim flow, auto-pattern), tasks (4 types, repeat modes), referrals, bonus ad |
| M5 | Games | Spin (tiers, odds, guard), Chest rounds, Ride (runs, plausibility, daily cap), gift collection (sell/claim), winners feed |
| M6 | Tournaments + bot | Tournaments (all metrics, entry fee, freeze, payout), the bot (commands, categories, scheduler, RU/EN templates, broadcasts) |
| M7 | Operations | Dashboard aggregates, anti-fraud signals and holds, backups + PITR, alerts, load test, security review, privacy tools, final docs |

### 10.3 Acceptance criteria
- All automated tests pass (section 5) on a real PostgreSQL; the money paths have **no double credit/charge** under concurrent
  and repeated requests (fuzz/race tests included).
- A **load test** on the owner's VPS size: the target numbers (concurrent players, requests/second, p95 latency) are agreed with
  the owner at M7 start; the build must meet them with headroom.
- The restore drill succeeds; a payout cannot be sent twice; the payout guard and automatic holds are visible in admin.
- Security review checklist: secrets only in environment, HTTPS only, initData signature and age checks, webhook secrets, admin
  lockout and audit, rate limits, no debug/dev modes in production, dependency scan.
- The front-end can run against v4 for every flow in the app (with the changes listed in A.15).

### 10.4 Assumptions about the owner's side
Bot token and @ImTomsBot webhook access · Adsgram account and block ids · a project TON wallet (and, only if automatic payout is
enabled, a hot wallet with its key kept in the server secret store) · the VPS access for Docker · a Telegram admin chat for alerts.
Credentials are given directly to the server environment, never written in the repository or in chat.

## 11. Values the owner supplies after the backend is built (the {CONFIG} inventory)
| Area | Values |
|---|---|
| Fishing | Mining-rate and capacity level tables (levels 1–10); upgrade cost and currency per level; level requirements (conditions) |
| Boosters | Price (stars) of Autocollect and 1.5×; duration (30 days) |
| Levels | Reward (coins/stars) for levels 2–10 |
| Daily | Streak rewards for days 1–10; combo coins per correct picture; extra-attempt limit; double-reward settings |
| Ads | Adsgram block ids and secret; view expiry; failed-loads fallback N; bonus-ad reward and daily limit; free-Ride daily limit |
| Stars top-up | Packs (amounts); Telegram-Stars ratio; stars per TON; TON bonus % (admin-editable); wallet address; confirmations |
| Withdrawals | Coins per TON; fee (TON); minimum/maximum; cooldown; minimum level and account age; daily/total limit; automatic-payout limit and daily cap |
| Games | Spin tier prices and odds per tier; gift star values; Chest cost, picks, pool and odds; Ride run cost, plausibility limits, daily coin cap; payout-guard ratio and window |
| Friends | Referral level rewards (levels 2–10); mining share % and cap per friend; default friend join bonus (0); per-inviter overrides |
| Tasks | Each task's reward, repeat mode, dates, texts (RU/EN) |
| Tournaments | Per tournament: metric, dates, entry fee, prizes |
| Bot | Message texts RU/EN; default hour; reminder hours; cooldowns; daily cap; quiet hours; collect-reminder and idle hours; booster-expiry days |
| Content | Support/News/FAQ links; music tracks; combo images (8) and patterns; gift names and pictures; level names (RU/EN) |
| Risk and retention | Risk thresholds; log and ad-view retention days |
Until a value is supplied the matching feature returns a configuration error (9.2); nothing is guessed.
