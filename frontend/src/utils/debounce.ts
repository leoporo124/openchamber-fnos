/**
 * 异步防并发锁定包装器：在 Promise 未完成前自动忽略后续重复点击
 */
export function withAsyncLock<T extends (...args: any[]) => Promise<any>>(
  fn: T
): (...args: Parameters<T>) => Promise<ReturnType<T> | void> {
  let isRunning = false
  return async function (this: any, ...args: Parameters<T>) {
    if (isRunning) return
    isRunning = true
    try {
      return await fn.apply(this, args)
    } finally {
      isRunning = false
    }
  }
}
