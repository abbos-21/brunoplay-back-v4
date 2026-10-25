// Writes the economy in scripts/economy.mjs to a server through the admin API, and creates the starter tasks.
// Safe to run again: it only sets values, and it creates tasks only when the task list is empty.
//
//   ADMIN_PASSWORD='...' node scripts/production-seed.mjs --api https://api.example.com --email you@example.com            (shows what it would do)
//   ADMIN_PASSWORD='...' node scripts/production-seed.mjs --api https://api.example.com --email you@example.com --apply    (does it)
//
// Options
//   --ton-usd 1.24|auto   price of 1 TON in USD; "auto" (the default) asks a public price API. The coin and star rates follow it.
//   --wallet UQ...        the project TON wallet that receives star top-ups (nothing is written for it if you leave it out)
//   --block-id 12345      one Adsgram block id used for every ad placement, or
//   --block-ids '{"FISHING_START":"1","COLLECT":"2",...}'   one per placement (all 8 placements)
//   --support / --news / --faq <https link>       links shown in the menu
//   --channel @name / --chat @name                 your channel and chat for the "join" tasks
//   --skip-tasks          do not create the starter tasks
//
// Values only you know (wallet, Adsgram blocks, links, channels) are never invented: whatever you do not pass is listed at the end.
import { buildSettings } from './economy.mjs'

const argv = process.argv.slice(2)
const flag = (name) => argv.includes(name)
const opt = (name) => {
  const i = argv.indexOf(name)
  return i >= 0 ? argv[i + 1] : undefined
}
const fail = (msg) => {
  console.error('\n' + msg + '\n')
  process.exit(1)
}

const base = (opt('--api') ?? '').replace(/\/+$/, '')
const email = opt('--email')
const password = process.env.ADMIN_PASSWORD
const apply = flag('--apply')
if (!base || !email) fail('usage: ADMIN_PASSWORD=... node scripts/production-seed.mjs --api <https://api...> --email <admin email> [--apply] [options]\n(see the top of this file)')
if (!password) fail('Set the admin password in the ADMIN_PASSWORD environment variable (it is never taken from an argument or written to a file).')

// ---- the price of TON
async function tonPrice() {
  const given = opt('--ton-usd') ?? 'auto'
  if (given !== 'auto') {
    const n = Number(given)
    if (!(n > 0)) fail('--ton-usd must be a number')
    return { usd: n, from: 'given' }
  }
  const sources = [
    ['tonapi.io', 'https://tonapi.io/v2/rates?tokens=ton&currencies=usd', (j) => j.rates.TON.prices.USD],
    ['coingecko.com', 'https://api.coingecko.com/api/v3/simple/price?ids=the-open-network&vs_currencies=usd', (j) => j['the-open-network'].usd],
  ]
  for (const [name, url, pick] of sources) {
    try {
      const j = await (await fetch(url, { signal: AbortSignal.timeout(10000) })).json()
      const v = Number(pick(j))
      if (v > 0.05 && v < 1000) return { usd: v, from: name }
    } catch {
      // try the next one
    }
  }
  fail('Could not read the TON price. Pass it yourself: --ton-usd 1.24')
}

const price = await tonPrice()
let blockIds
if (opt('--block-ids')) {
  try {
    blockIds = JSON.parse(opt('--block-ids'))
  } catch {
    fail('--block-ids must be JSON')
  }
} else if (opt('--block-id')) {
  const id = String(opt('--block-id'))
  blockIds = Object.fromEntries(['FISHING_START', 'COLLECT', 'DOUBLE_STREAK', 'DOUBLE_COMBO', 'COMBO_EXTRA', 'RIDE_FREE', 'TASK_ADS', 'BONUS_COINS'].map((p) => [p, id]))
}
const { settings, derived } = buildSettings({
  tonUsd: price.usd,
  wallet: opt('--wallet'),
  blockIds,
  links: { support: opt('--support'), news: opt('--news'), faq: opt('--faq') },
})

console.log(`TON price: $${price.usd} (${price.from})  ->  1 TON = ${derived.coinsPerTon.toLocaleString()} coins = ${derived.starsPerTon} stars`)
console.log(`Server: ${base}   mode: ${apply ? 'APPLY (writes)' : 'dry run (writes nothing)'}`)

const api = async (method, path, token, body) => {
  const res = await fetch(`${base}/api${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${JSON.stringify(data).slice(0, 300)}`)
  return data
}

const login = await api('POST', '/admin/auth/login', '', { email, password })
const token = login.accessToken
const current = await api('GET', '/admin/settings', token).catch(() => null)

const keys = Object.keys(settings)
let ok = 0
let changed = 0
const failed = []
for (const key of keys) {
  const value = settings[key]
  const before = current?.settings?.find?.((s) => s.key === key)
  const same = before && JSON.stringify(before.value) === JSON.stringify(value)
  if (same) {
    ok++
    continue
  }
  if (!apply) {
    console.log('would set', key)
    changed++
    continue
  }
  try {
    await api('PUT', `/admin/settings/${encodeURIComponent(key)}`, token, { value })
    console.log('set  ', key)
    changed++
  } catch (e) {
    failed.push(key)
    console.error('FAIL ', key, String(e.message).slice(0, 220))
  }
}
console.log(`\n${keys.length} values: ${ok} already right, ${changed} ${apply ? 'written' : 'would be written'}, ${failed.length} failed`)

// ---- starter tasks (only when there are none)
if (!flag('--skip-tasks')) {
  const existing = await api('GET', '/admin/tasks', token).catch(() => ({ tasks: [] }))
  const have = existing.tasks ?? existing.items ?? []
  if (have.length) {
    console.log(`\nTasks: ${have.length} already exist, leaving them alone`)
  } else {
    const tasks = []
    let order = 1
    const channel = opt('--channel')
    const chat = opt('--chat')
    if (channel) tasks.push({ category: 'MAIN', type: 'CHANNEL_JOIN', titleEn: 'Join the BrunoPlay channel', titleRu: 'Подпишитесь на канал BrunoPlay', descEn: 'Subscribe to our channel for news and giveaways.', descRu: 'Подпишитесь на наш канал: новости и розыгрыши.', channelUsername: channel, required: 1, rewardCoins: 2000, repeat: 'ONCE' })
    if (chat) tasks.push({ category: 'MAIN', type: 'CHANNEL_JOIN', titleEn: 'Join the BrunoPlay chat', titleRu: 'Вступите в чат BrunoPlay', descEn: 'Chat with other players.', descRu: 'Общайтесь с другими игроками.', channelUsername: chat, required: 1, rewardCoins: 2000, repeat: 'ONCE' })
    tasks.push(
      { category: 'MAIN', type: 'INVITE_FRIENDS', titleEn: 'Invite 3 friends', titleRu: 'Пригласите 3 друзей', descEn: 'A friend counts once they reach level 2.', descRu: 'Друг засчитывается, когда достигнет 2 уровня.', required: 3, rewardCoins: 3000, repeat: 'ONCE' },
      { category: 'MAIN', type: 'INVITE_FRIENDS', titleEn: 'Invite 10 friends', titleRu: 'Пригласите 10 друзей', descEn: 'A friend counts once they reach level 2.', descRu: 'Друг засчитывается, когда достигнет 2 уровня.', required: 10, rewardCoins: 10000, rewardStars: 5, repeat: 'ONCE' },
      { category: 'MAIN', type: 'INVITE_FRIENDS', titleEn: 'Invite 25 friends', titleRu: 'Пригласите 25 друзей', descEn: 'A friend counts once they reach level 2.', descRu: 'Друг засчитывается, когда достигнет 2 уровня.', required: 25, rewardCoins: 30000, rewardStars: 15, repeat: 'ONCE' },
      { category: 'OTHER', type: 'WATCH_ADS', titleEn: 'Watch 5 ads today', titleRu: 'Посмотрите 5 реклам сегодня', descEn: 'Come back every day.', descRu: 'Возвращайтесь каждый день.', required: 5, rewardCoins: 500, repeat: 'DAILY' },
    )
    for (const t of tasks) {
      const body = { ...t, descEn: t.descEn, rewardStars: t.rewardStars ?? 0, isActive: true, sortOrder: order++ }
      if (!apply) {
        console.log('would create task:', t.titleEn)
        continue
      }
      try {
        await api('POST', '/admin/tasks', token, body)
        console.log('task ', t.titleEn)
      } catch (e) {
        console.error('FAIL task', t.titleEn, String(e.message).slice(0, 200))
      }
    }
  }
}

// ---- what is still on you
const missing = (await api('GET', '/admin/settings/missing', token).catch(() => ({}))).missing ?? []
console.log('\nStill missing on the server' + (apply ? '' : ' (before applying)') + ':', missing.length ? '\n  ' + missing.join('\n  ') : ' nothing')
const todo = []
if (!settings['topup.ton.wallet_address']) todo.push('--wallet <your TON wallet>   (star top-ups with TON stay switched off without it)')
if (!settings['ads.block_ids']) todo.push('--block-id <Adsgram block id>   (or --block-ids with one per placement)')
if (!settings['links.support']) todo.push('--support / --news / --faq <links>   (optional, shown in the menu)')
if (todo.length) console.log('\nYou still have to give:\n  ' + todo.join('\n  '))
if (!apply) console.log('\nThis was a dry run. Add --apply to write the values.')
if (failed.length) process.exit(2)
