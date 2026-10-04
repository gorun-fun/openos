// OpenOS service worker：PWA 安装（可添加到主屏/桌面）+ 静态资源离线缓存。
//
// 策略刻意保守——本应用的动态数据全部在 API 与 WebSocket 上，离线缓存的价值只在
// 「外壳可打开」与静态资源加速：
//   - API / 总线 / 代理 一律网络直通（不响应 fetch 事件，绝不缓存动态数据）：
//     /api、/ws、/goder-api、/app、/vendor；
//   - 导航请求 network-first：优先拿最新外壳，离线时回退缓存副本（能打开登录页）；
//   - 静态资源（带 hash 的 /assets 产物、图标、manifest）stale-while-revalidate。
// 版本升级：改 CACHE 版本号即可在 activate 时清掉旧缓存。
const CACHE = 'openos-v1'
const PRECACHE = ['/', '/manifest.webmanifest', '/icons/icon-192.png', '/icons/icon-512.png']
const NO_CACHE = /^\/(api|ws|goder-api|app|vendor)(\/|$)/

self.addEventListener('install', (e) => {
  e.waitUntil(
    caches.open(CACHE).then((c) => c.addAll(PRECACHE)).then(() => self.skipWaiting())
  )
})

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  )
})

self.addEventListener('fetch', (e) => {
  const req = e.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)
  if (url.origin !== self.location.origin) return
  if (NO_CACHE.test(url.pathname)) return // 动态通道直通网络

  if (req.mode === 'navigate') {
    e.respondWith(
      fetch(req)
        .then((resp) => {
          const copy = resp.clone()
          caches.open(CACHE).then((c) => c.put('/', copy))
          return resp
        })
        .catch(() => caches.match('/'))
    )
    return
  }

  e.respondWith(
    caches.match(req).then((hit) => {
      const net = fetch(req)
        .then((resp) => {
          if (resp.ok) {
            const copy = resp.clone()
            caches.open(CACHE).then((c) => c.put(req, copy))
          }
          return resp
        })
        .catch(() => hit)
      return hit || net
    })
  )
})
