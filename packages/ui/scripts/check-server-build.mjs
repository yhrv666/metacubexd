import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { chromium } from 'playwright'

const root = new URL('../../../apps/server-webui/', import.meta.url)
const temp = await mkdtemp(join(tmpdir(), 'mcxd-browser-'))
const windows = process.platform === 'win32'
const panelPath = join(temp, windows ? 'panel.exe' : 'panel')
const fixturePath = join(temp, windows ? 'fixture.exe' : 'fixture')
const password = 'synthetic-admin-fixture'
const controller = 'synthetic-controller-fixture'
const perf = Number(process.env.MCXD_PERF_SECONDS || 0)
const children = []
let browser

function build(args) {
  const result = spawnSync('go', args, { cwd: root, stdio: 'inherit' })
  assert.equal(result.status, 0, 'build embedded Go test binary')
}

function start(path, args, env) {
  const child = spawn(path, args, {
    env: { ...process.env, ...env },
    stdio: 'inherit',
  })
  children.push(child)
  return child
}

async function port() {
  return new Promise((resolve, reject) => {
    const socket = createServer()
    socket.once('error', reject)
    socket.listen(0, '127.0.0.1', () => {
      const address = socket.address()
      socket.close((error) =>
        error ? reject(error) : resolve('127.0.0.1:' + address.port),
      )
    })
  })
}

async function waitUntil(check, message, timeout = 20_000) {
  const stop = Date.now() + timeout
  while (Date.now() < stop) {
    if (await check()) return
    await delay(150)
  }
  throw new Error(message)
}

async function waitHTTP(url) {
  await waitUntil(async () => {
    try {
      return (await fetch(url)).ok
    } catch {
      return false
    }
  }, 'local fixture did not start')
}

async function signIn(page, url) {
  await page.goto(url)
  await page.getByLabel('管理员密码').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByText('Mihomo v1.19.34', { exact: true }).waitFor()
}

async function kib(pid) {
  const text = await readFile('/proc/' + pid + '/status', 'utf8')
  return Number(text.match(/VmRSS:\s+(\d+) kB/)[1])
}

try {
  build(['test', '-c', '-o', fixturePath])
  if (perf) build(['build', '-trimpath', '-o', panelPath, '.'])
  const [fixtureAddr, panelAddr] = await Promise.all([port(), port()])
  const url = 'http://' + panelAddr
  const fixture = 'http://' + fixtureAddr
  start(fixturePath, ['-test.run', '^TestBrowserFixture$', '-test.timeout=0'], {
    MCXD_BROWSER_FIXTURE: fixtureAddr,
    MCXD_FIXTURE_CONNECTIONS: perf ? '5000' : '3',
  })
  await waitHTTP(fixture + '/ready')
  let app
  if (perf) {
    const adminFile = join(temp, 'admin')
    const controllerFile = join(temp, 'controller')
    await writeFile(adminFile, password, { mode: 0o600 })
    await writeFile(controllerFile, controller, { mode: 0o600 })
    app = start(panelPath, [
      '-listen',
      panelAddr,
      '-controller',
      fixture,
      '-admin-file',
      adminFile,
      '-controller-secret-file',
      controllerFile,
    ])
  } else {
    app = start(
      fixturePath,
      ['-test.run', '^TestBrowserFixture$', '-test.timeout=0'],
      {
        MCXD_BROWSER_FIXTURE: fixtureAddr,
        MCXD_BROWSER_PANEL: panelAddr,
      },
    )
  }
  await waitHTTP(url)
  const idle = perf ? await kib(app.pid) : 0
  if (perf) assert.ok(idle <= 64 * 1024, 'idle RSS exceeds 64 MiB')
  browser = await chromium.launch({
    args: [
      '--disable-background-timer-throttling',
      '--disable-renderer-backgrounding',
    ],
  })
  const context = await browser.newContext({
    locale: 'en-US',
    viewport: { width: 1440, height: 960 },
  })
  const initial = await context.newPage()
  assert.equal(
    (await context.request.get(url + '/api/monitor/version')).status(),
    401,
  )
  const frames = new Map()
  const track = (page) => {
    frames.set(page, 0)
    page.on('websocket', (socket) => {
      assert.ok(socket.url().startsWith('ws://' + panelAddr + '/api/monitor/'))
      assert.ok(!socket.url().includes('token='))
      if (socket.url().includes('/connections'))
        socket.on('framereceived', () => frames.set(page, frames.get(page) + 1))
    })
  }
  track(initial)
  await signIn(initial, url + '/#/connections')
  await initial.locator('.conn-data-row').first().waitFor()
  const pages = [initial]
  if (perf) {
    for (let i = 0; i < 4; i++) {
      const page = await context.newPage()
      track(page)
      pages.push(page)
    }
    await Promise.all(
      pages.slice(1).map(async (page) => {
        await page.goto(url + '/#/connections')
        await page.locator('.conn-data-row').first().waitFor()
      }),
    )
  }
  await fetch(fixture + '/interrupt', { method: 'POST' })
  await delay(2200)
  const before = pages.map((page) => frames.get(page))
  await waitUntil(
    () => pages.every((page, i) => frames.get(page) > before[i]),
    'connection view did not recover',
  )

  if (perf) {
    let peak = idle
    const startTime = Date.now()
    const counts = pages.map((page) => frames.get(page))
    while (Date.now() - startTime < perf * 1000) {
      peak = Math.max(peak, await kib(app.pid))
      assert.ok(peak <= 128 * 1024, 'loaded RSS exceeds 128 MiB')
      await delay(1000)
    }
    for (const [i, page] of pages.entries()) {
      assert.ok(
        frames.get(page) - counts[i] >= Math.max(1, perf * 0.85),
        'connection stream did not sustain the 1 Hz fixture',
      )
    }
    console.log(
      JSON.stringify({
        connections: 5000,
        tabs: 5,
        seconds: perf,
        idleKiB: idle,
        peakKiB: peak,
        reconnected: true,
      }),
    )
  } else {
    await initial.goto(url + '/#/logs')
    await initial
      .getByText(/using Fixture/)
      .first()
      .waitFor()
    await initial.goto(url + '/#/rules')
    await initial.getByText('Fixture', { exact: true }).first().waitFor()
    await initial.goto(url + '/#/setup')
    await initial.waitForURL('**/#/overview')
    assert.equal(
      (await context.request.get(url + '/api/monitor/configs')).status(),
      404,
    )
    assert.equal(
      await initial.evaluate(() =>
        navigator.serviceWorker.getRegistrations().then((list) => list.length),
      ),
      0,
    )
    await initial.getByRole('link', { name: '托管配置' }).click()
    await initial.getByRole('button', { name: '添加链路' }).click()
    await initial.getByLabel('链路 ID').fill('home')
    await initial.getByLabel('固定中转节点').fill('transit')
    await initial
      .getByLabel('代理字符串', { exact: true })
      .fill('socks5://fixture-user:fixture-password@residential.example:1080')
    await initial.getByRole('button', { name: '添加规则' }).click()
    await initial.getByLabel('目标链路').selectOption('home')
    await initial.getByLabel('OpenAI').check()
    await initial.getByRole('button', { name: '脱敏预览' }).click()
    await initial.getByRole('button', { name: '发布预览' }).click()
    await initial.getByText('发布完成，运行态已验证。').waitFor()
    const saved = await (
      await context.request.get(url + '/api/managed/state')
    ).text()
    assert.ok(
      saved.includes('home') &&
        !saved.includes('fixture-password') &&
        !saved.includes('residential.example'),
    )
    await initial.getByLabel('订阅 ID').fill('fixture')
    await initial
      .getByLabel('HTTPS 订阅地址')
      .fill('https://subscription.example/?value=synthetic-private')
    await initial.getByRole('button', { name: '新增或替换凭据' }).click()
    await initial.getByRole('button', { name: '刷新', exact: true }).click()
    await initial.waitForFunction(
      async () =>
        (await (await fetch('/api/managed/state')).json()).subscriptions?.[0]
          ?.refreshed,
    )
    const refreshed = await (
      await context.request.get(url + '/api/managed/state')
    ).text()
    assert.ok(
      refreshed.includes('openai') && !refreshed.includes('synthetic-private'),
    )
    await initial.getByLabel('回滚版本').selectOption({ index: 1 })
    await initial.getByRole('button', { name: '回滚并验证' }).click()
    await initial.getByText('回滚完成，运行态已验证。').waitFor()
    await initial.getByRole('button', { name: '退出', exact: true }).click()
    await initial.getByLabel('管理员密码').waitFor()
    assert.equal(
      (await context.request.get(url + '/api/monitor/version')).status(),
      401,
    )
    console.log(
      'Server browser monitor, publish, subscription refresh and rollback passed',
    )
  }
} finally {
  await browser?.close()
  await Promise.all(
    children.map(async (child) => {
      if (child.exitCode !== null || child.signalCode !== null) return
      await new Promise((resolve) => {
        child.once('exit', resolve)
        child.kill()
      })
    }),
  )
  await rm(temp, { recursive: true, force: true })
}
