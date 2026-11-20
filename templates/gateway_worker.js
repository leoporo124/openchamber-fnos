(() => {
  const prefix = '/app/openchamber/fngateway';
  const NativeURL = self.URL;
  const resolveUrl = (value) => {
    const url = new NativeURL(String(value), self.location.href);
    if (url.origin === self.location.origin && url.pathname !== prefix && !url.pathname.startsWith(prefix + '/')) {
      url.pathname = prefix + url.pathname;
    }
    return url.href;
  };
  const showNotification = self.registration.showNotification.bind(self.registration);
  self.registration.showNotification = (title, options = {}) => showNotification(title, {
    ...options,
    ...(options.icon ? { icon: resolveUrl(options.icon) } : {}),
    ...(options.badge ? { badge: resolveUrl(options.badge) } : {})
  });
  const matchAll = self.clients.matchAll.bind(self.clients);
  self.clients.matchAll = async (options) => (await matchAll(options))
    .filter((client) => new NativeURL(client.url).pathname.startsWith(prefix + '/'))
    .map(client => new Proxy(client, {
      get(target, key) {
        if (key === 'navigate') return url => target.navigate(resolveUrl(url));
        if (key === 'postMessage') return (message, ...args) => target.postMessage(
          message?.type === 'openchamber:notification-navigation'
            ? { ...message, url: resolveUrl(message.url) } : message, ...args);
        const value = Reflect.get(target, key, target);
        return typeof value === 'function' ? value.bind(target) : value;
      }
    }));
  const openWindow = self.clients.openWindow.bind(self.clients);
  self.clients.openWindow = url => openWindow(resolveUrl(url));
})();
