import type { ApiResponse, RequestResult } from '../types/api'

export interface RequestOptions extends RequestInit {
  timeout?: number
}

class HttpClient {
  private readonly baseUrl: string

  constructor(baseUrl = 'api') {
    this.baseUrl = baseUrl.replace(/\/+$/, '')
  }

  private resolveUrl(path: string): string {
    const cleanPath = path.replace(/^\/+/, '')
    return `${this.baseUrl}/${cleanPath}`
  }

  private async request<T>(path: string, options: RequestOptions = {}): Promise<RequestResult<T>> {
    const { timeout = 30000, ...fetchOptions } = options
    const url = this.resolveUrl(path)

    const controller = new AbortController()
    const abort = () => controller.abort()
    fetchOptions.signal?.addEventListener('abort', abort, { once: true })
    if (fetchOptions.signal?.aborted) abort()
    const timer = setTimeout(() => controller.abort(), timeout)

    try {
      const response = await fetch(url, {
        ...fetchOptions,
        signal: controller.signal
      })

      const isJson = response.headers.get('content-type')?.includes('application/json')
      if (!isJson) {
        return {
          success: false,
          code: response.ok ? 502 : response.status,
          message: '管理接口未返回 JSON，请检查飞牛登录状态或网关连接'
        }
      }
      let body: unknown
      try {
        body = await response.json()
      } catch (error) {
        if (controller.signal.aborted) throw error
        return { success: false, code: 502, message: '管理接口返回的 JSON 无效，请重新连接后重试' }
      }
      if (!body || typeof body !== 'object' || !('code' in body) || !Number.isInteger(body.code) ||
        !('message' in body) || typeof body.message !== 'string') {
        return {
          success: false,
          code: response.ok ? 502 : response.status,
          message: body && typeof body === 'object' && 'message' in body && typeof body.message === 'string'
            ? body.message : '管理接口响应格式无效'
        }
      }
      const apiResp = body as ApiResponse<T>
      if (response.ok && apiResp.code === 0) {
        return { success: true, data: apiResp.data, message: apiResp.message, timestamp: apiResp.timestamp }
      }
      return {
        success: false,
        code: response.ok ? apiResp.code : response.status,
        message: apiResp.message || `请求失败 (HTTP ${response.status})`
      }
    } catch (err: unknown) {
      if (controller.signal.aborted) {
        return {
          success: false,
          code: 408,
          message: fetchOptions.signal?.aborted ? '请求已取消' : '网络请求超时，请检查服务状态'
        }
      }
      return {
        success: false,
        code: -1,
        message: (err as Error)?.message || '网络连接异常，请重试'
      }
    } finally {
      clearTimeout(timer)
      fetchOptions.signal?.removeEventListener('abort', abort)
    }
  }

  get<T>(path: string, options?: RequestOptions): Promise<RequestResult<T>> {
    return this.request<T>(path, { ...options, method: 'GET' })
  }

  post<T>(path: string, data?: unknown, options?: RequestOptions): Promise<RequestResult<T>> {
    const headers = new Headers(options?.headers)
    headers.set('Content-Type', 'application/json')
    return this.request<T>(path, {
      ...options,
      method: 'POST',
      headers,
      body: data !== undefined ? JSON.stringify(data) : undefined
    })
  }

  delete<T>(path: string, options?: RequestOptions): Promise<RequestResult<T>> {
    return this.request<T>(path, { ...options, method: 'DELETE' })
  }
}

export const http = new HttpClient()
