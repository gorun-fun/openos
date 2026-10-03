// OpenOS 聊天室 —— 商店 bundle 应用 v1.0.0
//
// 形态约定：单文件 ES 模块，默认导出 Vue 组件定义（{ template, setup }），
// template 由 OpenOS 主程序的完整版 Vue 运行时编译；模块自身不 import 任何
// 依赖，全部能力经 window.OpenOS 注入：
//   OpenOS.vue   ref/computed/onMounted…（Vue 具名导出原样转发）
//   OpenOS.bus   事件总线（on/emit，与桌面同源）
//   OpenOS.api   REST 客户端（自动携带登录态）
//   OpenOS.me()  当前用户（未登录为 null）
//
// 消息推送走总线主题 chat:room：发送 = bus.emit（本地即时回显 + 服务端广播给
// 其他订阅者），发送者身份 from 由服务端强制注入不可伪造；所有登录客户端默认
// 远程订阅 chat:*。在线状态：sys:presence 上/下线事件做增量（同一用户多标签
// 页按连接计数），GET /api/presence 快照做启动基线与周期纠偏。
// 消息不持久化，仅在线实时（应用卸载即无痕）。
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
.oschat-row.self .oschat-bubble { background: var(--os-accent); border-color: transparent; color: #fff; }
.oschat-row.sys { justify-content: center; margin: 4px 0 14px; }
.oschat-sysline { font-size: 11px; color: var(--os-text-secondary); background: var(--os-hover); padding: 3px 10px; border-radius: 999px; }
.oschat-input { flex: none; display: flex; gap: 8px; padding: 10px 12px calc(10px + var(--os-safe-b, 0px)); border-top: 1px solid var(--os-border); }
.oschat-input textarea { flex: 1; resize: none; height: 38px; max-height: 96px; padding: 9px 12px; border: 1px solid var(--os-border); border-radius: 10px; background: var(--os-input-bg); color: var(--os-text); font: inherit; outline: none; box-sizing: border-box; }
.oschat-input textarea:focus { border-color: var(--os-accent); }
.oschat-send { flex: none; padding: 0 18px; border: none; border-radius: 10px; background: var(--os-accent); color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
.oschat-send:hover { background: var(--os-accent-hover); }
.oschat-send:disabled { opacity: .5; cursor: default; }
@media (max-width: 560px) { .oschat-side { display: none; } }
`

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
                <div class="oschat-bubble">{{ m.text }}</div>
              </div>
            </template>
            <template v-else>
              <div class="oschat-sysline">{{ m.text }}</div>
            </template>
          </div>
        </div>
        <div class="oschat-input">
          <textarea v-model="input" placeholder="输入消息，Enter 发送" spellcheck="false" @keydown.enter.exact.prevent="send" />
          <button class="oschat-send" :disabled="!input.trim()" @click="send">发送</button>
        </div>
      </div>
    </div>
  `,
  setup() {
    const OS = window.OpenOS
    const { ref, onMounted, onUnmounted, nextTick } = OS.vue

    const me = (OS.me && OS.me() && OS.me().username) || ''
    const messages = ref([]) // { id, kind: 'chat' | 'sys', from, text, ts }
    const users = ref([])    // 在线用户名（有序）
    const input = ref('')
    const listEl = ref(null)

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

    const onMsg = (p, from) => {
      if (!p || typeof p.text !== 'string') return
      const text = p.text.slice(0, 500).trim()
      if (!text) return
      push({ id: ++uid, kind: 'chat', from: from || '？', text, ts: Date.now() })
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
      if (!text) return
      // bus.emit 本地即时回显（from = 自己），服务端广播给其他订阅者
      OS.bus.emit('chat:room', { text: text.slice(0, 500) })
      input.value = ''
    }

    const fmtTime = (ts) => {
      const d = new Date(ts)
      const p = (n) => (n < 10 ? '0' + n : '' + n)
      return p(d.getHours()) + ':' + p(d.getMinutes())
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

    return { me, messages, users, input, listEl, send, fmtTime, avaColor }
  },
}
