# BrunoPlay economy

Every number the game needs, chosen together. The values live in `scripts/economy.mjs`; `node scripts/economy-report.mjs` prints
what they do (saved in `ECONOMY-REPORT.txt`); `node scripts/production-seed.mjs` writes them to a server.

**Read this first.** These numbers are a reasoned starting point, not a promise of profit. Three inputs are guesses until you
have real data: what an ad pays, how many players buy stars, and how many won gifts you really send. Section 5 says how to
learn each one in the first two weeks and what to change.

## 1. The idea in five lines

1. **Ads are the base income.** Every fishing cycle is two ads (start and collect); every bonus ad, double reward, extra combo try and free Ride run is one more.
2. **Coins are worth very little on purpose.** 1,000,000 coins = about $1.25. A player earns about 5,000–7,000 coins ($0.006–$0.009) a day for free, which is a third of what their ads earn you.
3. **A payout is a small, early reward.** A first withdrawal needs level 2 and 0.05 TON of coins (50,000 coins, about $0.06), which a player who stays about a week reaches. There is no account-age rule for now (`withdrawal.min_account_days` 0); set it to 7 or more if fake accounts become a problem. One account can take out at most 400,000 coins ($0.50) a day and, because of the 48-hour wait, about $7 a month; the lifetime limit is 100 TON (about $124). Small payouts are sent by the server by itself.
4. **Stars are the second income.** Levels 6–10 of both cards cost stars (about 1,185 stars, roughly $17 of income, to reach the top), plus the two boosters, the spin, the chest and Ride.
5. **The prize games can never lose more than they take.** Each spin tier gives back about 70–80% on average, and a hard guard stops any tier from paying out more than 85% of what it took in over 72 hours.

## 2. The rates (they follow the price of TON)

The price of TON is the one number that moves. When the seed runs it reads the live price and sets:

| Setting | Value at TON = $1.24 | Rule |
|---|---|---|
| `withdrawal.coins_per_ton` | **990,000** | TON price ÷ $0.00000125, rounded to 10,000 |
| `topup.ton.stars_per_ton` | **71** | TON price ÷ $0.0175 per star |
| `withdrawal.fee_ton` | **0.02** | well above the network fee of a TON transfer (check yours; it is usually under 0.01) |
| `topup.ton.bonus_percent` | **10** | the "+10%" on the TON tab |
| `topup.telegram.ratio` / bonus | **1** / **0** | 1 Telegram Star = 1 game star |

Why a star costs $0.0175 with TON and about $0.0199 to buy with Telegram Stars: you net about $0.013 per Telegram Star (the figure reported for Fragment payouts, paid after a 21-day hold; check the live rate on Fragment) and keep about $0.0159 per star on the TON path after the 10% bonus. The TON path is better for you and for them, so it gets the bonus.

**Re-run the seed when TON moves more than about 20%.** If TON doubles and you do not, every coin you owe costs you twice as much in dollars. Withdrawals already requested keep the rate they were made at.

## 3. All the values and why

### Fishing
| Level | Tank (coins) | Fills in | Mining upgrade | Capacity upgrade |
|---|---|---|---|---|
| 1 | 200 | 2 h | – | – |
| 2 | 300 | 2 h | 800 coins | 600 coins |
| 3 | 450 | 2 h | 2,000 coins | 1,500 coins |
| 4 | 650 | 1.75 h | 4,500 coins | 3,500 coins |
| 5 | 900 | 1.5 h | 9,000 coins | 7,000 coins |
| 6 | 1,150 | 1.5 h | 40 stars | 30 stars |
| 7 | 1,400 | 1.25 h | 70 stars | 55 stars |
| 8 | 1,650 | 1.25 h | 110 stars | 90 stars |
| 9 | 1,850 | 1 h | 170 stars | 140 stars |
| 10 | 2,000 | 1 h | 260 stars | 220 stars |

* **Small tanks that fill fast, on purpose.** A tank that fills every 1 to 2 hours gives a player a reason to open the game many times a day (the "tank is full" message brings them back), and every collect is two ads. The first version had tanks of 400 to 10,000 coins filling in 3 to 5 hours: players opened the game three times a day and the big top tanks paid out more than the ads they brought. The numbers now keep the same earning power for a player who collects on time (about 2,000 coins a day at level 1, 14,000 at level 5) but pay it in many small collects.
* **Why the tank stops at 2,000 coins.** One collect shows two ads, worth about $0.004 in a normal ad market ($0.0014 in a bad one), and 1,000 coins are worth $0.00125. A tank above about 2,000 coins pays more than its own ads in a bad market, so the top is capped there. Growth in the top levels is in speed (fills in 1 hour) more than in size.
* Levels 2–5 are paid in coins so a new player feels progress on day one. From level 6 the price is in stars, which is where most of your star income comes from.
* The second card must be nearly level with the first (`other_card_level`), so nobody races one card to the top and wrecks the pacing. Mining levels 5, 7 and 10 and capacity level 8 also need invited friends (2, 3, 5 and 3): that is your free growth.
* Simulation (per 1,000 new players, normal ad market, minimum withdrawal 20,000 coins): with the same number of opens as before the project already earns about 7% more than with the old tanks (each collect pays less); at 1.6 times as many opens about 17% more, at twice as many about 23% more. A regular player's first payout stays on day 4 to 5 as long as they open the game about twice as often as before, and comes about a day later if they do not.
* `fishing.collect_min_percent` 10 (collect once the tank is 10% full).

### Boosters (30 days)
* **Autocollect: 150 stars** (about $2). It removes the collect-and-restart ads, so the price has to pay for those lost ads plus the extra coins the player mines while away (about $0.85 for an average level-5 player, up to $3 at level 10).
* **1.5× mining: 70 stars** (about $1).

### Level-up rewards
Coins every level (500 → 30,000), stars only from level 5 (3, 5, 8, 10, 15, 25). Free stars replace stars you could have sold, so they stay small.

### Daily
* **Streak** (days 1–10): 200, 250, 300, 400, 500, 600, 750, 900, 1,100, 1,500 coins. Day 10 is 7× day 1, which is what makes people come back. A missed day starts again from day 1.
* **Combo**: 300 coins per correct picture (at most 1,200). Two extra tries a day, each one an ad. The game tells you which slots are right, so with three tries a careful player solves it. Each extra try is worth more to you (one ad) than it pays out.

### Ads
* **Bonus ad: 250 coins, 12 a day.** 250 coins is $0.0003 against about $0.002 earned by the ad: an 85% margin on the most profitable placement you have.
* Extra combo tries: 2 a day. Free Ride runs: 3 a day.
* `ads.required` **true**. Never switch it off in production. `view_expiry_seconds` 300, `max_failed_loads` 3, `fallback_minutes` 10 (if the ad network fails three times in a row, play continues for ten minutes instead of locking people out).

### Friends
* Inviter gets 1,500 / 2,500 / 4,000 / 6,000 / 9,000 / 13,000 / 18,000 / 25,000 / 35,000 coins as a friend reaches levels 2–10, plus 5% of that friend's collected coins, up to 30,000 coins per friend.
* The friend gets 1,000 coins on reaching level 2. A friend only counts at level 2, so a fake account has to do real work first.

### Games
* **Gifts** (value in stars; this is also what selling pays): Apple 15, Cat 25, Panda 50, Watch 100, Ring 250, Cap 1,000, Heart 2,500, Frog 10,000.
* **Spin tiers**: 30 / 75 / 150 / 400 / 1,000 stars per spin, giving back about 70% / 72% / 75% / 78% / 80%. Every spin wins a gift (the cheapest is worth half the cheapest tier), the higher tiers tilt toward the valuable ones. The odds are computed, not guessed (`economy.mjs`, `tuneSpin`).
* **Chest**: 20 stars for a round of 3 picks, giving back about 74%. Prizes: coins (3,000 / 10,000 / 30,000), stars (3 / 8 / 25), small gifts (Apple, Cat, Panda).
* **Ride**: 2 stars a run, up to 4,000 coins a day. Three runs a day are free for an ad.
* **Payout guard**: 85% over 72 hours, with a coin counted as 0.0001 star. With low volume the guard simply holds back the biggest prizes, because a tier cannot pay out what it has not yet taken in. So the Heart and Frog appear only when many people are playing that tier: this is what keeps them from bankrupting you early. They stay on the wheel for show.

* **The wheel pays stars and coins, not Telegram gifts (the current setting).** Every gift became the same number of stars: Apple 15, Cat 25, Panda 50, Watch 100, Ring 250, Cap 1,000, Heart 2,500, Frog 10,000. The three cheapest (the ones that come up most) are also paid as coins of equal worth, 150,000, 250,000 and 500,000, at the payout guard's own rate of 10,000 coins to the star (what a gift costs you is about $0.013 a star, and 10,000 coins are $0.0125). Each of those is split 60% stars and 40% coins. The return of every tier is unchanged (70.3 / 72.1 / 75.0 / 78.0 / 80.0%). Stars cost you less than the same value in coins, because stars cannot be withdrawn and coins can: keep that in mind before making the coin share bigger. `scripts/economy.mjs` builds these odds; set `WHEEL_PAYS_GIFTS = true` there to bring the gifts back, and run `production-seed.mjs --only spin.odds` to write them without touching anything else. The winners strip lists any prize worth at least `spin.winners_min_value` stars (250).
* **Coins and stars on the wheel.** Besides gifts, a tier can pay coins or stars. In Settings, games, `spin.odds`, add entries named `coins:AMOUNT` or `stars:AMOUNT` next to the gift keys, for example `{"TIER_1": {"apple": 4000, "cat": 2500, "coins:500": 3000, "coins:2000": 600, "stars:5": 800}}`. The numbers are relative weights, not percentages. Coins and stars are paid straight to the balance (no gift is created); the prize list and the wheel show them, and the arrow stops on them. The wheel has 8 places: with coin or star prizes among the gifts, at least five go to gifts and the others are shared out, big to small, alternating around the wheel.
* **Check the return after you edit the odds.** Run `ADMIN_PASSWORD='...' node scripts/wheel-rtp.mjs --api https://YOUR-SITE --email YOUR-ADMIN-EMAIL`. It prints, for every tier, what the wheel gives back out of each 100 stars spent (aim for 70 to 80) and how much of it comes from gifts, coins and stars. Stars are worth real money to the player (about $0.0175 each) and can buy upgrades, so give them out carefully and keep the weights of big star prizes small. A coin counts as 0.0001 star in the payout guard, so 500 coins is 0.05 star.
* `spin.publish_odds` false, `spin.winners_min_value` 250 (only wins worth at least 250 stars appear in the live feed).

### Top-up
Packs: 100, 250, 500, 1,000, 2,500, 5,000, 10,000, 25,000 stars. A small first pack lowers the barrier; the big ones are for the few who pay most of the money.

### Withdrawals
| Setting | Value |
|---|---|
| minimum per request | 50,000 coins = **0.05 TON** (the seed keeps it at 0.05 TON when the TON price changes) |
| maximum per request / per day | 400,000 coins (about $0.50) |
| most one account can ever take | **100 TON** = 99,000,000 coins (about $124) |
| time between requests | 48 hours |
| minimum level / account age | **2** / **0 days** (no age rule) |
| automatic payout | **on up to 0.075 TON** the player receives (after the fee), at most 10 TON a day in total; bigger requests wait for you. Needs `AUTO_PAYOUT=true` and the payout wallet (see `DEPLOY-VPS.md`) |

### Everything else
`risk.hold_threshold` 30 (three implausible Ride runs put an account on hold for review), `tasks.link_min_seconds` 5, retention 90 / 30 days, at most 5 bot messages a day, quiet hours 20:00–04:00 UTC, default message hour 09:00 UTC.

### Starter tasks (the seed creates them if there are none)
Join the channel (2,000 coins) and chat (2,000, both only when you give their names), invite 3 / 10 / 25 friends (3,000 / 10,000 + 5 stars / 30,000 + 15 stars), watch 5 ads today (500 coins, daily).

## 4. What the numbers say

Per 1,000 people who install the game, over 90 days (`node scripts/economy-report.mjs`):

| Case | An ad pays | Stars bought | Gifts really sent | TON price after the rate is set | Income | Cost | **Profit** |
|---|---|---|---|---|---|---|---|
| **Best** | $0.006 | 1.5× | 10% | −30% | $3,131 | $296 | **$2,834** |
| **Base** | $0.002 | 1× | 25% | same | $1,547 | $420 | **$1,126** |
| **Worst** | $0.0007 | 0.4× | 50% | +60% | $663 | $491 | **$172** |
| Nobody ever buys (base ads) | $0.002 | 0 | – | same | $630 | $234 | **$396** |
| **Nobody buys and ads are bad** | $0.0007 | 0 | – | +60% | $211 | $374 | **−$163** |

"Worst" still includes 6% farm accounts that earn you nothing. The last row is the real danger: **if ads pay very little and no one buys stars, the game loses a little.** You can see that coming within two weeks (section 5) and fix it with one setting, which is why payouts are manual at the start.

Who pays whom (base case, per player):

| Player | Ads you earn | Stars they buy | You pay them | Gifts you send |
|---|---|---|---|---|
| Leaves in 2 days (49%) | $0.03 | – | – | – |
| Casual, 10 days (26%) | $0.24 | – | $0.07 | – |
| Regular, 45 days (13%) | $1.64 | $0.21 | $0.56 | $0.05 |
| Grinder, 90 days (7%) | $4.27 | $0.85 | $1.48 | $0.21 |
| Spender (2%) | $1.82 | $25.49 | $1.62 | $4.17 |
| Whale (0.2%) | $1.83 | $169.96 | $2.79 | $42.89 |

The first payout is possible from about day 6, as soon as a player has reached level 2 and 50,000 coins. Even the most dedicated free player earns you about three times what they cost. A farm account now cashes out about $0.22 on average, where before it got nothing: the low minimum is paid for with a little more exposure to fake accounts.

## 5. Check these three things in the first two weeks

| Unknown | Where to see it | If it is worse than assumed |
|---|---|---|
| **What an ad pays** (assumed $0.002) | Adsgram dashboard: earnings ÷ completed views | Under $0.001: lower `ads.bonus.coins` to 150 and raise `withdrawal.coins_per_ton` by 30% (a coin becomes worth less). Over $0.004: you can be more generous (see below) |
| **Do players buy** (assumed 2% spend about $25) | Admin → Top-ups | Under 0.5% buying: lower the star prices of card levels 6–8, set `topup.telegram.bonus_percent` to 10 for a while, and promote the TON tab |
| **How many won gifts are sent** (assumed 25%) | Admin → Gift claims | Above 40%: lower the values of Cap, Heart, Frog (`gifts`), or lower the guard to 0.75 |

Weekly: look at Admin → Dashboard (payout ratio of the games, withdrawals, ledger check) and Withdrawals. A healthy first month has payouts under a third of ad income.

## 6. Levers

| I want to… | Change |
|---|---|
| pay players less, quickly | `withdrawal.coins_per_ton` up (1,200,000 makes a coin worth 20% less) |
| pay players more / be generous | `withdrawal.coins_per_ton` down, or `ads.bonus.coins` up |
| stop a drain right now | `withdrawal.daily_limit_coins` down, or put `maintenance.enabled` on |
| earn more from stars | lower star prices of levels 6–8, add TON bonus % |
| make the games less generous | lower `guard.max_payout_ratio` (0.75) or the gift values |
| TON price moved | re-run the seed: it recomputes the two rates (existing requests keep theirs) |

## 7. Risks you should know about

* **The assumptions are unproven** (section 5). The model uses average players, not a crowd; real results will be uneven.
* **Spin and chest are paid games of chance.** In many countries (including some where Telegram is popular) that is regulated like gambling, and Telegram's own rules restrict it. Get local legal advice before you promote them, and think about keeping them off for some regions.
* **Real gifts cost real money.** The star value of a gift must match what you can really buy and send it for. Do not leave Heart (2,500) and Frog (10,000) at these values if you cannot deliver them; change the numbers in the `gifts` setting, which is your call.
* **TON volatility.** Payouts are in TON; ad income and Telegram Stars are in dollars. A sharp rise in TON costs you, so watch it.
* **Farm accounts.** The limits (level, age, minimum, daily and total caps) make one fake account cost you at most about $7 a month (400,000 coins every 48 hours) and usually far less, because it can only withdraw what it earns; but a large farm is still real money, and with the lifetime limit now at 100 TON the total cap no longer protects you, only the daily limit, the cooldown and the minimum level do. The risk score and hold exist for this; review the Risk page weekly.
* The simulator is an expectation, not a forecast. It cannot see viral growth, seasonality or a change of Adsgram's rates.
