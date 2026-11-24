(() => {
  const prefix = '/app/openchamber/fngateway';
  window.__OPENCHAMBER_API_BASE_URL__ = location.origin + prefix;

  function resolveGatewayUrl(value, isNavigation = false) {
    // 保留内嵌附件、临时文件和 SVG 片段引用。
    if (/^(?:data:|blob:|#)/i.test(String(value))) return String(value);
    const url = new URL(String(value), document.baseURI);
    const isLocal = ((url.protocol === 'http:' || url.protocol === 'https:') && url.origin === location.origin) ||
      ((url.protocol === 'ws:' || url.protocol === 'wss:') && url.host === location.host);
    const isAppPath = isNavigation || /^(?:\/api(?:\/|$)|\/assets\/|\/health$|\/auth(?:\/|$)|\/sw\.js$|\/manifest[^/]*$|\/(?:favicon[^/]*|apple-touch-icon[^/]*|icon-[^/]*))/.test(url.pathname);
    if (isLocal && isAppPath && url.pathname !== prefix && !url.pathname.startsWith(prefix + '/')) {
      url.pathname = prefix + url.pathname;
    }
    return url.href;
  }

  for (const method of ['pushState', 'replaceState']) {
    const nativeMethod = history[method].bind(history);
    history[method] = (data, unused, url) => nativeMethod(data, unused,
      url == null ? url : resolveGatewayUrl(url, true));
  }

  const nativeFetch = window.fetch;
  window.fetch = (input, options) => {
    if (input instanceof Request) {
      const url = resolveGatewayUrl(input.url);
      return nativeFetch(url === input.url ? input : new Request(url, input), options);
    }
    return nativeFetch(resolveGatewayUrl(input), options);
  };

  for (const name of ['WebSocket', 'EventSource', 'Worker']) {
    const Native = window[name];
    if (!Native) continue;
    window[name] = new Proxy(Native, {
      construct(target, args) {
        args[0] = resolveGatewayUrl(args[0]);
        return Reflect.construct(target, args);
      }
    });
  }

  const nativeOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (method, url, ...args) {
    return nativeOpen.call(this, method, resolveGatewayUrl(url), ...args);
  };

  for (const [prototype, field] of [
    [HTMLLinkElement.prototype, 'href'],
    [HTMLScriptElement.prototype, 'src'],
    [HTMLImageElement.prototype, 'src'],
    [HTMLSourceElement.prototype, 'src']
  ]) {
    const descriptor = Object.getOwnPropertyDescriptor(prototype, field);
    if (!descriptor?.set) continue;
    Object.defineProperty(prototype, field, {
      ...descriptor,
      set(value) { descriptor.set.call(this, resolveGatewayUrl(value)); }
    });
  }

  const nativeSetAttribute = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function (name, value) {
    const attribute = String(name).toLowerCase();
    const isUrl = (attribute === 'src' && /^(?:IMG|SCRIPT|SOURCE)$/.test(this.tagName)) ||
      (attribute === 'href' && this.tagName === 'LINK');
    return nativeSetAttribute.call(this, name, isUrl ? resolveGatewayUrl(value) : value);
  };

  // 网关页面使用固定子路径，避免上游根作用域 Service Worker 接管飞牛桌面。
  if (navigator.serviceWorker) {
    const register = navigator.serviceWorker.register.bind(navigator.serviceWorker);
    navigator.serviceWorker.register = (script, options = {}) =>
      register(resolveGatewayUrl(script), { ...options, scope: prefix + '/' });
  }
})();
