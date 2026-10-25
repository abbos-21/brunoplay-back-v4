# Front-end integration guide

Everything the Vue app must do to talk to v4. All routes are listed in `API.md`; owner values in `SETTINGS.md`.
Base path: `/api`. JSON in, JSON out.

## 1. Conventions

| Topic | Rule |
|---|---|
| Sign-in | `POST /auth/telegram { "initData": Telegram.WebApp.initData }` → `{ token, expiresAt, isNew, user }`. Send `Authorization: Bearer <token>` afterwards. Tokens last 12 h: on `401 AUTH_EXPIRED` sign in again silently. |
| Headers | `X-App-Version: 1.2.3` on every request (maintenance / forced reload), `Accept-Language: ru|en` before sign-in, `Idempotency-Key: <uuid>` on every money POST (retry with the **same** key after a network error: you get the stored answer, never a second payment). |
| Errors | `{ "error": { "code": "TANK_BELOW_MINIMUM", "message": "…in the player's language…", "params": {…}, "requestId": "…" } }`. Switch on `code`, show `message`. |
| Special codes | `503 MAINTENANCE` (`params.text`, `params.until`: show the "back soon" screen), `426 UPGRADE_REQUIRED` (reload the app), `403 ACCOUNT_BLOCKED` (show the blocked screen; `GET /me` and `GET /app-info` still work), `503 CONFIG_MISSING` (feature not configured yet), `403 UNDER_REVIEW`, `429 RATE_LIMITED` (`Retry-After`). |
| Numbers | Coins, stars, costs, prizes: whole numbers. The tank is the only decimal (`tankCoins`). TON amounts are decimal strings (`"0.095"`) plus `…Nano`. |
| Time | Always trust `serverTime` / `resetsAt` from the server. The game day is 00:00–23:59 **UTC** for everyone. |

`GET /app-info` (no sign-in, works during maintenance) returns `supportUrl`, `newsUrl`, `faqUrl`, `maintenance{enabled,until,textEn,textRu}`, `minVersion`, `botUsername`, `topupTonBonusPercent`.

## 2. Home — fishing (no more 2.5 s polling)

`GET /fishing` is a **snapshot**. Animate locally and refresh on app open, after each action, on focus and about every 30 s:

```json
{ "isFishing": true, "tankCoins": 1.5, "tankCapacity": 5, "miningRate": 0.025, "effectiveMiningRate": 0.0375,
  "isComplete": false, "fullAt": "2026-10-24T12:03:20Z", "serverTime": "2026-10-24T12:01:00Z",
  "canCollect": true, "collectMinPercent": 10, "collectMinCoins": 1,
  "nextRateChange": { "at": "...", "ratePerSecond": 0.025 }, "adsRequired": true }
```
Local tank = `min(tankCapacity, tankCoins + effectiveMiningRate × (now − serverTime))`, switching to `nextRateChange.ratePerSecond` at `nextRateChange.at`.
The big button: not fishing → **Fishing**; fishing and `!canCollect` → locked **Collect** with the hint (`collectMinPercent`); `canCollect` → **Collect**.

Ad flow (both buttons): `POST /ads {placement:"FISHING_START"|"COLLECT"}` → `{adViewId, blockId, required}` → show the Adsgram ad for `blockId` →
when it finishes call `POST /fishing/start|collect {adViewId}`. If the SDK cannot load an ad call `POST /ads/failed {placement}`; after N failures
the answer has `fallback:true` and an `ad.adViewId` you may use without an ad. A refused `collect` (tank too low) does **not** burn the ad proof.
Collect answer: `{coinsCollected, newBalance, autoRestarted:false}`. The session ends at collect; start needs a new ad.

## 3. Shop and levels

* `GET /shop/cards` → `cards[{cardName:"MINING_RATE"|"COIN_CAPACITY", currentLevel, currentValue, nextLevel, nextValue, upgradeCost, upgradeCurrency:"COIN"|"STAR", canUpgrade, requirements[{description, requirementType, requirementValue, met}]}]` — show the currency icon from `upgradeCurrency` and "Needs: …" for the first unmet requirement.
* `POST /shop/cards/{card}/upgrade` → `{newLevel,newValue,newUserLevel,levelUp,rewardCoins,rewardStars,coins,stars}` — play the level-up popup with the reward when `levelUp`.
* `GET /shop/boosters` → `boosters[{type:"AUTO_MINING"|"MINING_BOOST_1_5X", name, description, costStars, durationMinutes, isActive, expiresAt, canBuy}]`; `POST /shop/boosters/{type}/buy` (same kind twice → `BOOSTER_ALREADY_ACTIVE`).
* `GET /levels` → `levels[{level,name,rewardCoins,rewardStars}]`, `current`.

## 4. Daily
* `GET /daily/streak` → `{currentStreak, canClaim, todayReward, todayDay, streakRewards[{day,coins,claimed,isToday}], resetsAt}`; `POST /daily/streak/claim {double?:bool, adViewId?}` (×2 needs `POST /ads {placement:"DOUBLE_STREAK"}` first).
* `GET /daily/combo` → `{items[{id,name,icon}], slots:4, alreadyAttempted, attempts, extraAttemptsLeft, bestCorrect, lastResult, lastCorrect[bool], lastItems[], reward, canClaim, claimed, coinsPerCorrect, resetsAt}` (**no answer in any response**).
* `POST /daily/combo/submit {items:[id,id,id,id], adViewId?}` → `{correct:[bool×4], correctCount, bestCorrect, reward, attempts, extraAttemptsLeft}` — draw the ✔ / ✘ badge per slot from `correct`. Extra attempts need `placement:"COMBO_EXTRA"`.
* `POST /daily/combo/claim {double?, adViewId?}` → `{reward, doubled, newBalance}`; claiming locks the day; an unclaimed reward is lost at the reset.

## 5. Tasks and the bonus ad
* `GET /tasks` → `{tasks[{id,category:"MAIN"|"OTHER",type,title,description,channelUsername,url,required,rewardCoins,rewardStars,isCompleted,progress,isNew,repeat,endsAt}], newCount}`; `POST /tasks/seen` clears the NEW tags and the badge.
* `POST /tasks/{id}/open` (link tasks: starts the timer) → then `POST /tasks/{id}/check` → `{completed, reward, rewardStars, error?, progress, required}` (`completed:false` is a normal answer, not an HTTP error). Ad tasks: `POST /tasks/{id}/ad-progress {adViewId}` with `placement:"TASK_ADS"`.
* Bonus ad card (new): `GET /ads/bonus` → `{coins, perDay, usedToday, left}`; show an ad (`BONUS_COINS`), then `POST /ads/bonus/claim {adViewId}`.

## 6. Friends
`GET /friends` → `{stats{totalReferrals,pendingReferrals,referralEarnings}, friends[{id,username,firstName,level,counted,createdAt}], rewards[{level,reward}], links{inviteLink,botLink}}`. Share `links.inviteLink`.

## 7. Games
* **Spin**: `GET /spin/tiers`, `POST /spin {tier, count:1|3|5|10}` → `{results[{type:"gift",gift,label,giftId,starValue}], spent, stars}`; `GET /spin/winners?page=`.
* **Chest**: `GET /chest` → `{round|null, roundCostStars, picksPerRound}` (a paid round survives leaving the app: resume it); `POST /chest/round` pays once; `POST /chest/pick {slot:0..2}` → `{prize, remaining, finished, results[], totalCoinsWon, totalStarsWon}`. The same slot twice returns the same prize.
* **Ride**: `GET /ride/me` → `{bestScore, runCostStars, coinsLeftToday, freeRunsLeft, canPayWithStars, adOffered}`; `POST /ride/start {adViewId?}` (the ad pays only when stars are short, `placement:"RIDE_FREE"`) → `{runId, startTime, expiresAt}`; `POST /ride/finish {runId, coins, score}` → `{coinsEarned, capped, bestScore, newBest, coinsLeftToday}`.
* **Gifts (new screen)**: `GET /gifts` → `gifts[{id,gift,name,starValue,status:"OWNED"|"SOLD"|"CLAIM_REQUESTED"|"DELIVERED",rejectReason}]`; `POST /gifts/{id}/sell` → `{stars,newBalance}`; `POST /gifts/{id}/claim`.
* **Tournaments (new banner + screen)**: `GET /tournaments` → `tournaments[{id,name,description,metric,startAt,endAt,entryFee,prizes,status,joined,score,rank,timeRemainingMs}]`; `POST /tournaments/{id}/join`; `GET /tournaments/{id}/leaderboard?page=` → `{entries[{rank,username,score}], total, userRank, userScore}`.

## 8. Money
* **Stars top-up**: `GET /topup/packs` → `{packs[{stars, telegramStars, telegramBonusStars, ton, tonNano, tonBonusStars}], tonBonusPercent, tonAvailable}` (the "+10 %" badge shows `tonBonusPercent`). Telegram: `POST /topup/telegram-invoice {packStars}` → `invoiceLink` → `Telegram.WebApp.openInvoice(link)`; the stars arrive when Telegram confirms (poll `GET /topup/orders/{id}` or refresh `GET /me`).
  TON: `POST /topup/ton-order {packStars}` → `{address, amountNano, comment, payload, expiresAt}` → TON Connect `sendTransaction({validUntil, messages:[{address, amount: amountNano, payload}]})`; the server credits it automatically after it finds the transfer (status `PENDING` → `PAID`).
* **Withdraw**: `GET /withdrawals/info` → `{coinsPerTon, coinToTonRate, minimumCoinWithdrawal, maximumCoinWithdrawal, withdrawalFeeTon, balance, notice, canWithdraw, blockedReason, nextWithdrawalAt}`; `POST /withdrawals {targetAddress, amountCoins}` → `{withdrawal}`; `GET /withdrawals?page=` → `{data[], total, page, totalPages}`; `POST /withdrawals/{id}/cancel` (only while `PENDING`).

## 9. Menu and bot
`GET /me/notifications` / `PATCH /me/notifications {allOff?, off?:["fishing",…]}`; `PATCH /me/language {language:"EN"|"RU"}` (the bot uses the same language).
The player gets bot messages only after pressing **/start** in the bot once; offer an "Enable notifications" button that opens the bot chat.

## 10. Checklist of app changes (ToR A.15)
1. Adsgram SDK before fishing start/collect, failure handling, locked Collect with hint.
2. Gift collection page (menu). 3. Tournament banner on Games + leaderboard/join. 4. Real top-up (Telegram invoice + TON Connect).
5. Claim / Claim ×2 and combo extra attempt, best-attempt flow. 6. Bonus-ad card on Tasks; free Ride (ad) when stars run out.
7. Blocked-account screen and "under review" notice. 8. Card requirements + currency icons; level-up popup with reward.
9. Chest round resume; music next/previous (`GET /music` is not implemented in v4: the playlist stays bundled for now, see BUILD-REPORT).
10. Use server error `message` text. 11. Replace polling by the fishing snapshot.
