import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { setImmediate } from 'node:timers/promises'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'

const gatewayPrefix = '/app/openchamber/fngateway'
const origin = 'https://nas.example.test'
const source = readFileSync(new URL('../src/fnosBridge.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 }
})

function deferred() {
  let resolve, reject
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function createBridge({ standalone = false, response } = {}) {
  const connection = deferred()
  const requests = []
  const openedPaths = []
  const timers = new Map()
  let timerID = 0
  let readyCalls = 0
  const sdk = {
    ready() { readyCalls++; return connection.promise }
  }
  const trimSdk = {
    sdk,
    isStandaloneWeb: standalone,
    // 和服务封装保持一致：普通 ready() 会忽略初始化错误。
    async ready() { try { await sdk.ready() } catch {} },
    async openFileManager(path) { openedPaths.push(path); return { success: true } }
  }
  const window = {
    async fetch(...args) {
      requests.push(args)
      return response || Response.json({ path: '/vol1/projects/demo', directory: '/vol1/projects/demo' })
    },
    setTimeout(callback, delay) { const id = ++timerID; timers.set(id, { callback, delay }); return id },
    clearTimeout(id) { timers.delete(id) }
  }
  runInNewContext(outputText, {
    require(id) {
      if (id === './devServerShortcut') return { installLanDevServerShortcuts() {} }
      assert.equal(id, './utils/trimSdk')
      return { trimSdk }
    },
    exports: {}, window, document: { baseURI: `${origin}${gatewayPrefix}/` },
    location: new URL(origin), URL, Request, Response, Headers, Error
  })
  const reveal = (init = {}) => window.fetch(`${origin}${gatewayPrefix}/api/fs/reveal`, {
    method: 'POST', body: JSON.stringify({ path: '/vol1/projects/demo' }), ...init
  })
  return { window, reveal, connection, requests, openedPaths, timers, get readyCalls() { return readyCalls } }
}

test('页面加载即连接飞牛宿主，不等第一次点击，也不提前打开文件管理器', () => {
  const bridge = createBridge()
  assert.equal(bridge.readyCalls, 1)
  assert.equal(bridge.requests.length, 0)
  assert.equal(bridge.openedPaths.length, 0)
})

test('冷启动时首次定位等待同一个宿主连接，就绪后只打开一次', async () => {
  const bridge = createBridge()
  const pending = bridge.reveal()
  assert.equal(bridge.readyCalls, 1)
  assert.equal(bridge.requests.length, 0)
  assert.equal(bridge.openedPaths.length, 0)

  bridge.connection.resolve()
  const response = await pending
  assert.equal(response.status, 200)
  assert.deepEqual(await response.json(), { success: true, path: '/vol1/projects/demo' })
  assert.equal(bridge.requests.length, 1)
  assert.equal(bridge.requests[0][0].headers.get('X-OpenChamber-FnOS'), '1')
  assert.deepEqual(bridge.openedPaths, ['/vol1/projects/demo'])
  assert.equal(bridge.timers.size, 0)
})

test('连续定位共享页面启动时取得的就绪 Promise', async () => {
  const bridge = createBridge()
  bridge.connection.resolve()
  assert.equal((await bridge.reveal()).status, 200)
  assert.equal((await bridge.reveal()).status, 200)
  assert.equal(bridge.readyCalls, 1)
  assert.equal(bridge.openedPaths.length, 2)
})

test('宿主连接失败保留实际错误，不继续定位', async () => {
  const bridge = createBridge()
  bridge.connection.reject(new Error('host handshake failed'))
  // 初始化失败可能先于用户点击，不应产生未处理的 Promise rejection。
  await setImmediate()
  const response = await bridge.reveal()
  assert.equal(response.status, 502)
  assert.match((await response.json()).error, /host handshake failed/)
  assert.equal(bridge.requests.length, 0)
  assert.equal(bridge.openedPaths.length, 0)
})

test('首次等待超时后不会延迟打开，连接完成后无需重开 UI 即可重试', async () => {
  const bridge = createBridge()
  const pending = bridge.reveal()
  const timer = [...bridge.timers.values()][0]
  assert.ok(timer.delay >= 15000, '冷启动连接应比普通宿主操作留出更多时间')
  timer.callback()
  const response = await pending
  assert.equal(response.status, 502)
  assert.match((await response.json()).error, /超时/)
  assert.equal(bridge.timers.size, 0)

  bridge.connection.resolve()
  await setImmediate()
  assert.equal(bridge.requests.length, 0)
  assert.equal(bridge.openedPaths.length, 0)
  assert.equal((await bridge.reveal()).status, 200)
  assert.equal(bridge.openedPaths.length, 1)
})

test('独立浏览器仍拒绝调用飞牛文件管理器', async () => {
  const bridge = createBridge({ standalone: true })
  bridge.connection.resolve()
  assert.equal((await bridge.reveal()).status, 409)
  assert.equal(bridge.requests.length, 0)
  assert.equal(bridge.openedPaths.length, 0)
})

test('目录不存在等后端错误原样返回，不调用文件管理器', async () => {
  const bridge = createBridge({ response: Response.json({ error: '目标路径不存在' }, { status: 404 }) })
  bridge.connection.resolve()
  const response = await bridge.reveal()
  assert.equal(response.status, 404)
  assert.deepEqual(await response.json(), { error: '目标路径不存在' })
  assert.equal(bridge.openedPaths.length, 0)
})

test('文件定位使用后端返回的父目录，不使用请求中的文件路径', async () => {
  const bridge = createBridge({ response: Response.json({ path: '/vol1/demo/a.txt', directory: '/vol1/demo' }) })
  bridge.connection.resolve()
  const response = await bridge.reveal({ body: JSON.stringify({ path: '/vol1/demo/a.txt' }) })
  assert.equal(response.status, 200)
  assert.deepEqual(bridge.openedPaths, ['/vol1/demo'])
})

test('等待宿主期间已取消的请求不触发定位', async () => {
  const bridge = createBridge()
  const controller = new AbortController()
  const pending = bridge.reveal({ signal: controller.signal })
  controller.abort()
  bridge.connection.resolve()
  await assert.rejects(pending, { name: 'AbortError' })
  assert.equal(bridge.requests.length, 0)
  assert.equal(bridge.openedPaths.length, 0)
})

test('不匹配的路径、方法和外站请求仍走原始 fetch', async () => {
  const bridge = createBridge()
  for (const [url, init] of [
    [`${origin}${gatewayPrefix}/api/fs/reveal`, { method: 'GET' }],
    [`${origin}${gatewayPrefix}/api/sessions`, { method: 'POST' }],
    ['https://other.example.test/api/fs/reveal', { method: 'POST' }]
  ]) {
    await bridge.window.fetch(url, init)
    assert.deepEqual(bridge.requests.at(-1), [url, init])
  }
  assert.equal(bridge.openedPaths.length, 0)
})
