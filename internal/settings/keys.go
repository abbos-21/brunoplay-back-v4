package settings

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
)

// Setting keys. A key with Default == nil is a value the owner must supply (ToR section 11).
const (
	// system
	KeyMaintenanceOn  = "maintenance.enabled"
	KeyMaintenanceEnd = "maintenance.until"
	KeyMaintenanceEN  = "maintenance.message_en"
	KeyMaintenanceRU  = "maintenance.message_ru"
	KeyMinVersion     = "app.min_version"
	KeyLinkSupport    = "links.support"
	KeyLinkNews       = "links.news"
	KeyLinkFAQ        = "links.faq"
	KeyRetentionLogs  = "retention.logs_days"
	KeyRetentionAds   = "retention.ad_views_days"
	KeyRiskHold       = "risk.hold_threshold"

	// ads
	KeyAdsRequired    = "ads.required"
	KeyAdsViewExpiry  = "ads.view_expiry_seconds"
	KeyAdsBlockIDs    = "ads.block_ids"
	KeyAdsPlacements  = "ads.placements_enabled"
	KeyAdsMaxFailed   = "ads.max_failed_loads"
	KeyAdsFallbackMin = "ads.fallback_minutes"
	KeyAdsBonusCoins  = "ads.bonus.coins"
	KeyAdsBonusPerDay = "ads.bonus.max_per_day"
	KeyAdsComboExtra  = "ads.combo_extra.max_per_day"
	KeyAdsRideFree    = "ads.ride_free.max_per_day"

	KeyAdsInterEnabled        = "ads.interstitial.enabled"
	KeyAdsInterBlock          = "ads.interstitial.block_id"
	KeyAdsInterGap            = "ads.interstitial.interval_seconds"
	KeyAdsInterFirst          = "ads.interstitial.first_after_seconds"
	KeyAdsTaskEnabled         = "ads.task.enabled"
	KeyAdsTaskBlock           = "ads.task.block_id"
	KeyAdsTaskCoins           = "ads.task.reward_coins"
	KeyAdsTaskInterval        = "ads.task.interval_minutes"
	KeyAdsProviders           = "ads.providers"
	KeyAdsTadsWidget          = "ads.tads.widget_id"
	KeyAdsTadsDebug           = "ads.tads.debug"
	KeyAdsTadsWait            = "ads.tads.wait_seconds"
	KeyAdsTadsTGB             = "ads.tads.tgb_widget_id"
	KeyAdsMonetagZone         = "ads.monetag.zone_id"
	KeyAdsMonetagWait         = "ads.monetag.wait_seconds"
	KeyAdsMonetagTask         = "ads.monetag.task_enabled"
	KeyAdsMonetagTaskCoins    = "ads.monetag.task_coins"
	KeyAdsMonetagTaskInterval = "ads.monetag.task_interval_minutes"

	// fishing, cards, boosters, levels
	KeyCollectMinPct   = "fishing.collect_min_percent"
	KeyCardsMining     = "cards.mining.levels"
	KeyCardsCapacity   = "cards.capacity.levels"
	KeyBoosterDays     = "boosters.duration_days"
	KeyBoosterMulti    = "boosters.mining.multiplier"
	KeyBoosterAutoCost = "boosters.autocollect.price_stars"
	KeyBoosterMineCost = "boosters.mining.price_stars"
	KeyLevelRewards    = "levels.rewards"

	// daily
	KeyStreakRewards   = "streak.rewards"
	KeyComboPerCorrect = "combo.coins_per_correct"

	// stars top-up
	KeyTopupPacks       = "topup.packs"
	KeyTopupTGRatio     = "topup.telegram.ratio"
	KeyTopupTGBonus     = "topup.telegram.bonus_percent"
	KeyTopupTonBonus    = "topup.ton.bonus_percent"
	KeyTopupStarsPerTon = "topup.ton.stars_per_ton"
	KeyTopupTonWallet   = "topup.ton.wallet_address"
	KeyTopupTonConf     = "topup.ton.confirmations"
	KeyTopupTonExpiry   = "topup.ton.order_expiry_minutes"

	// withdrawals
	KeyWdCoinsPerTon   = "withdrawal.coins_per_ton"
	KeyWdFeeTon        = "withdrawal.fee_ton"
	KeyWdMin           = "withdrawal.min_coins"
	KeyWdMax           = "withdrawal.max_coins"
	KeyWdCooldownHours = "withdrawal.cooldown_hours"
	KeyWdMinLevel      = "withdrawal.min_level"
	KeyWdMinAgeDays    = "withdrawal.min_account_days"
	KeyWdDailyLimit    = "withdrawal.daily_limit_coins"
	KeyWdTotalLimit    = "withdrawal.total_limit_coins"
	KeyWdAutoMaxTon    = "withdrawal.auto_payout_max_ton"
	KeyWdAutoDailyCap  = "withdrawal.auto_payout_daily_cap_ton"
	KeyWdNoticeEN      = "withdrawal.notice_en"
	KeyWdNoticeRU      = "withdrawal.notice_ru"

	// games
	KeySpinTiers       = "spin.tiers"
	KeySpinOdds        = "spin.odds"
	KeyGifts           = "gifts"
	KeyChestCost       = "chest.round_cost_stars"
	KeyChestPicks      = "chest.picks_per_round"
	KeyChestPool       = "chest.pool"
	KeyChestExpiryH    = "chest.round_expiry_hours"
	KeyRideCost        = "ride.run_cost_stars"
	KeyRideMinSeconds  = "ride.min_duration_seconds"
	KeyRideMaxSeconds  = "ride.max_duration_seconds"
	KeyRideSpawnDelay  = "ride.spawn_delay_seconds"
	KeyRideMaxCoinsSec = "ride.max_coins_per_second"
	KeyRideMaxPtsSec   = "ride.max_points_per_second"
	KeyRideDailyCoins  = "ride.max_coins_per_day"
	KeyGuardRatio      = "guard.max_payout_ratio"
	KeyGuardWindowH    = "guard.window_hours"
	KeyWinnersMin      = "spin.winners_min_value"
	KeySpinPublishOdds = "spin.publish_odds"
	KeyGuardCoinValue  = "guard.coin_value_stars"

	// friends and tasks
	KeyRefLevelRewards = "referral.level_rewards"
	KeyRefMiningPct    = "referral.mining_percent"
	KeyRefMiningCap    = "referral.mining_cap_per_friend"
	KeyRefJoinBonus    = "referral.default_join_bonus"
	KeyTaskLinkSeconds = "tasks.link_min_seconds"

	// notifications
	KeyNotifDailyCap  = "notifications.daily_cap"
	KeyNotifDefaultHr = "notifications.default_hour_utc"
	KeyNotifWarnHours = "notifications.warn_hours"
	KeyNotifQuiet     = "notifications.quiet_hours"
	KeyNotifCollectH  = "notifications.collect_reminder_hours"
	KeyNotifIdleH     = "notifications.idle_hours"
	KeyNotifBoosterD  = "notifications.booster_expiry_days"
	KeyNotifUpgrade   = "notifications.card_upgrade_ready"
	KeyNotifChestH    = "notifications.chest_round_hours"
)

func f(v float64) *float64 { return &v }

func jsonArray(raw json.RawMessage) error {
	var a []json.RawMessage
	if err := json.Unmarshal(raw, &a); err != nil {
		return errors.New("must be a list")
	}
	return nil
}
func jsonObject(raw json.RawMessage) error {
	var a map[string]json.RawMessage
	if err := json.Unmarshal(raw, &a); err != nil {
		return errors.New("must be an object")
	}
	return nil
}

// spinOddsCheck validates the wheel odds: {tier:{prize:weight}}. A prize is a gift key ("heart"), or an amount of coins
// ("coins:500") or of stars ("stars:25"). The weights are relative: only their proportions matter.
func spinOddsCheck(raw json.RawMessage) error {
	var odds map[string]map[string]float64
	if err := json.Unmarshal(raw, &odds); err != nil {
		return errors.New(`must look like {"TIER_1": {"apple": 40, "coins:500": 30, "stars:5": 10}}`)
	}
	for tier, row := range odds {
		positive := false
		for key, w := range row {
			if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 {
				return errors.New("tier " + tier + ": the weight of " + key + " must be a number from 0 up")
			}
			if w > 0 {
				positive = true
			}
			if strings.TrimSpace(key) == "" {
				return errors.New("tier " + tier + ": a prize needs a name")
			}
			if kind, amount, ok := strings.Cut(key, ":"); ok {
				n, err := strconv.ParseInt(amount, 10, 64)
				if (kind != "coins" && kind != "stars") || err != nil || n <= 0 || n > 1_000_000_000 {
					return errors.New("tier " + tier + `: "` + key + `" must be coins:AMOUNT or stars:AMOUNT with a whole number above 0, or a gift key`)
				}
			}
		}
		if !positive {
			return errors.New("tier " + tier + " needs at least one prize with a weight above 0")
		}
	}
	return nil
}

// cardsCheck validates a card level table: levels 1..N in order (N <= 10), positive values, known currencies and requirement types.
func cardsCheck(raw json.RawMessage) error {
	var levels []struct {
		Level        int     `json:"level"`
		Value        float64 `json:"value"`
		Cost         int64   `json:"cost"`
		Currency     string  `json:"currency"`
		Requirements []struct {
			Type  string `json:"type"`
			Value int64  `json:"value"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal(raw, &levels); err != nil {
		return errors.New("must be a list of levels")
	}
	if len(levels) < 1 || len(levels) > 10 {
		return errors.New("needs 1 to 10 levels")
	}
	prev := 0.0
	for i, l := range levels {
		if l.Level != i+1 {
			return errors.New("levels must be numbered 1, 2, 3 ... in order")
		}
		if l.Value <= 0 || l.Value <= prev {
			return errors.New("values must be positive and growing")
		}
		prev = l.Value
		if i > 0 && l.Cost <= 0 {
			return errors.New("every level after the first needs a cost")
		}
		if l.Currency != "COIN" && l.Currency != "STAR" {
			return errors.New("currency must be COIN or STAR")
		}
		for _, r := range l.Requirements {
			switch r.Type {
			case "invite_friends", "player_level", "other_card_level", "task_done":
			default:
				return errors.New("unknown requirement type " + r.Type)
			}
			if r.Value <= 0 {
				return errors.New("requirement value must be positive")
			}
		}
	}
	return nil
}

// Registry lists every setting with its safe default (nil default = must be configured).
func Registry() []Def {
	i := func(key, group, desc string, def any, min, max float64) Def {
		return Def{Key: key, Kind: KInt, Group: group, Desc: desc, Default: def, Min: f(min), Max: f(max)}
	}
	fl := func(key, group, desc string, def any, min, max float64) Def {
		return Def{Key: key, Kind: KFloat, Group: group, Desc: desc, Default: def, Min: f(min), Max: f(max)}
	}
	b := func(key, group, desc string, def any) Def {
		return Def{Key: key, Kind: KBool, Group: group, Desc: desc, Default: def}
	}
	s := func(key, group, desc string, def any) Def {
		return Def{Key: key, Kind: KString, Group: group, Desc: desc, Default: def}
	}
	arr := func(key, group, desc string, def any) Def {
		return Def{Key: key, Kind: KJSON, Group: group, Desc: desc, Default: def, Check: jsonArray}
	}
	obj := func(key, group, desc string, def any) Def {
		return Def{Key: key, Kind: KJSON, Group: group, Desc: desc, Default: def, Check: jsonObject}
	}
	cards := func(key, group, desc string, def any) Def {
		return Def{Key: key, Kind: KJSON, Group: group, Desc: desc, Default: def, Check: cardsCheck}
	}
	const big = 1e12
	return []Def{
		b(KeyMaintenanceOn, "system", "Show the 'back soon' screen to all players", false),
		s(KeyMaintenanceEnd, "system", "Expected end of maintenance (RFC3339, optional)", ""),
		s(KeyMaintenanceEN, "system", "Maintenance text (English)", ""),
		s(KeyMaintenanceRU, "system", "Maintenance text (Russian)", ""),
		s(KeyMinVersion, "system", "Minimum app version; older apps are told to reload", ""),
		s(KeyLinkSupport, "links", "Support link", ""),
		s(KeyLinkNews, "links", "News / community link", ""),
		s(KeyLinkFAQ, "links", "FAQ link", ""),
		i(KeyRetentionLogs, "system", "Days to keep technical logs", 90, 1, 3650),
		i(KeyRetentionAds, "system", "Days to keep old ad views", 30, 1, 3650),
		i(KeyRiskHold, "system", "Risk score that triggers an automatic hold (0 = off)", 0, 0, 10000),

		b(KeyAdsRequired, "ads", "Ads are required for fishing start and collect (keep ON in production)", true),
		i(KeyAdsViewExpiry, "ads", "Seconds an ad proof stays valid", 300, 30, 3600),
		obj(KeyAdsBlockIDs, "ads", "Adsgram block id per placement", nil),
		obj(KeyAdsPlacements, "ads", "Placement switches", map[string]bool{
			"FISHING_START": true, "COLLECT": true, "BONUS_COINS": true, "DOUBLE_STREAK": true,
			"DOUBLE_COMBO": true, "COMBO_EXTRA": true, "RIDE_FREE": true, "TASK_ADS": true}),
		i(KeyAdsMaxFailed, "ads", "Failed ad loads in a row after which the action is allowed without an ad", 3, 1, 50),
		i(KeyAdsFallbackMin, "ads", "Minutes the no-ad window lasts after a fallback", 10, 1, 1440),
		i(KeyAdsBonusCoins, "ads", "Coins for one bonus ad", nil, 1, big),
		i(KeyAdsBonusPerDay, "ads", "Bonus ads per player per day", nil, 0, 1000),
		i(KeyAdsComboExtra, "ads", "Extra combo attempts (ad) per player per day", nil, 0, 100),
		i(KeyAdsRideFree, "ads", "Free Ride runs (ad) per player per day", nil, 0, 1000),
		b(KeyAdsInterEnabled, "ads", "Interstitial ads (full screen, the player can close them) between games", false),
		s(KeyAdsInterBlock, "ads", "Adsgram block id of the Interstitial block (digits)", ""),
		i(KeyAdsInterGap, "ads", "Interstitial: minimum seconds between two ads for one player", 180, 30, 86400),
		i(KeyAdsInterFirst, "ads", "Interstitial: no ad in the first seconds after the app is opened", 120, 0, 3600),
		b(KeyAdsTaskEnabled, "ads", "Sponsored task (Adsgram Task block) on the Tasks screen", false),
		s(KeyAdsTaskBlock, "ads", "Adsgram block id of the Task block (task-123)", ""),
		i(KeyAdsTaskCoins, "ads", "Coins for one completed sponsored task", 2000, 1, big),
		i(KeyAdsTaskInterval, "ads", "Sponsored task: minutes between two paid tasks of one player (480 = 3 a day)", 480, 1, 10080),
		arr(KeyAdsProviders, "ads", "Ad providers: the order is the priority, each can be switched on or off (managed on the Ads page)",
			[]map[string]any{{"id": "adsgram", "enabled": true}, {"id": "tads", "enabled": false}, {"id": "monetag", "enabled": false}}),
		s(KeyAdsTadsWidget, "ads", "TADS widget id (a number from the TADS dashboard)", ""),
		s(KeyAdsTadsTGB, "ads", "TADS TGB widget id for the test card on the Tasks screen (empty = no card)", ""),
		b(KeyAdsTadsDebug, "ads", "TADS debug mode (test ads; keep OFF in production)", false),
		s(KeyAdsMonetagZone, "ads", "Monetag main zone id (a number from the Monetag dashboard)", ""),
		i(KeyAdsMonetagWait, "ads", "Monetag: seconds after Monetag reports the ad before the action may go ahead (it reports when the ad starts, so set the ad's length)", 10, 0, 120),
		b(KeyAdsMonetagTask, "ads", "Monetag popup task (Rewarded Popup: opens a partner offer) on the Tasks screen", false),
		i(KeyAdsMonetagTaskCoins, "ads", "Coins for one completed Monetag popup task", 1000, 1, big),
		i(KeyAdsMonetagTaskInterval, "ads", "Monetag popup task: minutes between two paid tasks of one player (480 = 3 a day)", 480, 1, 10080),
		i(KeyAdsTadsWait, "ads", "TADS: seconds after TADS reports the ad before the action may go ahead (TADS reports when the ad starts, so set the ad's length)", 15, 0, 120),

		i(KeyCollectMinPct, "fishing", "Minimum tank fill (%) needed to collect", 10, 0, 100),
		cards(KeyCardsMining, "fishing", "Mining-rate card: [{level,value,cost,currency:COIN|STAR,requirements:[{type,value,desc_en,desc_ru}]}] for levels 1..10", nil),
		cards(KeyCardsCapacity, "fishing", "Coin-capacity card: [{level,value,cost,currency:COIN|STAR,requirements:[{type,value,desc_en,desc_ru}]}] for levels 1..10", nil),
		i(KeyBoosterDays, "boosters", "Booster duration in days", 30, 1, 3650),
		fl(KeyBoosterMulti, "boosters", "Mining speed multiplier of the speed booster", 1.5, 1, 100),
		i(KeyBoosterAutoCost, "boosters", "Autocollect price (stars)", nil, 0, big),
		i(KeyBoosterMineCost, "boosters", "1.5x booster price (stars)", nil, 0, big),
		arr(KeyLevelRewards, "levels", "Level-up rewards [{level,coins,stars}] for levels 2..10", []any{}),

		arr(KeyStreakRewards, "daily", "Streak reward coins for days 1..10", nil),
		i(KeyComboPerCorrect, "daily", "Coins per correct combo picture", nil, 0, big),

		arr(KeyTopupPacks, "topup", "Star packs [{stars}] shown in the top-up panel", nil),
		fl(KeyTopupTGRatio, "topup", "Game stars per 1 Telegram Star", 1, 0.0001, 1000),
		fl(KeyTopupTGBonus, "topup", "Bonus % on Telegram Stars payments", 0, 0, 1000),
		fl(KeyTopupTonBonus, "topup", "Bonus % on TON payments (shown on the TON tab)", 0, 0, 1000),
		fl(KeyTopupStarsPerTon, "topup", "Game stars per 1 TON", nil, 0.0001, big),
		s(KeyTopupTonWallet, "topup", "Project TON wallet that receives top-ups", nil),
		i(KeyTopupTonConf, "topup", "Seconds a TON transfer must be old before it is credited (extra safety)", 10, 0, 3600),
		i(KeyTopupTonExpiry, "topup", "Minutes a TON payment order stays open", 30, 5, 1440),

		fl(KeyWdCoinsPerTon, "withdrawal", "Coins per 1 TON", nil, 0.0001, big),
		fl(KeyWdFeeTon, "withdrawal", "Fixed withdrawal fee in TON", nil, 0, 1000),
		i(KeyWdMin, "withdrawal", "Minimum coins per request", nil, 1, big),
		i(KeyWdMax, "withdrawal", "Maximum coins per request", nil, 1, big),
		i(KeyWdCooldownHours, "withdrawal", "Hours between two requests of one player", nil, 0, 100000),
		i(KeyWdMinLevel, "withdrawal", "Minimum player level to withdraw", nil, 1, 10),
		i(KeyWdMinAgeDays, "withdrawal", "Minimum account age in days", nil, 0, 100000),
		i(KeyWdDailyLimit, "withdrawal", "Coins a player may withdraw per day", nil, 1, big),
		i(KeyWdTotalLimit, "withdrawal", "Coins a player may withdraw in total", nil, 1, big),
		fl(KeyWdAutoMaxTon, "withdrawal", "Requests up to this TON amount are paid automatically (0 = all manual)", 0, 0, big),
		fl(KeyWdAutoDailyCap, "withdrawal", "Daily cap of automatic payouts in TON", 0, 0, big),
		s(KeyWdNoticeEN, "withdrawal", "Notice text on the history screen (English)", ""),
		s(KeyWdNoticeRU, "withdrawal", "Notice text on the history screen (Russian)", ""),

		arr(KeySpinTiers, "games", "Spin tiers [{tier,price_stars}]", nil),
		{Key: KeySpinOdds, Kind: KJSON, Group: "games", Desc: "Wheel odds {tier:{prize:weight}}. A prize is a gift key (heart), or coins:500, or stars:25. Weights are relative.", Check: spinOddsCheck},
		arr(KeyGifts, "games", "Gifts [{key,name_en,name_ru,star_value}]", nil),
		i(KeyChestCost, "games", "Chest round price (stars)", nil, 0, big),
		i(KeyChestPicks, "games", "Chests per round", 3, 1, 12),
		arr(KeyChestPool, "games", "Chest prize pool [{type:coins|stars|gift,value,weight,gift}]", nil),
		i(KeyChestExpiryH, "games", "Hours an unfinished chest round stays open (0 = forever)", 0, 0, 100000),
		i(KeyRideCost, "games", "Ride run price (stars)", nil, 0, big),
		i(KeyRideMinSeconds, "games", "Shortest believable Ride run (seconds); the game itself cannot end a run before about 4.5 s, so keep this below that", 3, 0, 3600),
		i(KeyRideMaxSeconds, "games", "A run can be saved for this long after it starts (seconds)", 900, 30, 7200),
		i(KeyRideSpawnDelay, "games", "Seconds before obstacles and coins appear", 3, 0, 60),
		fl(KeyRideMaxCoinsSec, "games", "Most coins one can pick up per second", 8, 0.1, 1000),
		fl(KeyRideMaxPtsSec, "games", "Most score points per second (the game scores 60 per second)", 70, 0.1, 100000),
		i(KeyRideDailyCoins, "games", "Coins a player can earn from Ride per day", nil, 0, big),
		fl(KeyGuardRatio, "games", "Payout guard: maximum payout ratio, e.g. 0.95 (0 = guard off)", 0, 0, 10),
		i(KeyGuardWindowH, "games", "Payout guard window (hours)", 24, 1, 720),
		b(KeySpinPublishOdds, "games", "Show the relative odds of the gifts to players (false = every gift shown with equal weight)", false),
		fl(KeyGuardCoinValue, "games", "Payout guard: how many stars one coin is worth when counting coin prizes (0 = coins are not counted)", 0, 0, 1000000),
		i(KeyWinnersMin, "games", "Smallest prize worth, in stars, shown in the winners feed (0 = every prize)", 0, 0, big),

		obj(KeyRefLevelRewards, "friends", "Coins the inviter gets when a friend reaches a level: {\"2\":200,...}", nil),
		fl(KeyRefMiningPct, "friends", "Percent of a friend's collected coins paid to the inviter", 0, 0, 100),
		i(KeyRefMiningCap, "friends", "Most coins an inviter can earn from one friend's mining", 0, 0, big),
		i(KeyRefJoinBonus, "friends", "Default friend join bonus (coins), paid at friend level 2", 0, 0, big),
		i(KeyTaskLinkSeconds, "tasks", "Seconds before a link task can complete", 5, 0, 600),

		i(KeyNotifDailyCap, "notifications", "Most bot messages per player per day (except payments)", 6, 1, 100),
		i(KeyNotifDefaultHr, "notifications", "Default UTC hour for 'ready' messages", 9, 0, 23),
		arr(KeyNotifWarnHours, "notifications", "Hours before the daily reset to warn", []int{6, 1}),
		obj(KeyNotifQuiet, "notifications", "Quiet hours in UTC {from,to} (null = none)", map[string]int{"from": -1, "to": -1}),
		i(KeyNotifCollectH, "notifications", "Hours after the tank is full before the collect reminder", 6, 1, 720),
		i(KeyNotifIdleH, "notifications", "Hours without fishing before the 'start fishing' message", 12, 1, 720),
		arr(KeyNotifBoosterD, "notifications", "Days before a booster ends to warn", []int{3, 1}),
		b(KeyNotifUpgrade, "notifications", "Tell players when they can afford the next card level", false),
		i(KeyNotifChestH, "notifications", "Hours before reminding about an unfinished chest round", 6, 1, 720),
	}
}
