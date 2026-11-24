(() => {
  function checkStartup() {
    const root = document.getElementById('root');
    if (!root || (!root.querySelector('#initial-loading') && root.children.length > 0)) return;

    // Keep the recovery control outside React's root. A late mount can still replace its splash.
    const notice = document.createElement('div');
    notice.setAttribute('role', 'status');
    notice.style.cssText = 'position:fixed;z-index:2147483647;inset:auto 12px 12px;margin-inline:auto;max-width:30rem;padding:12px;border:1px solid GrayText;border-radius:8px;background:Canvas;color:CanvasText;font:14px/1.5 system-ui';
    const text = document.createElement('p');
    text.textContent = 'WebUI 尚未完成加载。可以重新加载，或在 OpenChamber Admin 查看服务状态和运行日志。';
    const reload = document.createElement('button');
    reload.type = 'button';
    reload.textContent = '重新加载';
    reload.addEventListener('click', () => location.reload());
    notice.append(text, reload);
    document.body.append(notice);

    const observer = new MutationObserver(() => {
      if (!root.querySelector('#initial-loading') && root.children.length > 0) {
        observer.disconnect();
        notice.remove();
      }
    });
    observer.observe(root, { childList: true });
  }

  // Run independently of the upstream module and its 10-second splash timeout.
  setTimeout(() => {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', checkStartup, { once: true });
    } else {
      checkStartup();
    }
  }, 12000);
})();
