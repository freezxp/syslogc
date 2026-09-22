// Captures screenshots of the main pages in mock mode and fails on console errors.
//
//   npm run screenshots            (starts `vite --mode mock` itself)
//   BASE_URL=http://127.0.0.1:5173 npm run screenshots   (use a running server)
import { spawn } from 'node:child_process'
import { mkdir } from 'node:fs/promises'
import { setTimeout as sleep } from 'node:timers/promises'

import { chromium } from '@playwright/test'

const port = 5174
let base = process.env.BASE_URL
let server
if (!base) {
  base = `http://127.0.0.1:${port}`
  server = spawn(
    'node',
    [
      new URL('../node_modules/vite/bin/vite.js', import.meta.url).pathname,
      '--mode',
      'mock',
      '--port',
      String(port),
      '--strictPort',
      '--host',
      '127.0.0.1',
    ],
    {
      stdio: 'ignore',
      detached: false,
    },
  )
  for (let i = 0; i < 60; i++) {
    try {
      const res = await fetch(base)
      if (res.ok) break
    } catch {
      // not up yet
    }
    await sleep(500)
  }
}

const outDir = new URL('../docs/screenshots/', import.meta.url).pathname
await mkdir(outDir, { recursive: true })

const browser = await chromium.launch()
const context = await browser.newContext({ viewport: { width: 1600, height: 960 }, deviceScaleFactor: 1 })
const page = await context.newPage()
const errors = []
page.on('console', (msg) => {
  if (msg.type() === 'error') errors.push(`${page.url()}: ${msg.text()}`)
})
page.on('pageerror', (err) => errors.push(`${page.url()}: ${err.message}`))

async function shot(name) {
  await page.waitForLoadState('networkidle').catch(() => {})
  await sleep(700)
  await page.screenshot({ path: `${outDir}${name}.png` })
  console.log(`captured ${name}`)
}

try {
  await page.goto(`${base}/login`)
  await page.getByRole('button', { name: /sign in/i }).waitFor()
  await shot('login')
  await page.getByRole('button', { name: /sign in/i }).click()
  await page.waitForURL(/\/dashboard/)
  await page.getByText('Top hosts').waitFor()
  await shot('dashboard')

  await page.goto(`${base}/logs?from=now-6h&to=now&q=${encodeURIComponent('app_name in (vpnd, firewall, sshd)')}`)
  await page.getByTestId('log-row').first().waitFor()
  await page.getByTestId('log-row').nth(2).click()
  await page.keyboard.press('Enter')
  await page.getByText('Additional fields').waitFor()
  await shot('explorer-detail')
  await page.keyboard.press('Escape')

  await page.goto(
    `${base}/logs?from=now-1h&to=now&mode=advanced&native=${encodeURIComponent('app_name:=nginx | stats by (hostname) count() errors')}`,
  )
  await page.getByTestId('logsql-editor').waitFor()
  await shot('explorer-logsql-table')

  await page.goto(`${base}/analytics?from=now-6h&to=now`)
  await page.getByTestId('breakdown-row').first().waitFor()
  await shot('analytics')

  await page.goto(`${base}/analytics?from=now-24h&to=now&group=hostname&metric=unique&mfield=source_ip&top=5`)
  await page.getByTestId('breakdown-row').first().waitFor()
  await shot('analytics-unique')

  await page.goto(`${base}/logs/live`)
  await page.getByTestId('tail-row').first().waitFor({ timeout: 10000 })
  await sleep(2500)
  await shot('live-tail')

  await page.goto(`${base}/system?tab=ingestion`)
  await page.getByText('Ingest queue').waitFor()
  await page.getByText('Forwarding').waitFor()
  await shot('system-ingestion')

  await page.goto(`${base}/searches`)
  await page.getByText('VPN Failures').waitFor()
  await shot('saved-searches')

  await page.goto(`${base}/sources`)
  await page.getByTestId('source-name').first().waitFor()
  await shot('sources')

  await page.getByTestId('source-name').filter({ hasText: 'edge-tls' }).click()
  await page.waitForURL(/\/sources\//)
  await page.getByLabel('Certificate file').waitFor()
  await shot('source-detail')

  await page.goto(`${base}/sources`)
  await page.getByTestId('source-name').filter({ hasText: 'branch-office' }).click()
  await page.waitForURL(/\/sources\//)
  await page.getByLabel('Pattern').first().waitFor()
  await page.getByText('Extract fields').evaluate((el) => el.scrollIntoView({ block: 'start' }))
  await shot('source-extract')

  // One line the first rule claims and one nothing matches, so both result
  // states are in the picture.
  await page
    .getByLabel('Sample lines')
    .fill(
      '2026-09-22T05:30:00.978892101Z dnsdist CLIENT_QUERY - 2001:db8:1:2::5 7248 INET6 UDP 81b siplb-1.ane2-prd.connectrcs.com A -\n' +
        'Accepted publickey for deploy from 10.20.4.9 port 51234 ssh2',
    )
  await page.getByRole('button', { name: /run test/i }).click()
  await page.getByTestId('extract-result').first().waitFor()
  await page.getByText('Sample lines').evaluate((el) => el.scrollIntoView({ block: 'center' }))
  await shot('source-extract-test')

  await page.goto(`${base}/users`)
  await page.getByText('Alice Chen').waitFor()
  await shot('users')

  await page.goto(`${base}/audit?from=now-24h&to=now`)
  await page.getByTestId('audit-row').first().waitFor()
  await page.getByTestId('audit-row').nth(1).click()
  await shot('audit')

  await page.goto(`${base}/settings`)
  await page.getByText('Changing retention').waitFor()
  await shot('settings-retention')

  await page.goto(`${base}/settings?tab=config`)
  await page.getByText('victorialogs:').waitFor()
  await shot('settings-config')
} finally {
  await browser.close()
  server?.kill()
}

const relevant = errors.filter((e) => !/Download the React DevTools/.test(e))
if (relevant.length) {
  console.error(`console errors:\n${relevant.join('\n')}`)
  process.exit(1)
}
console.log('no console errors')
