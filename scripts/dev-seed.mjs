// DEVELOPMENT ONLY. Fills the settings the owner has not decided yet with made-up TEST numbers, so the app can be tried
// end to end against a local database. Never run it against a real server: the values are not the game's economy.
//
//   node scripts/dev-seed.mjs http://localhost:8080 you@example.com            (the admin password is read from ADMIN_PASSWORD)
//
// It also switches ads off (ads.required=false) because a local run has no Adsgram.

const [base = 'http://localhost:8080', email] = process.argv.slice(2)
const password = process.env.ADMIN_PASSWORD
if (!email || !password) {
  console.error('usage: ADMIN_PASSWORD=... node scripts/dev-seed.mjs <api url> <admin email>')
  process.exit(1)
}
if (/brunoplay|duckdns|https:\/\/(?!localhost)/i.test(base)) {
  console.error('refusing to seed a non-local server:', base)
  process.exit(1)
}

const api = async (method, path, token, body) => {
  const res = await fetch(`${base}/api${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${JSON.stringify(data)}`)
  return data
}

const { accessToken } = await api('POST', '/admin/auth/login', '', { email, password })
const set = (key, value) => api('PUT', `/admin/settings/${encodeURIComponent(key)}`, accessToken, { value })

const levels = (value0, valueStep, cost0, costStep, currency = 'COIN') =>
  Array.from({ length: 10 }, (_, i) => ({
    level: i + 1,
    value: Number((value0 + valueStep * i).toFixed(4)),
    cost: Math.round(cost0 + costStep * i),
    currency,
  }))

const gifts = [
  ['heart', 'Heart', 'Сердце', 500000], ['apple', 'Apple', 'Яблоко', 15000], ['ring', 'Ring', 'Кольцо', 8000],
  ['watch', 'Watch', 'Часы', 7500], ['cat', 'Cat', 'Кот', 10000], ['panda', 'Panda', 'Панда', 20000],
  ['cap', 'Cap', 'Кепка', 400000], ['frog', 'Frog', 'Лягушка', 1000000],
].map(([key, en, ru, v]) => ({ key, name_en: en, name_ru: ru, star_value: v }))

const odds = (w) => Object.fromEntries(['heart', 'apple', 'ring', 'watch', 'cat', 'panda', 'cap', 'frog'].map((k, i) => [k, w[i]]))

const values = {
  'ads.required': false,
  'ads.block_ids': { FISHING_START: '1', COLLECT: '1', DOUBLE_STREAK: '1', DOUBLE_COMBO: '1', COMBO_EXTRA: '1', RIDE_FREE: '1', TASK_ADS: '1', BONUS_COINS: '1' },
  'ads.bonus.coins': 50,
  'ads.bonus.max_per_day': 5,
  'ads.combo_extra.max_per_day': 3,
  'ads.ride_free.max_per_day': 3,
  'boosters.autocollect.price_stars': 500,
  'boosters.mining.price_stars': 100,
  'combo.coins_per_correct': 25,
  'streak.rewards': [10, 20, 30, 40, 50, 60, 70, 80, 90, 100],
  'cards.mining.levels': levels(0.025, 0.005, 700, 600),
  'cards.capacity.levels': levels(5, 5, 500, 500),
  'referral.level_rewards': { 2: 200, 3: 300, 4: 400, 5: 500 },
  'chest.round_cost_stars': 10,
  'chest.pool': [
    { type: 'coins', value: 500, weight: 30 }, { type: 'coins', value: 1500, weight: 25 }, { type: 'coins', value: 5000, weight: 15 },
    { type: 'stars', value: 5, weight: 20 }, { type: 'gift', gift: 'apple', weight: 10 },
  ],
  gifts,
  'ride.run_cost_stars': 1,
  'ride.max_coins_per_day': 2000,
  'spin.tiers': [500, 1000, 2500, 5000, 10000].map((p, i) => ({ tier: `TIER_${i + 1}`, price_stars: p, name_en: `Tier ${i + 1}`, name_ru: `Уровень ${i + 1}` })),
  'spin.odds': Object.fromEntries(
    [1, 2, 3, 4, 5].map((n) => [`TIER_${n}`, odds([n, 12, 14, 14, 13, 10, n * 2, n / 2])]),
  ),
  'topup.packs': [{ stars: 200 }, { stars: 500 }, { stars: 1000 }, { stars: 2500 }, { stars: 5000 }],
  'withdrawal.coins_per_ton': 100000,
  'withdrawal.fee_ton': 0.005,
  'withdrawal.min_coins': 10000,
  'withdrawal.max_coins': 5000000,
  'withdrawal.cooldown_hours': 24,
  'withdrawal.min_level': 1,
  'withdrawal.min_account_days': 0,
  'withdrawal.daily_limit_coins': 1000000,
  'withdrawal.total_limit_coins': 10000000,
}

for (const [k, v] of Object.entries(values)) {
  try {
    await set(k, v)
    console.log('ok  ', k)
  } catch (e) {
    console.error('FAIL', k, String(e.message).slice(0, 200))
  }
}
const { missing } = await api('GET', '/admin/settings/missing', accessToken)
console.log('\nstill missing:', missing.length ? missing.join(', ') : 'nothing')
