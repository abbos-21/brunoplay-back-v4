// Writes the economy in scripts/economy-answers.json (through scripts/economy.mjs) to a server through the admin API, and creates the starter tasks.
// Safe to run again: it only sets values, and it creates tasks only when the task list is empty.
//
//   ADMIN_PASSWORD='...' node scripts/production-seed.mjs --api https://api.example.com --email you@example.com            (shows what it would do)
//   ADMIN_PASSWORD='...' node scripts/production-seed.mjs --api https://api.example.com --email you@example.com --apply    (does it)
//
// Options
//   --wallet UQ...        the project TON wallet that receives star top-ups (nothing is written for it if you leave it out)
//   --block-id 12345      one Adsgram block id used for every ad placement, or
//   --block-ids '{"FISHING_START":"1","COLLECT":"2",...}'   one per placement (all 8 placements)
//   --support / --news / --faq <https link>       links shown in the menu
//   --skip-tasks          do not create the starter tasks
//   --only cards.,levels.  write only the settings whose names start with these prefixes (comma separated) and leave every other
//                         value, and the tasks, exactly as they are. Use it to ship one change without undoing what was set by hand.
//
// Values only you know (wallet, Adsgram blocks, links, channels) are never invented: whatever you do not pass is listed at the end.
import { buildSettings, starterTasks } from './economy.mjs'

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
  wallet: opt('--wallet'),
  blockIds,
  links: { support: opt('--support'), news: opt('--news'), faq: opt('--faq') },
})

console.log(`Rates from the answers file: 1 TON = ${derived.coinsPerTon.toLocaleString()} coins = ${derived.starsPerTon} stars`)
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

const only = (opt('--only') ?? '').split(',').map((p) => p.trim()).filter(Boolean)
const keys = Object.keys(settings).filter((k) => !only.length || only.some((p) => k.startsWith(p)))
if (only.length) console.log(`Only the settings starting with: ${only.join(', ')} (${keys.length} values); nothing else is touched`)
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
if (!flag('--skip-tasks') && !only.length) {
  const existing = await api('GET', '/admin/tasks', token).catch(() => ({ tasks: [] }))
  const have = existing.tasks ?? existing.items ?? []
  if (have.length) {
    console.log(`\nTasks: ${have.length} already exist, leaving them alone`)
  } else {
    const tasks = starterTasks()
    let order = 1
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
