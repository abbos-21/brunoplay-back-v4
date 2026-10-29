# Settings reference (generated)

Every value the owner supplies or changes in the admin panel (`PUT /api/admin/settings/{key}`). **Required** means there is no default: the feature that needs it answers `CONFIG_MISSING` until it is set.


## ads

| Key | Type | Default | What it is |
|---|---|---|---|
| `ads.block_ids` | json | **required** | Adsgram block id per placement |
| `ads.bonus.coins` | int | **required** | Coins for one bonus ad |
| `ads.bonus.max_per_day` | int | **required** | Bonus ads per player per day |
| `ads.combo_extra.max_per_day` | int | **required** | Extra combo attempts (ad) per player per day |
| `ads.fallback_minutes` | int | `10` | Minutes the no-ad window lasts after a fallback |
| `ads.interstitial.block_id` | string | `` | Adsgram block id of the Interstitial block (digits) |
| `ads.interstitial.enabled` | bool | `false` | Interstitial ads (full screen, the player can close them) between games |
| `ads.interstitial.first_after_seconds` | int | `120` | Interstitial: no ad in the first seconds after the app is opened |
| `ads.interstitial.interval_seconds` | int | `180` | Interstitial: minimum seconds between two ads for one player |
| `ads.max_failed_loads` | int | `3` | Failed ad loads in a row after which the action is allowed without an ad |
| `ads.monetag.task_coins` | int | `1000` | Coins for one completed Monetag popup task |
| `ads.monetag.task_enabled` | bool | `false` | Monetag popup task (Rewarded Popup: opens a partner offer) on the Tasks screen |
| `ads.monetag.task_interval_minutes` | int | `480` | Monetag popup task: minutes between two paid tasks of one player (480 = 3 a day) |
| `ads.monetag.wait_seconds` | int | `10` | Monetag: seconds after Monetag reports the ad before the action may go ahead (it reports when the ad starts, so set the ad's length) |
| `ads.monetag.zone_id` | string | `` | Monetag main zone id (a number from the Monetag dashboard) |
| `ads.placements_enabled` | json | `map[BONUS_COINS:true COLLECT:true COMBO_EXTRA:true DOUBLE_COMBO:true DOUBLE_STREAK:true FISHING_START:true RIDE_FREE:true TASK_ADS:true]` | Placement switches |
| `ads.providers` | json | `[map[enabled:true id:adsgram] map[enabled:false id:tads] map[enabled:false id:monetag]]` | Ad providers: the order is the priority, each can be switched on or off (managed on the Ads page) |
| `ads.required` | bool | `true` | Ads are required for fishing start and collect (keep ON in production) |
| `ads.ride_free.max_per_day` | int | **required** | Free Ride runs (ad) per player per day |
| `ads.tads.debug` | bool | `false` | TADS debug mode (test ads; keep OFF in production) |
| `ads.tads.tgb_widget_id` | string | `` | TADS TGB widget id for the test card on the Tasks screen (empty = no card) |
| `ads.tads.wait_seconds` | int | `15` | TADS: seconds after TADS reports the ad before the action may go ahead (TADS reports when the ad starts, so set the ad's length) |
| `ads.tads.widget_id` | string | `` | TADS widget id (a number from the TADS dashboard) |
| `ads.task.block_id` | string | `` | Adsgram block id of the Task block (task-123) |
| `ads.task.enabled` | bool | `false` | Sponsored task (Adsgram Task block) on the Tasks screen |
| `ads.task.interval_minutes` | int | `480` | Sponsored task: minutes between two paid tasks of one player (480 = 3 a day) |
| `ads.task.reward_coins` | int | `2000` | Coins for one completed sponsored task |
| `ads.view_expiry_seconds` | int | `300` | Seconds an ad proof stays valid |

## boosters

| Key | Type | Default | What it is |
|---|---|---|---|
| `boosters.autocollect.price_stars` | int | **required** | Autocollect price (stars) |
| `boosters.duration_days` | int | `30` | Booster duration in days |
| `boosters.mining.multiplier` | float | `1.5` | Mining speed multiplier of the speed booster |
| `boosters.mining.price_stars` | int | **required** | 1.5x booster price (stars) |

## daily

| Key | Type | Default | What it is |
|---|---|---|---|
| `combo.coins_per_correct` | int | **required** | Coins per correct combo picture |
| `streak.rewards` | json | **required** | Streak reward coins for days 1..10 |

## fishing

| Key | Type | Default | What it is |
|---|---|---|---|
| `cards.capacity.levels` | json | **required** | Coin-capacity card: [{level,value,cost,currency:COIN/STAR,requirements:[{type,value,desc_en,desc_ru}]}] for levels 1..10 |
| `cards.mining.levels` | json | **required** | Mining-rate card: [{level,value,cost,currency:COIN/STAR,requirements:[{type,value,desc_en,desc_ru}]}] for levels 1..10 |
| `fishing.collect_min_percent` | int | `10` | Minimum tank fill (%) needed to collect |

## friends

| Key | Type | Default | What it is |
|---|---|---|---|
| `referral.default_join_bonus` | int | `0` | Default friend join bonus (coins), paid at friend level 2 |
| `referral.level_rewards` | json | **required** | Coins the inviter gets when a friend reaches a level: {"2":200,...} |
| `referral.mining_cap_per_friend` | int | `0` | Most coins an inviter can earn from one friend's mining |
| `referral.mining_percent` | float | `0` | Percent of a friend's collected coins paid to the inviter |
| `referral.topup_percent` | float | `10` | Percent of a friend's top-up (Telegram Stars or TON; in stars) paid to the inviter, rounded down; paid on every top-up, no level needed (0 = off) |

## games

| Key | Type | Default | What it is |
|---|---|---|---|
| `chest.picks_per_round` | int | `3` | Chests per round |
| `chest.pool` | json | **required** | Chest prize pool [{type:coins/stars/gift,value,weight,gift}] |
| `chest.round_cost_stars` | int | **required** | Chest round price (stars) |
| `chest.round_expiry_hours` | int | `0` | Hours an unfinished chest round stays open (0 = forever) |
| `gifts` | json | **required** | Gifts [{key,name_en,name_ru,star_value}] |
| `guard.coin_value_stars` | float | `0` | Payout guard: how many stars one coin is worth when counting coin prizes (0 = coins are not counted) |
| `guard.max_payout_ratio` | float | `0` | Payout guard: maximum payout ratio, e.g. 0.95 (0 = guard off) |
| `guard.window_hours` | int | `24` | Payout guard window (hours) |
| `ride.max_coins_per_day` | int | **required** | Coins a player can earn from Ride per day |
| `ride.max_coins_per_second` | float | `8` | Most coins one can pick up per second |
| `ride.max_duration_seconds` | int | `900` | A run can be saved for this long after it starts (seconds) |
| `ride.max_points_per_second` | float | `70` | Most score points per second (the game scores 60 per second) |
| `ride.min_duration_seconds` | int | `3` | Shortest believable Ride run (seconds); the game itself cannot end a run before about 4.5 s, so keep this below that |
| `ride.run_cost_stars` | int | **required** | Ride run price (stars) |
| `ride.spawn_delay_seconds` | int | `3` | Seconds before obstacles and coins appear |
| `spin.odds` | json | **required** | Wheel odds {tier:{prize:weight}}. A prize is a gift key (heart), or coins:500, or stars:25. Weights are relative. |
| `spin.publish_odds` | bool | `false` | Show the relative odds of the gifts to players (false = every gift shown with equal weight) |
| `spin.tiers` | json | **required** | Spin tiers [{tier,price_stars}] |
| `spin.winners_min_value` | int | `0` | Smallest prize worth, in stars, shown in the winners feed (0 = every prize) |

## levels

| Key | Type | Default | What it is |
|---|---|---|---|
| `levels.rewards` | json | `[]` | Level-up rewards [{level,coins,stars}] for levels 2..10 |

## links

| Key | Type | Default | What it is |
|---|---|---|---|
| `links.faq` | string | `` | FAQ link |
| `links.news` | string | `` | News / community link |
| `links.support` | string | `` | Support link |

## notifications

| Key | Type | Default | What it is |
|---|---|---|---|
| `notifications.booster_expiry_days` | json | `[3 1]` | Days before a booster ends to warn |
| `notifications.card_upgrade_ready` | bool | `false` | Tell players when they can afford the next card level |
| `notifications.chest_round_hours` | int | `6` | Hours before reminding about an unfinished chest round |
| `notifications.collect_reminder_hours` | int | `6` | Hours after the tank is full before the collect reminder |
| `notifications.daily_cap` | int | `6` | Most bot messages per player per day (except payments) |
| `notifications.default_hour_utc` | int | `9` | Default UTC hour for 'ready' messages |
| `notifications.idle_hours` | int | `12` | Hours without fishing before the 'start fishing' message |
| `notifications.quiet_hours` | json | `map[from:-1 to:-1]` | Quiet hours in UTC {from,to} (null = none) |
| `notifications.warn_hours` | json | `[6 1]` | Hours before the daily reset to warn |

## system

| Key | Type | Default | What it is |
|---|---|---|---|
| `app.min_version` | string | `` | Minimum app version; older apps are told to reload |
| `maintenance.enabled` | bool | `false` | Show the 'back soon' screen to all players |
| `maintenance.message_en` | string | `` | Maintenance text (English) |
| `maintenance.message_ru` | string | `` | Maintenance text (Russian) |
| `maintenance.until` | string | `` | Expected end of maintenance (RFC3339, optional) |
| `retention.ad_views_days` | int | `30` | Days to keep old ad views |
| `retention.logs_days` | int | `90` | Days to keep technical logs |
| `risk.hold_threshold` | int | `0` | Risk score that triggers an automatic hold (0 = off) |

## tasks

| Key | Type | Default | What it is |
|---|---|---|---|
| `tasks.link_min_seconds` | int | `5` | Seconds before a link task can complete |

## topup

| Key | Type | Default | What it is |
|---|---|---|---|
| `topup.packs` | json | **required** | Star packs [{stars}] shown in the top-up panel |
| `topup.telegram.bonus_percent` | float | `0` | Bonus % on Telegram Stars payments |
| `topup.telegram.ratio` | float | `1` | Game stars per 1 Telegram Star |
| `topup.ton.bonus_percent` | float | `0` | Bonus % on TON payments (shown on the TON tab) |
| `topup.ton.confirmations` | int | `10` | Seconds a TON transfer must be old before it is credited (extra safety) |
| `topup.ton.order_expiry_minutes` | int | `30` | Minutes a TON payment order stays open |
| `topup.ton.stars_per_ton` | float | **required** | Game stars per 1 TON |
| `topup.ton.wallet_address` | string | **required** | Project TON wallet that receives top-ups |

## withdrawal

| Key | Type | Default | What it is |
|---|---|---|---|
| `withdrawal.auto_payout_daily_cap_ton` | float | `0` | Daily cap of automatic payouts in TON |
| `withdrawal.auto_payout_max_ton` | float | `0` | Requests up to this TON amount are paid automatically (0 = all manual) |
| `withdrawal.coins_per_ton` | float | **required** | Coins per 1 TON |
| `withdrawal.cooldown_hours` | int | **required** | Hours between two requests of one player |
| `withdrawal.daily_limit_coins` | int | **required** | Coins a player may withdraw per day |
| `withdrawal.fee_ton` | float | **required** | Fixed withdrawal fee in TON |
| `withdrawal.max_coins` | int | **required** | Maximum coins per request |
| `withdrawal.min_account_days` | int | **required** | Minimum account age in days |
| `withdrawal.min_coins` | int | **required** | Minimum coins per request |
| `withdrawal.min_level` | int | **required** | Minimum player level to withdraw |
| `withdrawal.notice_en` | string | `` | Notice text on the history screen (English) |
| `withdrawal.notice_ru` | string | `` | Notice text on the history screen (Russian) |
| `withdrawal.total_limit_coins` | int | **required** | Coins a player may withdraw in total |
