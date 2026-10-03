// OpenOS 聊天室 —— 商店 bundle 应用 v1.1.1
//
// 形态约定：单文件 ES 模块，默认导出 Vue 组件定义（{ template, setup }），
// template 由 OpenOS 主程序的完整版 Vue 运行时编译；模块自身不 import 任何
// 依赖，全部能力经 window.OpenOS 注入：
//   OpenOS.vue   ref/computed/onMounted…（Vue 具名导出原样转发）
//   OpenOS.bus   事件总线（on/emit，与桌面同源）
//   OpenOS.api   REST 客户端（自动携带登录态；api.upload 走 multipart）
//   OpenOS.me()  当前用户（未登录为 null）
//   OpenOS.authToken()  登录令牌（拼 <img>/<a> 附件 URL 用）
//
// 消息推送走总线主题 chat:room：发送 = bus.emit（本地即时回显 + 服务端广播给
// 其他订阅者），发送者身份 from 由服务端强制注入不可伪造。在线状态：sys:presence
// 事件增量 + GET /api/presence 快照纠偏。应用未打开时的新消息由桌面壳常驻
// 监听并经通知中心提醒（desktop/appNotifier.ts），与 bundle 无关。
//
// 附件（v1.1.0）：先经 POST /api/attachments 上传（multipart，单文件 ≤10MB），
// 消息只带附件引用 {attach:{id,name,mime,size}}，接收方按需经
// /api/attachments/{id}?token= 拉取——总线消息不塞文件内容（WS 1MB 上限 +
// 广播给所有订阅者浪费）。图片白名单内联缩略图，其余（含 SVG，可携带脚本）
// 一律下载卡片，与服务端 attach.Inline 白名单一致。
const CSS = `
.oschat { display: flex; height: 100%; min-height: 0; font-size: 13px; color: var(--os-text); background: var(--os-panel-solid); }
.oschat-side { width: 168px; flex: none; border-right: 1px solid var(--os-border); padding: 12px 10px; overflow-y: auto; }
.oschat-side h4 { margin: 2px 6px 10px; font-size: 11px; font-weight: 600; color: var(--os-text-secondary); letter-spacing: .04em; }
.oschat-u { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 8px; }
.oschat-u:hover { background: var(--os-hover); }
.oschat-u.on { background: var(--os-selected); }
.oschat-u .dot { width: 8px; height: 8px; border-radius: 50%; background: #34c759; flex: none; box-shadow: 0 0 0 3px rgba(52,199,89,.18); }
.oschat-u span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.oschat-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.oschat-msgs { flex: 1; min-height: 0; overflow-y: auto; padding: 16px 18px 8px; scroll-behavior: smooth; }
.oschat-row { display: flex; gap: 10px; margin-bottom: 12px; }
.oschat-row.self { flex-direction: row-reverse; }
.oschat-ava { width: 30px; height: 30px; border-radius: 50%; flex: none; color: #fff; font-size: 13px; font-weight: 600; display: flex; align-items: center; justify-content: center; margin-top: 18px; }
.oschat-body { max-width: 68%; display: flex; flex-direction: column; }
.oschat-row.self .oschat-body { align-items: flex-end; }
.oschat-meta { font-size: 11px; color: var(--os-text-secondary); margin: 0 4px 4px; }
.oschat-bubble { padding: 8px 12px; border-radius: 14px; background: var(--os-input-bg); border: 1px solid var(--os-border); line-height: 1.5; word-break: break-word; white-space: pre-wrap; }
.oschat-bubble.oschat-only-attach { padding: 4px; background: transparent; border: none; }
.oschat-row.self .oschat-bubble { background: var(--os-accent); border-color: transparent; color: #fff; }
.oschat-row.self .oschat-bubble.oschat-only-attach { background: transparent; border: none; }
.oschat-img { display: block; max-width: 240px; max-height: 200px; border-radius: 10px; cursor: zoom-in; }
.oschat-file { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-radius: 10px; background: var(--os-input-bg); border: 1px solid var(--os-border); color: var(--os-text); text-decoration: none; max-width: 260px; }
.oschat-row.self .oschat-file { background: var(--os-accent); border-color: transparent; color: #fff; }
.oschat-file:hover { opacity: .85; }
.oschat-file .f-ico { flex: none; width: 26px; height: 26px; border-radius: 7px; background: rgba(125,125,125,.18); display: flex; align-items: center; justify-content: center; }
.oschat-file .f-ico svg { width: 14px; height: 14px; display: block; }
.oschat-file .f-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 500; }
.oschat-file .f-size { font-size: 11px; opacity: .7; flex: none; }
.oschat-row.sys { justify-content: center; margin: 4px 0 14px; }
.oschat-sysline { font-size: 11px; color: var(--os-text-secondary); background: var(--os-hover); padding: 3px 10px; border-radius: 999px; }
.oschat-input { flex: none; display: flex; gap: 8px; padding: 10px 12px calc(10px + var(--os-safe-b, 0px)); border-top: 1px solid var(--os-border); align-items: flex-end; }
.oschat-input textarea { flex: 1; resize: none; height: 38px; max-height: 96px; padding: 9px 12px; border: 1px solid var(--os-border); border-radius: 10px; background: var(--os-input-bg); color: var(--os-text); font: inherit; outline: none; box-sizing: border-box; }
.oschat-input textarea:focus { border-color: var(--os-accent); }
.oschat-tool { flex: none; width: 38px; height: 38px; border: 1px solid var(--os-border); border-radius: 10px; background: var(--os-input-bg); color: var(--os-text-secondary); cursor: pointer; display: flex; align-items: center; justify-content: center; }
.oschat-tool svg { width: 17px; height: 17px; display: block; }
.oschat-tool:hover { color: var(--os-text); }
.oschat-tool:disabled { opacity: .45; cursor: default; }
.oschat-tool.spin { animation: oschat-rot 1s linear infinite; }
@keyframes oschat-rot { to { transform: rotate(360deg); } }
.oschat-send { flex: none; padding: 0 18px; height: 38px; border: none; border-radius: 10px; background: var(--os-accent); color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
.oschat-send:hover { background: var(--os-accent-hover); }
.oschat-send:disabled { opacity: .5; cursor: default; }
@media (max-width: 560px) { .oschat-side { display: none; } }
`

// 与服务端 attach.Inline 白名单一致：仅这些 mime 内联显示（SVG 排除）
const IMAGE_MIMES = { 'image/png': 1, 'image/jpeg': 1, 'image/gif': 1, 'image/webp': 1, 'image/bmp': 1, 'image/avif': 1 }

export default {
  name: 'OsChatRoom',
  template: `
    <div class="oschat">
      <div class="oschat-side">
        <h4>在线 · {{ users.length }}</h4>
        <div v-for="u in users" :key="u" class="oschat-u" :class="{ on: u === me }">
          <span class="dot" /><span>{{ u }}{{ u === me ? '（我）' : '' }}</span>
        </div>
      </div>
      <div class="oschat-main">
        <div class="oschat-msgs" ref="listEl">
          <div v-if="!messages.length" class="oschat-row sys"><div class="oschat-sysline">还没有消息，说点什么吧</div></div>
          <div v-for="m in messages" :key="m.id" :class="['oschat-row', m.kind, m.kind === 'chat' && m.from === me ? 'self' : '']">
            <template v-if="m.kind === 'chat'">
              <div class="oschat-ava" :style="{ background: avaColor(m.from) }">{{ (m.from || '?').slice(0, 1).toUpperCase() }}</div>
              <div class="oschat-body">
                <div class="oschat-meta">{{ m.from }} · {{ fmtTime(m.ts) }}</div>
                <div v-if="m.attach && isImage(m.attach)" class="oschat-bubble oschat-only-attach">
                  <img class="oschat-img" :src="attachUrl(m.attach)" :alt="m.attach.name" @click="openAttach(m.attach)" />
                </div>
                <a v-else-if="m.attach" class="oschat-file" :href="attachUrl(m.attach)" :download="m.attach.name">
                  <span class="f-ico"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg></span>
                  <span class="f-name">{{ m.attach.name }}</span>
                  <span class="f-size">{{ fmtSize(m.attach.size) }}</span>
                </a>
                <div v-if="m.text" class="oschat-bubble">{{ m.text }}</div>
              </div>
            </template>
            <template v-else>
              <div class="oschat-sysline">{{ m.text }}</div>
            </template>
          </div>
        </div>
        <div class="oschat-input">
          <button class="oschat-tool" :class="{ spin: uploading }" :disabled="uploading" title="发送图片 / 文件" @click="pickFiles"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m21.44 11.05-9.19 9.19a6 6 0 0 1-8.49-8.49l8.57-8.57A4 4 0 1 1 18 8.84l-8.59 8.57a2 2 0 0 1-2.83-2.83l8.49-8.48"/></svg></button>
          <textarea v-model="input" placeholder="输入消息，Enter 发送" spellcheck="false" @keydown.enter.exact.prevent="send" />
          <button class="oschat-send" :disabled="!input.trim() || uploading" @click="send">发送</button>
        </div>
        <input ref="fileEl" type="file" multiple hidden @change="onFiles" />
      </div>
    </div>
  `,
  setup() {
    const OS = window.OpenOS
    const { ref, onMounted, onUnmounted, nextTick } = OS.vue

    const me = (OS.me && OS.me() && OS.me().username) || ''
    const messages = ref([]) // { id, kind: 'chat' | 'sys', from, text, ts, attach? }
    const users = ref([])    // 在线用户名（有序）
    const input = ref('')
    const listEl = ref(null)
    const fileEl = ref(null)
    const uploading = ref(false)

    let uid = 0
    let syncTimer = 0
    let firstSync = true
    // user → 连接计数：同一用户多标签页会产生多条连接，presence 事件按连接粒度
    // 上下线，计数归零才算真正离开
    const counts = new Map()

    const push = (m) => {
      messages.value.push(m)
      if (messages.value.length > 200) messages.value.splice(0, messages.value.length - 200)
      nextTick(() => { if (listEl.value) listEl.value.scrollTop = listEl.value.scrollHeight })
    }

    const normAttach = (a) => {
      if (!a || typeof a.id !== 'string' || a.id.length > 64 || !/^[A-Za-z0-9_-]+$/.test(a.id)) return null
      return { id: a.id, name: String(a.name || 'file').slice(0, 200), mime: String(a.mime || ''), size: Number(a.size) || 0 }
    }

    const onMsg = (p, from) => {
      if (!p || typeof p !== 'object') return
      const attach = normAttach(p.attach)
      const text = typeof p.text === 'string' ? p.text.slice(0, 500).trim() : ''
      if (!text && !attach) return
      push({ id: ++uid, kind: 'chat', from: from || '？', text, ts: Date.now(), attach })
    }

    const setOnline = (user, online) => {
      if (!user) return
      const known = counts.has(user)
      const n = (counts.get(user) || 0) + (online ? 1 : -1)
      if (n > 0) counts.set(user, n)
      else counts.delete(user)
      if (online && !known && user !== me) push({ id: ++uid, kind: 'sys', text: user + ' 来了', ts: Date.now() })
      if (!online && known && !counts.has(user) && user !== me) push({ id: ++uid, kind: 'sys', text: user + ' 离开了', ts: Date.now() })
      users.value = [...counts.keys()].sort()
    }

    // 快照纠偏：初次只做并集（与事件流有竞态，覆盖会丢刚上线的用户），此后周期覆盖
    const syncPresence = async () => {
      try {
        const d = await OS.api.get('/api/presence')
        const online = (d && d.online) || []
        for (const u of online) if (!counts.has(u)) counts.set(u, 1)
        if (!firstSync) {
          for (const u of [...counts.keys()]) if (!online.includes(u)) counts.delete(u)
        }
        firstSync = false
        users.value = [...counts.keys()].sort()
      } catch { /* 网络抖动等下次 */ }
    }

    const send = () => {
      const text = input.value.trim()
      if (!text || uploading.value) return
      // bus.emit 本地即时回显（from = 自己），服务端广播给其他订阅者
      OS.bus.emit('chat:room', { text: text.slice(0, 500) })
      input.value = ''
    }

    // ---- 附件：选择 → 上传 → 发带引用的消息 ----
    const pickFiles = () => { if (!uploading.value && fileEl.value) fileEl.value.click() }

    const onFiles = async () => {
      const files = fileEl.value ? [...fileEl.value.files] : []
      if (fileEl.value) fileEl.value.value = '' // 允许重选同一文件
      if (!files.length) return
      uploading.value = true
      try {
        for (const f of files) {
          if (f.size > 10 * 1024 * 1024) {
            OS.toast('「' + f.name + '」超过 10MB 限制', { type: 'error' })
            continue
          }
          try {
            const meta = await OS.api.upload('/api/attachments', f)
            OS.bus.emit('chat:room', { text: '', attach: meta })
          } catch (e) {
            OS.toast('「' + f.name + '」上传失败' + (e && e.message ? '：' + e.message : ''), { type: 'error' })
          }
        }
      } finally {
        uploading.value = false
      }
    }

    const attachUrl = (a) => '/api/attachments/' + a.id + '?token=' + encodeURIComponent(OS.authToken())
    const openAttach = (a) => { window.open(attachUrl(a), '_blank') }
    const isImage = (a) => !!IMAGE_MIMES[a.mime]

    const fmtTime = (ts) => {
      const d = new Date(ts)
      const p = (n) => (n < 10 ? '0' + n : '' + n)
      return p(d.getHours()) + ':' + p(d.getMinutes())
    }
    const fmtSize = (b) => {
      if (!b) return ''
      if (b < 1024) return b + ' B'
      if (b < 1048576) return (b / 1024).toFixed(1) + ' KB'
      return (b / 1048576).toFixed(1) + ' MB'
    }
    const AVA_COLORS = ['#0a84ff', '#5e5ce6', '#ff375f', '#ff9f0a', '#30d158', '#64d2ff', '#bf5af2', '#ff6482']
    const avaColor = (u) => {
      let h = 0
      for (let i = 0; i < u.length; i++) h = (h * 31 + u.charCodeAt(i)) >>> 0
      return AVA_COLORS[h % AVA_COLORS.length]
    }

    const offMsg = OS.bus.on('chat:room', onMsg)
    const offPresence = OS.bus.on('sys:presence', (p) => { if (p && p.user) setOnline(p.user, !!p.online) })

    onMounted(() => {
      if (!document.getElementById('oschat-style')) {
        const st = document.createElement('style')
        st.id = 'oschat-style'
        st.textContent = CSS
        document.head.appendChild(st)
      }
      syncPresence()
      syncTimer = setInterval(syncPresence, 60000)
    })
    onUnmounted(() => {
      offMsg()
      offPresence()
      if (syncTimer) clearInterval(syncTimer)
      // 样式标签常驻：多窗口实例共享，卸载单个窗口不摘除
    })

    return { me, messages, users, input, listEl, fileEl, uploading, send, pickFiles, onFiles, attachUrl, openAttach, isImage, fmtTime, fmtSize, avaColor }
  },
}
