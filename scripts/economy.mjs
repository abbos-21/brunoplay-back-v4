// The BrunoPlay economy. Every number the owner decides lives in scripts/economy-answers.json (the file filled in with
// BrunoPlay-economy-fill-in.html). buildSettings() turns it into the settings written to the server. Used by two scripts:
//
//   node scripts/economy-report.mjs      simulates the economy (best / base / worst case) and prints the result
//   node scripts/production-seed.mjs     writes these values to a server through the admin API
//
// Nothing is invented or recalculated here: the typed values are written exactly. The only changes made to the answers are
// listed in OVERRIDES. To change the economy, edit the answers file (or fill the HTML again) and run the seed again.
import { readFileSync } from 'node:fs'

const answers = JSON.parse(readFileSync(new URL('./economy-answers.json', import.meta.url), 'utf8'))

// ---------------------------------------------------------------------------------------------------------------------
// Assumptions (the real world, only used by the report, never written to the server)
// ---------------------------------------------------------------------------------------------------------------------
export const ASSUMPTIONS = {
  // what the owner nets for one game star bought with a Telegram Star (Telegram pays out about 0.013 USD per Star)
  netPerTelegramStar: 0.013,
  // what a game star costs when it is bought with TON (the owner keeps all of it, minus the bonus given on the TON tab)
  tonPricePerStar: 0.0166,
  tonBonusPercent: 10,
  // share of star purchases that go through TON (the rest uses Telegram Stars)
  tonShare: 0.4,
  // what one coin is worth in USD: 1,000,000 coins = 1 USD (1.16 TON x 1,000,000 coins)
  coinUsd: 1e-6,
  // what the owner's own Telegram Star / game star is worth when a real gift is bought for a winner
  giftCostPerStar: 0.013,
  // Adsgram pays per completed rewarded ad (USD). Nobody knows this for your audience until you have a month of data.
  adUsd: { worst: 0.0007, base: 0.002, best: 0.006 },
}

// Changes made on purpose to the typed answers (decided together when the answers were checked)
const OVERRIDES = {
  // the guard counts a coin as 0.00006 stars: 1 coin = $0.000001 and 1 star = $0.0166 in the owner's own calculator
  'guard.coin_value_stars': 0.00006,
  // the server does not accept 0 coins here (the minimum is 1); bonus ads per day is 0, so no bonus ad can pay it anyway
  'ads.bonus.coins': 1,
}

const round = (x, d = 0) => Math.round(x * 10 ** d) / 10 ** d
const rows = (name) => answers.tables[name].rows
const num = (v) => (v === null || v === undefined || v === '' ? 0 : Number(v))

export function buildSettings({ wallet, blockIds, links } = {}) {
  const s = {}
  for (const { key, value } of answers.settings) {
    if (value === null || value === undefined) continue
    s[key] = value
  }
  // two keys of the form are one setting on the server
  s['notifications.quiet_hours'] = { from: s['notifications.quiet_hours.from'], to: s['notifications.quiet_hours.to'] }
  delete s['notifications.quiet_hours.from']
  delete s['notifications.quiet_hours.to']
  Object.assign(s, OVERRIDES)
  // the Monetag card is off; 0 is below the server's minimum of 1, so the server's own default stays
  if (!(s['ads.monetag.task_coins'] >= 1)) delete s['ads.monetag.task_coins']

  // ---- cards: the mining speed is the tank divided by the seconds it takes to fill
  const reqs = (r, p) => {
    const out = []
    if (num(r[p + '_other']) > 1) out.push({ type: 'other_card_level', value: num(r[p + '_other']), desc_en: `The other card must be level ${r[p + '_other']}`, desc_ru: `Вторая карточка должна быть ${r[p + '_other']} уровня` })
    if (num(r[p + '_friends']) > 0) out.push({ type: 'invite_friends', value: num(r[p + '_friends']), desc_en: `Invite ${r[p + '_friends']} friends`, desc_ru: `Пригласите друзей: ${r[p + '_friends']}` })
    if (num(r[p + '_plevel']) > 1) out.push({ type: 'player_level', value: num(r[p + '_plevel']), desc_en: `Reach player level ${r[p + '_plevel']}`, desc_ru: `Достигните ${r[p + '_plevel']} уровня игрока` })
    return out
  }
  const card = (kind) =>
    rows('cards').map((r) => {
      const p = kind === 'mining' ? 'mining' : 'cap'
      return {
        level: r.level,
        value: kind === 'mining' ? round(r.tank / (r.hours * 3600), 5) : r.tank,
        cost: num(r[p + '_cost']),
        currency: r[p + '_cur'] ?? 'COIN',
        requirements: reqs(r, p),
      }
    })
  s['cards.mining.levels'] = card('mining')
  s['cards.capacity.levels'] = card('capacity')

  // ---- tables
  s['topup.packs'] = rows('topup_packs').map((r) => ({ stars: r.stars }))
  s['levels.rewards'] = rows('level_rewards').map((r) => ({ level: r.level, coins: num(r.coins), stars: num(r.stars) }))
  s['streak.rewards'] = rows('streak').map((r) => r.coins)
  s['referral.level_rewards'] = Object.fromEntries(rows('referral_levels').map((r) => [r.level, r.coins]))
  s['ads.placements_enabled'] = Object.fromEntries(rows('placements').map((r) => [r.name, r.on === 'Yes']))

  // ---- games
  s.gifts = rows('gifts').map((r) => ({ key: r.key, name_en: r.name_en, name_ru: r.name_ru, star_value: r.star_value }))
  s['spin.tiers'] = rows('spin_tiers').map((r) => ({ tier: r.tier, price_stars: r.price_stars, name_en: r.name_en, name_ru: r.name_ru }))
  const odds = {}
  for (const t of s['spin.tiers']) odds[t.tier] = {}
  for (const r of rows('spin_odds')) {
    for (const [col, tier] of [['t1', 'TIER_1'], ['t2', 'TIER_2'], ['t3', 'TIER_3'], ['t4', 'TIER_4'], ['t5', 'TIER_5']]) {
      if (num(r[col]) > 0) odds[tier][r.prize] = (odds[tier][r.prize] ?? 0) + num(r[col])
    }
  }
  s['spin.odds'] = odds
  s['chest.pool'] = rows('chest_pool').map((r) => (r.type === 'gift' ? { type: 'gift', gift: r.gift, weight: r.weight } : { type: r.type, value: r.amount, weight: r.weight }))

  // values only the owner knows: they are added when given, never invented
  if (wallet) s['topup.ton.wallet_address'] = wallet
  if (blockIds) s['ads.block_ids'] = blockIds
  if (links?.support) s['links.support'] = links.support
  if (links?.news) s['links.news'] = links.news
  if (links?.faq) s['links.faq'] = links.faq

  return {
    settings: s,
    derived: { coinsPerTon: s['withdrawal.coins_per_ton'], starsPerTon: s['topup.ton.stars_per_ton'], spinTiers: s['spin.tiers'], tonUsd: s['withdrawal.coins_per_ton'] * ASSUMPTIONS.coinUsd },
  }
}

/** The starter tasks of the answers file, in the shape of the admin API. */
export function starterTasks() {
  return rows('starter_tasks').map((r) => ({
    category: r.category || 'MAIN',
    type: r.type,
    titleEn: r.title_en,
    titleRu: r.title_ru,
    descEn: r.desc_en,
    descRu: r.desc_ru,
    ...(r.type === 'CHANNEL_JOIN' && r.channel ? { channelUsername: r.channel } : {}),
    required: Math.max(1, num(r.required)),
    rewardCoins: num(r.coins),
    rewardStars: num(r.stars),
    repeat: r.repeat || 'ONCE',
  }))
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
