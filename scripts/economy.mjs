// The BrunoPlay economy: every number the owner has to supply (docs/SETTINGS.md), chosen together so that the game is fun
// to play, pays something real to the players who stay, and still earns money. Used by two scripts:
//
//   node scripts/economy-report.mjs      simulates the economy (best / base / worst case) and prints the result
//   node scripts/production-seed.mjs     writes these values to a server through the admin API
//
// All money in this file is derived from the few ASSUMPTIONS below. If one of them is wrong in real life, change it here,
// run the report, and look at the result before you seed.

// ---------------------------------------------------------------------------------------------------------------------
// Assumptions (the real world, not game settings)
// ---------------------------------------------------------------------------------------------------------------------
export const ASSUMPTIONS = {
  // what the owner nets for one game star bought with a Telegram Star (Telegram pays out about 0.013 USD per Star)
  netPerTelegramStar: 0.013,
  // what a game star costs when it is bought with TON (the owner keeps all of it, minus the bonus given on the TON tab)
  tonPricePerStar: 0.0175,
  tonBonusPercent: 10,
  // share of star purchases that go through TON (the rest uses Telegram Stars)
  tonShare: 0.4,
  // what one coin is worth in USD at the moment the rate is set. This is THE number that decides how much the players
  // are paid: 1,000,000 coins = 1.25 USD. Everything else is built around it.
  coinUsd: 1.25e-6,
  // what the owner's own Telegram Star / game star is worth when a real gift is bought for a winner
  giftCostPerStar: 0.013,
  // Adsgram pays per completed rewarded ad (USD). Nobody knows this for your audience until you have a month of data.
  adUsd: { worst: 0.0007, base: 0.002, best: 0.006 },
}

const round = (x, d = 0) => Math.round(x * 10 ** d) / 10 ** d
const roundTo = (x, step) => Math.round(x / step) * step

// ---------------------------------------------------------------------------------------------------------------------
// The design
// ---------------------------------------------------------------------------------------------------------------------
export function buildSettings({ tonUsd, wallet, blockIds, links } = {}) {
  if (!(tonUsd > 0)) throw new Error('tonUsd (the price of 1 TON in USD) is required')
  const A = ASSUMPTIONS

  // ---- the two rates that depend on the price of TON (re-run the seed when TON moves a lot)
  const coinsPerTon = roundTo(tonUsd / A.coinUsd, 10000)
  const starsPerTon = Math.max(1, round(tonUsd / A.tonPricePerStar))

  // ---- fishing cards. Capacity is the tank (coins); the mining rate makes the tank fill in `hours`.
  // Small tanks that fill fast, so a player has a reason to open the game every one to two hours. The earning power for a
  // player who collects on time is about the same as before at levels 1 to 7 (a bit more at 1 to 6, a bit less above), but it
  // comes in many small collects instead of a few big ones. Every collect also shows ads, and a small collect pays less than
  // the ads it brings, so more entries is better for the project. The old design was 400 ... 10000 coins filling in 5 ... 3 hours.
  const capacity = [200, 300, 450, 650, 900, 1150, 1400, 1650, 1850, 2000]
  const hours = [2, 2, 2, 1.75, 1.5, 1.5, 1.25, 1.25, 1, 1]
  const rate = capacity.map((c, i) => round(c / (hours[i] * 3600), 5))
  const L = (n, ru, en) => ({ en, ru })

  const other = (n) => ({
    type: 'other_card_level', value: n,
    desc_en: `The other card must be level ${n}`, desc_ru: `Вторая карточка должна быть ${n} уровня`,
  })
  const invite = (n) => ({
    type: 'invite_friends', value: n,
    desc_en: `Invite ${n} friends`, desc_ru: `Пригласите друзей: ${n}`,
  })

  const miningCoins = [0, 800, 2000, 4500, 9000]
  const miningStars = [40, 70, 110, 170, 260]
  const capCoins = [0, 600, 1500, 3500, 7000]
  const capStars = [30, 55, 90, 140, 220]
  const miningReq = { 4: [other(3)], 5: [other(4), invite(2)], 6: [other(5)], 7: [other(6), invite(3)], 8: [other(7)], 9: [other(8)], 10: [other(9), invite(5)] }
  const capReq = { 3: [other(2)], 4: [other(3)], 5: [other(4)], 6: [other(5)], 7: [other(6)], 8: [other(7), invite(3)], 9: [other(8)], 10: [other(9)] }
  const table = (values, coins, stars, req) =>
    values.map((value, i) => {
      const level = i + 1
      const useCoins = level <= 5
      return {
        level, value,
        cost: level === 1 ? 0 : useCoins ? coins[level - 1] : stars[level - 6],
        currency: level <= 5 ? 'COIN' : 'STAR',
        requirements: req[level] ?? [],
      }
    })

  // ---- gifts and the spin (see tuneSpin below: the odds are computed so every tier pays out the intended share)
  const gifts = [
    ['apple', 'Apple', 'Яблоко', 15],
    ['cat', 'Cat', 'Кот', 25],
    ['panda', 'Panda', 'Панда', 50],
    ['watch', 'Watch', 'Часы', 100],
    ['ring', 'Ring', 'Кольцо', 250],
    ['cap', 'Cap', 'Кепка', 1000],
    ['heart', 'Heart', 'Сердце', 2500],
    ['frog', 'Frog', 'Лягушка', 10000],
  ].map(([key, en, ru, v]) => ({ key, name_en: en, name_ru: ru, star_value: v }))

  const spinTiers = [
    { tier: 'TIER_1', price_stars: 30, rtp: 0.7, name_en: 'Bronze', name_ru: 'Бронза' },
    { tier: 'TIER_2', price_stars: 75, rtp: 0.72, name_en: 'Silver', name_ru: 'Серебро' },
    { tier: 'TIER_3', price_stars: 150, rtp: 0.75, name_en: 'Gold', name_ru: 'Золото' },
    { tier: 'TIER_4', price_stars: 400, rtp: 0.78, name_en: 'Platinum', name_ru: 'Платина' },
    { tier: 'TIER_5', price_stars: 1000, rtp: 0.8, name_en: 'Diamond', name_ru: 'Алмаз' },
  ]
  const odds = tuneSpin(gifts, spinTiers)

  // ---- chest: 3 picks per round, prizes drawn when the round is paid
  const chestPool = [
    { type: 'coins', value: 3000, weight: 300 },
    { type: 'coins', value: 10000, weight: 200 },
    { type: 'coins', value: 30000, weight: 90 },
    { type: 'stars', value: 3, weight: 170 },
    { type: 'stars', value: 8, weight: 100 },
    { type: 'stars', value: 25, weight: 25 },
    { type: 'gift', gift: 'apple', weight: 70 },
    { type: 'gift', gift: 'cat', weight: 35 },
    { type: 'gift', gift: 'panda', weight: 10 },
  ]

  const streak = [200, 250, 300, 400, 500, 600, 750, 900, 1100, 1500]

  const s = {
    // ---- ads: ALWAYS required in production, they are the main income
    'ads.required': true,
    'ads.view_expiry_seconds': 300,
    'ads.max_failed_loads': 3,
    'ads.fallback_minutes': 10,
    'ads.bonus.coins': 250,
    'ads.bonus.max_per_day': 12,
    'ads.combo_extra.max_per_day': 2,
    'ads.ride_free.max_per_day': 3,

    // ---- fishing
    'fishing.collect_min_percent': 10,
    'cards.mining.levels': table(rate, miningCoins, miningStars, miningReq),
    'cards.capacity.levels': table(capacity, capCoins, capStars, capReq),

    // ---- boosters
    'boosters.autocollect.price_stars': 150,
    'boosters.mining.price_stars': 70,
    'boosters.mining.multiplier': 1.5,
    'boosters.duration_days': 30,

    // ---- levels: coins every level, a few stars from level 5 on
    'levels.rewards': [
      { level: 2, coins: 500, stars: 0 }, { level: 3, coins: 1000, stars: 0 }, { level: 4, coins: 2000, stars: 0 },
      { level: 5, coins: 4000, stars: 3 }, { level: 6, coins: 6000, stars: 5 }, { level: 7, coins: 9000, stars: 8 },
      { level: 8, coins: 13000, stars: 10 }, { level: 9, coins: 18000, stars: 15 }, { level: 10, coins: 30000, stars: 25 },
    ],

    // ---- daily
    'streak.rewards': streak,
    'combo.coins_per_correct': 300,

    // ---- friends
    'referral.level_rewards': { 2: 1500, 3: 2500, 4: 4000, 5: 6000, 6: 9000, 7: 13000, 8: 18000, 9: 25000, 10: 35000 },
    'referral.mining_percent': 5,
    'referral.mining_cap_per_friend': 30000,
    'referral.default_join_bonus': 1000,

    // ---- games
    'spin.tiers': spinTiers.map(({ rtp, ...t }) => t),
    'spin.odds': odds,
    'spin.publish_odds': false,
    'spin.winners_min_value': 250,
    gifts,
    'chest.round_cost_stars': 20,
    'chest.picks_per_round': 3,
    'chest.pool': chestPool,
    'chest.round_expiry_hours': 0,
    'ride.run_cost_stars': 2,
    'ride.max_coins_per_day': 4000,
    'ride.max_coins_per_second': 8,
    'ride.max_points_per_second': 70,
    'ride.min_duration_seconds': 3,
    'ride.max_duration_seconds': 900,
    'ride.spawn_delay_seconds': 3,
    // the safety net for the prize games: over any 72 hours a tier never pays out more than 85% of what it took in
    'guard.max_payout_ratio': 0.85,
    'guard.window_hours': 72,
    // one star is worth about 10,000 coins, so a coin prize counts for that little in the payout ratio
    'guard.coin_value_stars': 0.0001,

    // ---- stars top-up
    'topup.packs': [100, 250, 500, 1000, 2500, 5000, 10000, 25000].map((stars) => ({ stars })),
    'topup.telegram.ratio': 1,
    'topup.telegram.bonus_percent': 0,
    'topup.ton.bonus_percent': A.tonBonusPercent,
    'topup.ton.stars_per_ton': starsPerTon,
    'topup.ton.confirmations': 10,
    'topup.ton.order_expiry_minutes': 30,

    // ---- coins -> TON. 1,000,000 coins is about 1.25 USD. A first payout needs level 2, no account-age requirement and
    // 0.05 TON of coins (kept at 0.05 TON when the TON price moves); a player can take at most 0.5 USD a day and
    // 100 TON in total. Payouts that leave the player up to 0.075 TON are sent automatically.
    'withdrawal.coins_per_ton': coinsPerTon,
    'withdrawal.fee_ton': 0.02,
    'withdrawal.min_coins': roundTo(0.05 * coinsPerTon, 1000),
    'withdrawal.max_coins': 400000,
    'withdrawal.daily_limit_coins': 400000,
    'withdrawal.total_limit_coins': roundTo(100 * coinsPerTon, 1000000), // 100 TON
    'withdrawal.cooldown_hours': 48,
    'withdrawal.min_level': 2,
    'withdrawal.min_account_days': 0,
    // automatic payouts (needs AUTO_PAYOUT=true and the payout wallet in the server environment, see docs/DEPLOY-VPS.md):
    // a request whose payout (after the fee) is up to 0.075 TON is sent by the server; bigger ones wait for you.
    // The daily cap is the most the server may send by itself in one day; above it requests wait for you.
    'withdrawal.auto_payout_max_ton': 0.075,
    'withdrawal.auto_payout_daily_cap_ton': 10,
    'withdrawal.notice_en': 'Small payouts are sent automatically within minutes, larger ones are checked by hand, usually within 24 hours. The network fee is deducted from the amount.',
    'withdrawal.notice_ru': 'Небольшие выплаты отправляются автоматически за несколько минут, крупные проверяются вручную, обычно в течение 24 часов. Комиссия сети вычитается из суммы.',

    // ---- tasks, risk, retention
    'tasks.link_min_seconds': 5,
    'risk.hold_threshold': 30,
    'retention.logs_days': 90,
    'retention.ad_views_days': 30,

    // ---- the bot (UTC; the audience is mostly UTC+3..+5, so 20:00-04:00 UTC is the local night)
    'notifications.daily_cap': 5,
    'notifications.default_hour_utc': 9,
    'notifications.warn_hours': [6, 1],
    'notifications.quiet_hours': { from: 20, to: 4 },
    'notifications.collect_reminder_hours': 6,
    'notifications.idle_hours': 12,
    'notifications.booster_expiry_days': [3, 1],
    'notifications.card_upgrade_ready': true,
    'notifications.chest_round_hours': 6,
  }

  // values only the owner knows: they are added when given, never invented
  if (wallet) s['topup.ton.wallet_address'] = wallet
  if (blockIds) s['ads.block_ids'] = blockIds
  if (links?.support) s['links.support'] = links.support
  if (links?.news) s['links.news'] = links.news
  if (links?.faq) s['links.faq'] = links.faq

  return { settings: s, derived: { coinsPerTon, starsPerTon, spinTiers, tonUsd } }
}

// ---------------------------------------------------------------------------------------------------------------------
// Spin odds. Every tier has the same eight gifts; a higher tier tilts the odds toward the valuable ones. The tilt is found
// by bisection so that the expected value of a spin is exactly `rtp` x the tier price (what the tier gives back on average).
// ---------------------------------------------------------------------------------------------------------------------
function tuneSpin(gifts, tiers) {
  const base = { apple: 40, cat: 28, panda: 18, watch: 8, ring: 4, cap: 1.5, heart: 0.4, frog: 0.1 }
  const out = {}
  for (const t of tiers) {
    const target = t.rtp * t.price_stars
    const ev = (beta) => {
      const w = gifts.map((g) => base[g.key] * g.star_value ** beta)
      const sum = w.reduce((a, b) => a + b, 0)
      return gifts.reduce((a, g, i) => a + (w[i] / sum) * g.star_value, 0)
    }
    let lo = -2, hi = 3
    for (let i = 0; i < 80; i++) {
      const mid = (lo + hi) / 2
      if (ev(mid) < target) lo = mid
      else hi = mid
    }
    const beta = (lo + hi) / 2
    const w = gifts.map((g) => base[g.key] * g.star_value ** beta)
    const sum = w.reduce((a, b) => a + b, 0)
    out[t.tier] = Object.fromEntries(gifts.map((g, i) => [g.key, Math.max(1, Math.round((w[i] / sum) * 100000))]))
  }
  return out
}

/** What one prize of the odds table is worth in stars: a gift key, or "coins:500" / "stars:25" (a coin counts for guard.coin_value_stars). */
export function prizeWorth(settings, key) {
  const [kind, amount] = key.split(':')
  if (amount !== undefined) {
    const n = Number(amount)
    if (kind === 'stars') return n
    if (kind === 'coins') return n * (settings['guard.coin_value_stars'] ?? 0)
    return 0
  }
  return settings.gifts.find((g) => g.key === key)?.star_value ?? 0
}

/** The expected return of one spin / chest pick (in stars), computed from the settings that will be written. */
export function spinRtp(settings, tier) {
  const price = settings['spin.tiers'].find((t) => t.tier === tier).price_stars
  const odds = settings['spin.odds'][tier]
  const sum = Object.values(odds).reduce((a, b) => a + b, 0)
  const ev = Object.entries(odds).reduce((a, [k, w]) => a + (w / sum) * prizeWorth(settings, k), 0)
  return { price, ev, rtp: ev / price, jackpotChance: ((odds.frog ?? 0) + (odds.heart ?? 0)) / sum }
}

export function chestRtp(settings) {
  const value = Object.fromEntries(settings.gifts.map((g) => [g.key, g.star_value]))
  const coinStars = settings['guard.coin_value_stars']
  const pool = settings['chest.pool']
  const sum = pool.reduce((a, p) => a + p.weight, 0)
  const perPick = pool.reduce((a, p) => a + (p.weight / sum) * (p.type === 'coins' ? p.value * coinStars : p.type === 'stars' ? p.value : value[p.gift]), 0)
  const round = perPick * settings['chest.picks_per_round']
  return { perPick, round, rtp: round / settings['chest.round_cost_stars'] }
}

// ---------------------------------------------------------------------------------------------------------------------
// The simulation: players of several kinds live through the game day by day, using the real tables above. It is an
// expectation (no dice), so the same input always gives the same answer.
// ---------------------------------------------------------------------------------------------------------------------

// One kind of player. `share` of the people who install the game; `buyers` of them buy `stars` stars in total.
export const PLAYERS = [
  { name: 'Leaves in 2 days', share: 0.5, days: 2, visits: 2, bonusAds: 3, combo: 0.5, ride: 0, double: 0.2, friends: 0, buyers: 0, stars: 0 },
  { name: 'Casual, 10 days', share: 0.27, days: 10, visits: 2.5, bonusAds: 5, combo: 0.7, ride: 0.3, double: 0.3, friends: 0.2, buyers: 0, stars: 0 },
  { name: 'Regular, 45 days', share: 0.13, days: 45, visits: 3.5, bonusAds: 8, combo: 0.85, ride: 0.5, double: 0.4, friends: 0.4, buyers: 0.05, stars: 300 },
  { name: 'Grinder, 90 days', share: 0.07, days: 90, visits: 5, bonusAds: 12, combo: 0.95, ride: 0.8, double: 0.5, friends: 1.5, buyers: 0.1, stars: 600 },
  { name: 'Spender, 90 days', share: 0.02, days: 90, visits: 4, bonusAds: 6, combo: 0.9, ride: 0.6, double: 0.5, friends: 1, buyers: 1, stars: 1800, auto: true },
  { name: 'Whale, 90 days', share: 0.002, days: 90, visits: 4, bonusAds: 6, combo: 0.9, ride: 0.6, double: 0.5, friends: 2, buyers: 1, stars: 12000, auto: true },
  // bots / multi-accounts: they play as hard as they can for two weeks and try to cash out at once
  { name: 'Farm account', share: 0.03, days: 14, visits: 6, bonusAds: 12, combo: 1, ride: 1, double: 0.5, friends: 0, buyers: 0, stars: 0, farm: true },
]

export const SCENARIOS = {
  worst: { adUsd: 'worst', buy: 0.4, claim: 0.5, ton: 1.6, farmAd: 0, farmShare: 0.06 },
  base: { adUsd: 'base', buy: 1, claim: 0.25, ton: 1, farmAd: 0.5, farmShare: 0.03 },
  best: { adUsd: 'best', buy: 1.5, claim: 0.1, ton: 0.7, farmAd: 1, farmShare: 0.01 },
  // nobody ever buys a star and ads pay the base rate: the game has to survive on ads alone
  nobuy: { adUsd: 'base', buy: 0, claim: 0, ton: 1, farmAd: 0.5, farmShare: 0.03 },
  // the same, in the bad ad market with TON up 60%
  nobuyWorst: { adUsd: 'worst', buy: 0, claim: 0, ton: 1.6, farmAd: 0, farmShare: 0.06 },
}

function runPlayer(S, a, buyer, sc) {
  const A = ASSUMPTIONS
  const mining = S['cards.mining.levels']
  const cap = S['cards.capacity.levels']
  const levels = S['levels.rewards']
  const wd = {
    min: S['withdrawal.min_coins'], max: S['withdrawal.max_coins'], daily: S['withdrawal.daily_limit_coins'],
    total: S['withdrawal.total_limit_coins'], cool: S['withdrawal.cooldown_hours'] / 24, minLevel: S['withdrawal.min_level'],
    minAge: S['withdrawal.min_account_days'],
  }
  const adr = A.adUsd[sc.adUsd]
  let mL = 1, cL = 1, coins = 0, stars = 0
  let autoUntil = -1, boostUntil = -1
  let ads = 0, withdrawn = 0, lastWd = -99, bought = 0, spinSpent = 0, freeStars = 0
  let firstWd = null, lvl5 = null
  const friendsAt = (d) => a.friends * Math.min(1, d / 20)
  const level = () => Math.min(mL, cL)
  const reqOk = (r, d) => (r ?? []).every((q) => (q.type === 'other_card_level' ? true : q.type === 'invite_friends' ? friendsAt(d) >= q.value : q.type === 'player_level' ? level() >= q.value : true))
  const otherOk = (card, next, d) => {
    const reqs = (card === 'm' ? mining : cap)[next - 1].requirements ?? []
    return reqs.every((q) => {
      if (q.type === 'other_card_level') return (card === 'm' ? cL : mL) >= q.value
      return reqOk([q], d)
    })
  }
  const grant = (c, s) => { coins += c; stars += s; freeStars += s }
  const tryUpgrades = (d) => {
    for (;;) {
      const options = []
      if (mL < 10 && otherOk('m', mL + 1, d)) options.push({ card: 'm', row: mining[mL] })
      if (cL < 10 && otherOk('c', cL + 1, d)) options.push({ card: 'c', row: cap[cL] })
      const affordable = options
        .filter((o) => (o.row.currency === 'COIN' ? coins >= o.row.cost : stars >= o.row.cost))
        .sort((x, y) => (x.row.currency === 'COIN' ? x.row.cost * A.coinUsd : x.row.cost * 0.014) - (y.row.currency === 'COIN' ? y.row.cost * A.coinUsd : y.row.cost * 0.014))
      const o = affordable[0]
      if (!o) return
      if (o.row.currency === 'COIN') coins -= o.row.cost
      else stars -= o.row.cost
      const before = level()
      if (o.card === 'm') mL++
      else cL++
      if (level() > before) {
        const r = levels.find((x) => x.level === level())
        if (r) grant(r.coins, r.stars)
        if (level() === 5 && lvl5 === null) lvl5 = d
      }
    }
  }
  const purchases = buyer ? [[3, 0.4], [10, 0.6]] : []
  for (let d = 1; d <= a.days; d++) {
    for (const [day, part] of purchases) if (d === day || (a.days < day && d === a.days)) { const n = a.stars * part; stars += n; bought += n }
    const T = cap[cL - 1].value / mining[mL - 1].value / 3600 // hours to fill the tank
    const boosted = d <= boostUntil
    const hoursFull = boosted ? T / (S['boosters.mining.multiplier']) : T
    const auto = d <= autoUntil
    if (a.auto && buyer && !auto && stars >= S['boosters.autocollect.price_stars'] && level() >= 3) { stars -= S['boosters.autocollect.price_stars']; autoUntil = d + 29 }
    if (a.auto && buyer && !boosted && stars >= S['boosters.mining.price_stars'] + 60) { stars -= S['boosters.mining.price_stars']; boostUntil = d + 29 }
    const cyclesMax = 24 / hoursFull
    const cycles = auto ? cyclesMax : Math.min(a.visits, cyclesMax)
    coins += cycles * cap[cL - 1].value
    ads += auto ? (d === 1 || d === autoUntil - 29 ? 1 : 0) : cycles * 2
    // streak
    const sr = S['streak.rewards'][(d - 1) % 10]
    coins += sr * (1 + a.double)
    ads += a.double
    // combo: a free try is nearly a guess; with the ad-bought tries the player learns the pattern
    const extra = Math.min(2, S['ads.combo_extra.max_per_day'])
    const comboCoins = (0.3 * 0.5 + 0.7 * 3.2) * S['combo.coins_per_correct']
    coins += a.combo * comboCoins
    ads += a.combo * (0.7 * extra + 0.3 * a.double)
    // bonus ads
    const nb = Math.min(a.bonusAds, S['ads.bonus.max_per_day'])
    coins += nb * S['ads.bonus.coins']
    ads += nb
    // free Ride runs
    const runs = a.ride * Math.min(3, S['ads.ride_free.max_per_day'])
    coins += Math.min(S['ride.max_coins_per_day'], runs * 700)
    ads += runs
    // first-week tasks and friends
    if (d === 3 && a.days >= 3) coins += 6000
    if (d === 15) coins += a.friends * 5000
    tryUpgrades(d)
    // cash out
    if (level() >= wd.minLevel && d >= wd.minAge && coins >= wd.min && d - lastWd >= wd.cool && withdrawn < wd.total) {
      const w = Math.min(coins, wd.max, wd.daily, wd.total - withdrawn)
      if (w >= wd.min) { coins -= w; withdrawn += w; lastWd = d; if (firstWd === null) firstWd = d }
    }
  }
  // what is left of the stars is played: spin (60%), chest (30%), ride runs (10%); winnings are sold or claimed as real gifts
  let giftCost = 0
  let play = stars
  if (play > 0 && buyer) {
    const t = S['spin.tiers']
    const r = spinRtp(S, play >= 3000 ? 'TIER_4' : play >= 800 ? 'TIER_3' : play >= 300 ? 'TIER_2' : 'TIER_1')
    const c = chestRtp(S)
    const rtp = 0.6 * r.rtp + 0.3 * c.rtp + 0.1 * 0
    const q = sc.claim
    const total = play / (1 - rtp * (1 - q) * 0.8) // sold winnings are played again
    spinSpent = total
    giftCost = total * rtp * q * A.giftCostPerStar
  }
  const payoutUsd = withdrawn * A.coinUsd * sc.ton
  const netPerStar = (1 - A.tonShare) * A.netPerTelegramStar + A.tonShare * (A.tonPricePerStar / (1 + A.tonBonusPercent / 100)) * sc.ton
  const purchaseUsd = bought * sc.buy * netPerStar
  const adUsd = ads * adr * (a.farm ? sc.farmAd : 1)
  const freeStarCost = freeStars * 0.2 * netPerStar
  return { ads, adUsd, purchaseUsd, payoutUsd, giftCost: giftCost * (buyer ? sc.buy : 1), freeStarCost, firstWd, lvl5, level: level(), withdrawn }
}

export function simulate(S, scenarioName) {
  const sc = SCENARIOS[scenarioName]
  const people = PLAYERS.map((p) => (p.farm ? { ...p, share: sc.farmShare } : p))
  const total = people.reduce((a, p) => a + p.share, 0)
  const rows = []
  const sum = { adUsd: 0, purchaseUsd: 0, payoutUsd: 0, giftCost: 0, freeStarCost: 0 }
  for (const p of people) {
    const share = p.share / total
    const parts = p.buyers > 0 && p.buyers < 1 ? [[true, p.buyers], [false, 1 - p.buyers]] : p.buyers >= 1 ? [[true, 1]] : [[false, 1]]
    const row = { name: p.name, share, adUsd: 0, purchaseUsd: 0, payoutUsd: 0, giftCost: 0, freeStarCost: 0, firstWd: null, level: 0, lvl5: null }
    for (const [buyer, w] of parts) {
      const r = runPlayer(S, p, buyer, sc)
      for (const k of ['adUsd', 'purchaseUsd', 'payoutUsd', 'giftCost', 'freeStarCost']) row[k] += r[k] * w
      if (!buyer || parts.length === 1) { row.firstWd = r.firstWd; row.level = r.level; row.lvl5 = r.lvl5 }
    }
    for (const k of Object.keys(sum)) sum[k] += row[k] * share
    rows.push(row)
  }
  const revenue = sum.adUsd + sum.purchaseUsd
  const cost = sum.payoutUsd + sum.giftCost + sum.freeStarCost
  return { rows, per1000: Object.fromEntries(Object.entries(sum).map(([k, v]) => [k, v * 1000])), revenue: revenue * 1000, cost: cost * 1000, net: (revenue - cost) * 1000, margin: (revenue - cost) / revenue }
}
