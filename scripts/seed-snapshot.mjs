// Save the settings and tasks of a running server to a file (the SNAPSHOT), and put them back on an empty server.
// The snapshot holds every setting that was set by hand in the admin panel (the Settings page, the Ads page, the gift catalog values)
// and the task list. It holds no secrets: the bot token, JWT secrets and the wallet words live in .env, never in the database.
// What it does NOT hold: admin accounts, players, daily combo images and patterns, notification templates, gift PICTURES.
//
//   ADMIN_PASSWORD='...' node scripts/seed-snapshot.mjs --export --api https://api.example.com --email you@example.com
//        reads the live server and writes scripts/seed-snapshot.json
//   ADMIN_PASSWORD='...' node scripts/seed-snapshot.mjs --api https://api.example.com --email you@example.com            (dry run: shows what it would do)
//   ADMIN_PASSWORD='...' node scripts/seed-snapshot.mjs --api https://api.example.com --email you@example.com --apply    (does it)
//
// Options: --file <path> use another snapshot file; --skip-tasks do not create the tasks.
// Safe to run again: it only writes a setting whose value differs, and it creates tasks only while the task list is empty.
// The admin password is read from the ADMIN_PASSWORD environment variable (never from an argument, never written to a file).
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

const DEFAULT_FILE = fileURLToPath(new URL('./seed-snapshot.json', import.meta.url))
const TASK_FIELDS = ['category', 'type', 'titleEn', 'titleRu', 'descEn', 'descRu', 'url', 'channelUsername', 'required', 'rewardCoins', 'rewardStars', 'repeat', 'startsAt', 'endsAt', 'isActive', 'sortOrder']

/** settings that were set by hand (the rest come back by themselves as defaults), without anything that no longer exists */
export function snapshotFrom(settingsList, tasks) {
  const settings = {}
  for (const s of settingsList) {
    if (!s.configured) continue
    let value = s.value
    // the ad provider TADS was removed from the game: a stored list that still names it is cleaned
    if (s.key === 'ads.providers' && Array.isArray(value)) value = value.filter((p) => p.id !== 'tads')
    settings[s.key] = value
  }
  const list = [...tasks]
    .sort((a, b) => (a.sortOrder ?? 0) - (b.sortOrder ?? 0) || a.id - b.id)
    .map((t) => Object.fromEntries(TASK_FIELDS.filter((f) => t[f] !== undefined).map((f) => [f, t[f]])))
  return { exportedAt: new Date().toISOString(), settings, tasks: list }
}

async function main() {
  const argv = process.argv.slice(2)
  const flag = (n) => argv.includes(n)
  const opt = (n) => (argv.indexOf(n) >= 0 ? argv[argv.indexOf(n) + 1] : undefined)
  const fail = (m) => {
    console.error('\n' + m + '\n')
    process.exit(1)
  }
  const base = (opt('--api') ?? '').replace(/\/+$/, '')
  const email = opt('--email')
  const password = process.env.ADMIN_PASSWORD
  const file = opt('--file') ?? DEFAULT_FILE
  const apply = flag('--apply')
  if (!base || !email) fail('usage: ADMIN_PASSWORD=... node scripts/seed-snapshot.mjs [--export] --api <https://api...> --email <admin email> [--apply]\n(see the top of this file)')
  if (!password) fail('Set the admin password in the ADMIN_PASSWORD environment variable.')

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
  const { accessToken: token } = await api('POST', '/admin/auth/login', '', { email, password })

  if (flag('--export')) {
    const snap = snapshotFrom((await api('GET', '/admin/settings', token)).settings ?? [], (await api('GET', '/admin/tasks', token)).tasks ?? [])
    writeFileSync(file, JSON.stringify(snap, null, 2) + '\n')
    console.log(`saved ${Object.keys(snap.settings).length} settings and ${snap.tasks.length} tasks to ${file}`)
    return
  }

  const snap = JSON.parse(readFileSync(file, 'utf8'))
  console.log(`Snapshot of ${snap.exportedAt}: ${Object.keys(snap.settings).length} settings, ${snap.tasks.length} tasks`)
  console.log(`Server: ${base}   mode: ${apply ? 'APPLY (writes)' : 'dry run (writes nothing)'}`)

  const current = (await api('GET', '/admin/settings', token)).settings ?? []
  const failed = []
  let same = 0
  let written = 0
  for (const [key, value] of Object.entries(snap.settings)) {
    const before = current.find((s) => s.key === key)
    if (before && JSON.stringify(before.value) === JSON.stringify(value)) {
      same++
      continue
    }
    if (!apply) {
      console.log('would set', key)
      written++
      continue
    }
    try {
      await api('PUT', `/admin/settings/${encodeURIComponent(key)}`, token, { value })
      written++
    } catch (e) {
      failed.push(key)
      console.error('FAIL ', key, String(e.message).slice(0, 240))
    }
  }
  console.log(`settings: ${same} already right, ${written} ${apply ? 'written' : 'would be written'}, ${failed.length} failed`)

  if (!flag('--skip-tasks')) {
    const have = (await api('GET', '/admin/tasks', token).catch(() => ({ tasks: [] }))).tasks ?? []
    if (have.length) console.log(`tasks: ${have.length} already exist, left alone`)
    else {
      for (const t of snap.tasks) {
        if (!apply) {
          console.log('would create task:', t.titleEn)
          continue
        }
        try {
          await api('POST', '/admin/tasks', token, t)
          console.log('task ', t.titleEn)
        } catch (e) {
          failed.push('task ' + t.titleEn)
          console.error('FAIL task', t.titleEn, String(e.message).slice(0, 240))
        }
      }
    }
  }

  if (apply) {
    // read everything back: what is on the server must now equal the snapshot
    const after = (await api('GET', '/admin/settings', token)).settings ?? []
    const wrong = Object.entries(snap.settings).filter(([k, v]) => JSON.stringify(after.find((s) => s.key === k)?.value) !== JSON.stringify(v)).map(([k]) => k)
    const missing = (await api('GET', '/admin/settings/missing', token).catch(() => ({}))).missing ?? []
    console.log(`\nverified: ${Object.keys(snap.settings).length - wrong.length} of ${Object.keys(snap.settings).length} settings equal the snapshot` + (wrong.length ? `; DIFFERENT: ${wrong.join(', ')}` : ''))
    console.log('settings still missing on the server:', missing.length ? missing.join(', ') : 'none')
    if (wrong.length) failed.push(...wrong)
  } else console.log('\nThis was a dry run. Add --apply to write.')
  if (failed.length) process.exit(2)
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main().catch((e) => fail2(e))
function fail2(e) {
  console.error('\n' + (e instanceof Error ? e.message : e) + '\n')
  process.exit(1)
}
