// Reads the wheel as it is set on a server and tells you what it pays back, tier by tier. Use it after you change spin.odds
// (for example after adding coin or star prizes): the weights are relative numbers, so it is hard to judge them by eye.
//
//   ADMIN_PASSWORD='...' node scripts/wheel-rtp.mjs --api https://brunoplay.duckdns.org --email you@example.com
//
// "Gives back" is what a player wins on average for every 100 stars they spend. A good wheel gives back 70 to 80.
// Coins count for guard.coin_value_stars stars each, the same number the payout guard uses.
import { prizeWorth } from './economy.mjs'

const argv = process.argv.slice(2)
const opt = (name) => {
  const i = argv.indexOf(name)
  return i >= 0 ? argv[i + 1] : undefined
}
const base = (opt('--api') ?? '').replace(/\/+$/, '')
const email = opt('--email')
const password = process.env.ADMIN_PASSWORD
if (!base || !email || !password) {
  console.error('usage: ADMIN_PASSWORD=... node scripts/wheel-rtp.mjs --api <https://...> --email <admin email>')
  process.exit(1)
}

const call = async (method, path, token, body) => {
  const res = await fetch(`${base}/api${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${JSON.stringify(data).slice(0, 200)}`)
  return data
}

const { accessToken } = await call('POST', '/admin/auth/login', '', { email, password })
const all = (await call('GET', '/admin/settings', accessToken)).settings ?? []
const get = (key) => all.find((s) => s.key === key)?.value
const settings = {
  'spin.tiers': get('spin.tiers'),
  'spin.odds': get('spin.odds'),
  gifts: get('gifts'),
  'guard.coin_value_stars': get('guard.coin_value_stars') ?? 0,
}
if (!settings['spin.tiers'] || !settings['spin.odds'] || !settings.gifts) {
  console.error('The server has no spin tiers, odds or gifts yet.')
  process.exit(1)
}

const pct = (x) => (x * 100).toFixed(1) + '%'
console.log(`Coin value used by the guard: ${settings['guard.coin_value_stars']} stars per coin\n`)
for (const t of settings['spin.tiers']) {
  const odds = settings['spin.odds'][t.tier] ?? {}
  const sum = Object.values(odds).reduce((a, b) => a + b, 0)
  if (!(sum > 0)) {
    console.log(`${t.name_en ?? t.tier} (${t.price_stars} stars): no prizes set`)
    continue
  }
  const parts = { gift: 0, coins: 0, stars: 0 }
  const chance = { gift: 0, coins: 0, stars: 0 }
  for (const [key, w] of Object.entries(odds)) {
    const kind = key.startsWith('coins:') ? 'coins' : key.startsWith('stars:') ? 'stars' : 'gift'
    parts[kind] += (w / sum) * prizeWorth(settings, key)
    chance[kind] += w / sum
  }
  const ev = parts.gift + parts.coins + parts.stars
  console.log(`${t.name_en ?? t.tier}  price ${t.price_stars} stars   average win ${ev.toFixed(1)} stars   GIVES BACK ${pct(ev / t.price_stars)}`)
  console.log(`   chance of a gift ${pct(chance.gift)}, of coins ${pct(chance.coins)}, of stars ${pct(chance.stars)}   (they give back ${pct(parts.gift / t.price_stars)} / ${pct(parts.coins / t.price_stars)} / ${pct(parts.stars / t.price_stars)} of the price)`)
}
console.log('\nAbout 70 to 80% is a healthy wheel. Above 85% the payout guard starts replacing big prizes with small ones.')
