import { trimSdk } from './utils/trimSdk'

const gatewayPrefix = '/app/openchamber/fngateway'
const hostConnectTimeoutMs = 15000
const hostTimeoutMs = 8000
const revealPaths = new Set(['/api/fs/reveal', `${gatewayPrefix}/api/fs/reveal`])
const nativeFetch = window.fetch.bind(window)

// 在页面启动时就参与宿主握手，不能等到用户第一次点击定位才创建 SDK。
// 定位流程直接等待 SDK 的 ready，保留服务封装可能忽略的初始化错误。
const hostReady = trimSdk.sdk.ready()
// 连接可能先于用户操作失败；先标记为已处理，原 Promise 仍交给定位流程报错。
void hostReady.catch(() => {})

function revealError(status: number, message: string): Response {
  return Response.json({ error: message }, { status })
}

async function withHostTimeout<T>(operation: Promise<T>, timeoutMs = hostTimeoutMs): Promise<T> {
  let timer: number | undefined
  try {
    return await Promise.race([
      operation,
      new Promise<never>((_, reject) => {
        timer = window.setTimeout(() => reject(new Error('飞牛宿主响应超时，请稍后重试，并确认从飞牛桌面的 OpenChamber WebUI 图标进入')), timeoutMs)
      })
    ])
  } finally {
    window.clearTimeout(timer)
  }
}

async function revealInFnOS(request: Request): Promise<Response> {
  try {
    await withHostTimeout(hostReady, hostConnectTimeoutMs)
    if (trimSdk.isStandaloneWeb) {
      return revealError(409, '请从飞牛桌面的 OpenChamber WebUI 图标进入；独立浏览器页面无法调用飞牛文件管理器')
    }
    request.signal.throwIfAborted()
    const headers = new Headers(request.headers)
    headers.set('X-OpenChamber-FnOS', '1')
    const response = await nativeFetch(new Request(request, { headers }))
    if (!response.ok) return response
    const result = await response.json()
    if (typeof result.directory !== 'string' || !result.directory.startsWith('/')) {
      return revealError(502, '飞牛文件管理接口返回的目录无效')
    }
    request.signal.throwIfAborted()
    const opened = await withHostTimeout(trimSdk.openFileManager(result.directory))
    if (!opened.success) {
      return revealError(502, `打开飞牛文件管理器失败：${opened.message || '宿主未返回错误详情'}`)
    }
    return Response.json({ success: true, path: result.path })
  } catch (error) {
    if (request.signal.aborted) throw error
    return revealError(502, `打开飞牛文件管理器失败：${error instanceof Error ? error.message : '宿主未返回错误详情'}`)
  }
}

// 仅替换目录定位请求，其他请求保留官方处理流程。
window.fetch = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
  const url = new URL(input instanceof Request ? input.url : String(input), document.baseURI)
  const method = init?.method || (input instanceof Request ? input.method : 'GET')
  if (url.origin === location.origin && revealPaths.has(url.pathname.replace(/\/$/, '')) && method.toUpperCase() === 'POST') {
    return revealInFnOS(new Request(input, init))
  }
  return nativeFetch(input, init)
}
