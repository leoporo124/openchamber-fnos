const candidateSelector = '[class~="group/server"] > button[aria-disabled="true"]'
const adaptedSelector = 'button[data-fnos-dev-server-url]'
const addressSelector = 'input[aria-controls="openchamber-browser-address-suggestions"]'

function isPrivateIPv4(hostname: string): boolean {
  if (!/^\d+\.\d+\.\d+\.\d+$/.test(hostname)) return false
  const [first, second] = hostname.split('.').map(Number)
  return first === 10 || (first === 172 && second >= 16 && second <= 31) || (first === 192 && second === 168)
}

function isDirectLanPage(): boolean {
  if (location.protocol !== 'http:' || !isPrivateIPv4(location.hostname)) return false
  try {
    // 飞牛网关可以内嵌 WebUI。要求各层都来自同一局域网入口，避免在中继或 HTTPS 页面中降级。
    for (let frame: Window = window; frame !== frame.parent; frame = frame.parent) {
      if (frame.parent.location.origin !== location.origin) return false
    }
    return true
  } catch {
    return false
  }
}

function findAddressInput(button: HTMLButtonElement): HTMLInputElement | null {
  // 查找当前浏览器面板的工具栏，避免命中其他会话或面板中的地址栏。
  for (let ancestor = button.parentElement; ancestor && ancestor !== document.body; ancestor = ancestor.parentElement) {
    const inputs = ancestor.querySelectorAll<HTMLInputElement>(addressSelector)
    if (inputs.length) return inputs.length === 1 ? inputs[0] : null
  }
  return null
}

function getLanUrl(button: HTMLButtonElement): string | null {
  // Space 的端口属于另一台机器，其条目有独立的标记。
  if (button.firstElementChild?.matches('[role="img"]')) return null
  const label = Array.from(button.children).find(child => child.matches('span.truncate'))?.textContent ?? ''
  const match = /^localhost:(\d{1,5})(\/.*)?$/.exec(label)
  if (!match) return null
  const port = Number(match[1])
  if (port < 1 || port > 65535) return null
  return `http://${location.hostname}:${port}${match[2] ?? '/'}`
}

export function installLanDevServerShortcuts(): void {
  if (!isDirectLanPage()) return

  const refresh = () => {
    document.querySelectorAll<HTMLButtonElement>(candidateSelector).forEach(button => {
      const url = getLanUrl(button)
      if (!url || !findAddressInput(button)?.form) return
      button.dataset.fnosDevServerUrl = url
      button.removeAttribute('aria-disabled')
      button.classList.remove('cursor-default', 'opacity-60')
      button.title = url
    })
  }
  let scheduled = false
  const observer = new MutationObserver(records => {
    if (scheduled || !records.some(record => record.type === 'attributes' || record.addedNodes.length)) return
    scheduled = true
    window.requestAnimationFrame(() => {
      scheduled = false
      refresh()
    })
  })
  observer.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ['aria-disabled'] })
  refresh()

  document.addEventListener('click', event => {
    if (!(event.target instanceof Element)) return
    const button = event.target.closest<HTMLButtonElement>(adaptedSelector)
    if (!button) return
    const url = getLanUrl(button)
    const input = findAddressInput(button)
    if (!url || !input?.form) return

    event.preventDefault()
    event.stopPropagation()
    // 调用原有地址表单，保留侧栏导航、标签和历史记录。使用原生 setter 让 React 收到 input 事件。
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, url)
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.form.requestSubmit()
  }, true)
}
