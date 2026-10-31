// Prints what the economy in scripts/economy.mjs does: the exchange rates, what every game pays back, and how much money the
// project makes or loses per 1,000 new players in a good, an average and a bad case.
//
//   node scripts/economy-report.mjs [--ton-usd 1.24]
import { ASSUMPTIONS, PLAYERS, SCENARIOS, buildSettings, chestRtp, simulate, spinRtp } from './economy.mjs'

const arg = (name) => {
  const i = process.argv.indexOf(name)
  return i > 0 ? process.argv[i + 1] : undefined
}
const tonUsd = 1.16
const { settings: S, derived } = buildSettings({ tonUsd })
const usd = (x, d = 2) => (x < 0 ? '-' : '') + '$' + Math.abs(x).toLocaleString('en-US', { minimumFractionDigits: d, maximumFractionDigits: d })
const pct = (x) => (x * 100).toFixed(0) + '%'
const pad = (s, n) => String(s).padEnd(n)
const rpad = (s, n) => String(s).padStart(n)

console.log(`\n== Rates (1 TON = ${usd(tonUsd)})`)
console.log(`  1 TON = ${derived.coinsPerTon.toLocaleString()} coins   (1,000,000 coins = ${usd(1e6 * ASSUMPTIONS.coinUsd)})`)
console.log(`  1 TON = ${derived.starsPerTon} stars   (one star = ${usd(tonUsd / derived.starsPerTon, 4)} when bought with TON, +${ASSUMPTIONS.tonBonusPercent}% bonus)`)
console.log(`  one star = about ${Math.round(0.013 / ASSUMPTIONS.coinUsd).toLocaleString()} coins in value`)

console.log('\n== Fishing cards')
const m = S['cards.mining.levels']
const c = S['cards.capacity.levels']
console.log('  lvl  tank   speed/s  fill   mining upgrade        capacity upgrade')
for (let i = 0; i < 10; i++) {
  const fill = c[i].value / m[i].value / 3600
  const cost = (r) => (i === 0 ? '-' : `${r.cost.toLocaleString()} ${r.currency === 'COIN' ? 'coins' : 'stars'}`)
  console.log(`  ${rpad(i + 1, 3)}  ${rpad(c[i].value, 5)}  ${rpad(m[i].value, 7)}  ${rpad(fill.toFixed(1) + 'h', 5)}  ${pad(cost(m[i]), 20)}  ${cost(c[i])}`)
}
const starsToMax = [...m, ...c].filter((r) => r.currency === 'STAR').reduce((a, r) => a + r.cost, 0)
const coinsToL5 = [...m, ...c].filter((r) => r.currency === 'COIN').reduce((a, r) => a + r.cost, 0)
console.log(`  levels 1-5 cost ${coinsToL5.toLocaleString()} coins in total; levels 6-10 cost ${starsToMax.toLocaleString()} stars (${usd(starsToMax * 0.0142, 0)} of income) for both cards`)

console.log('\n== What the games give back (a good game gives back less than it takes)')
for (const t of S['spin.tiers']) {
  const r = spinRtp(S, t.tier)
  console.log(`  spin ${pad(t.name_en, 9)} price ${rpad(t.price_stars, 5)}  average win ${rpad(r.ev.toFixed(1), 7)} stars  gives back ${pct(r.rtp)}  (Heart or Frog: ${(r.jackpotChance * 100).toFixed(2)}% of spins)`)
}
const ch = chestRtp(S)
console.log(`  chest round price ${S['chest.round_cost_stars']}  average win ${ch.round.toFixed(1)} stars  gives back ${pct(ch.rtp)}`)
console.log(`  payout guard: a tier never pays more than ${pct(S['guard.max_payout_ratio'])} of what it took in over ${S['guard.window_hours']} h`)

console.log('\n== What a player can earn for free')
const dayIncome = (visits, bonus) => visits * c[0].value + S['streak.rewards'].reduce((a, b) => a + b, 0) / 10 + bonus * S['ads.bonus.coins'] + 3 * 0.8 * 700
console.log(`  a new player, 3 visits a day: about ${Math.round(dayIncome(3, 5)).toLocaleString()} coins a day (${usd(dayIncome(3, 5) * ASSUMPTIONS.coinUsd, 4)})`)
console.log(`  a committed player, 5 visits and 12 bonus ads: about ${Math.round(dayIncome(4.8, 12)).toLocaleString()} coins a day (${usd(dayIncome(4.8, 12) * ASSUMPTIONS.coinUsd, 4)})`)
console.log(`  first payout needs level ${S['withdrawal.min_level']}, ${S['withdrawal.min_account_days']} days and ${S['withdrawal.min_coins'].toLocaleString()} coins (${usd(S['withdrawal.min_coins'] * ASSUMPTIONS.coinUsd)}); the most one account can ever take out is ${S['withdrawal.total_limit_coins'].toLocaleString()} coins (${usd(S['withdrawal.total_limit_coins'] * ASSUMPTIONS.coinUsd)})`)

for (const name of ['base', 'worst', 'best', 'nobuy', 'nobuyWorst']) {
  const sc = SCENARIOS[name]
  const r = simulate(S, name)
  console.log(`\n== ${name.toUpperCase()} case: an ad earns ${usd(ASSUMPTIONS.adUsd[sc.adUsd], 4)}, buying is ${sc.buy}x, ${pct(sc.claim)} of won gifts are really sent, TON moves x${sc.ton}, ${pct(sc.farmShare)} are farm accounts`)
  if (name === 'base') {
    console.log(`  ${pad('player', 20)} ${rpad('share', 6)} ${rpad('ads', 8)} ${rpad('purchases', 10)} ${rpad('payouts', 9)} ${rpad('gifts', 8)}  first payout`)
    for (const row of r.rows) {
      console.log(`  ${pad(row.name, 20)} ${rpad(pct(row.share), 6)} ${rpad(usd(row.adUsd), 8)} ${rpad(usd(row.purchaseUsd), 10)} ${rpad(usd(row.payoutUsd), 9)} ${rpad(usd(row.giftCost), 8)}  ${row.firstWd ? 'day ' + row.firstWd : 'never'}   (level ${row.level}${row.lvl5 ? ', level 5 on day ' + row.lvl5 : ''})`)
    }
  }
  const p = r.per1000
  console.log(`  per 1,000 new players:  ads ${usd(p.adUsd, 0)}  +  star purchases ${usd(p.purchaseUsd, 0)}  =  income ${usd(r.revenue, 0)}`)
  console.log(`                          payouts to players ${usd(p.payoutUsd, 0)}  +  real gifts ${usd(p.giftCost, 0)}  +  free stars ${usd(p.freeStarCost, 0)}  =  cost ${usd(r.cost, 0)}`)
  console.log(`                          PROFIT ${usd(r.net, 0)}  (${pct(r.margin)} of income)`)
}

// the one-account worst case: someone who does everything right
const grinder = simulate(S, 'base').rows.find((x) => x.name.startsWith('Grinder'))
console.log(`\n== Worst single account: takes out ${S['withdrawal.total_limit_coins'].toLocaleString()} coins = ${usd(S['withdrawal.total_limit_coins'] * ASSUMPTIONS.coinUsd)} (at most ${usd(S['withdrawal.daily_limit_coins'] * ASSUMPTIONS.coinUsd)} a day). A grinder pays ${usd(grinder.adUsd)} in ads over 90 days.`)
console.log('')
