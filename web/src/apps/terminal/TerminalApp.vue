<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { useApps } from '../../stores/apps'
import { api } from '../../api/client'
import { bus } from '../../bus/bus'
import { ansiToHtml } from './ansi'

const props = defineProps<{ win: WinState }>()
const session = useSession()
const apps = useApps()
const user = session.user!.username

type Line = { html: string; cls?: string }
const lines = ref<Line[]>([])
const input = ref('')
const cwd = ref('')
const home = ref('')          // 真实沙箱 home（real 模式 ~ 展开）
const mode = ref<'virtual' | 'real'>('virtual')
const realAvailable = ref(true)
const history = ref<string[]>([])
const histIdx = ref(-1)
const bodyEl = ref<HTMLElement>()
const inputEl = ref<HTMLInputElement>()

// 双模式输出主题：term:<user>（虚拟）/ term:<user>:real（真实）
const outTopic = computed(() => (mode.value === 'real' ? `term:${user}:real:out` : `term:${user}:out`))
const inTopic = computed(() => (mode.value === 'real' ? `term:${user}:real` : `term:${user}`))

const offVirtual = bus.on(`term:${user}:out`, (p: any) => onOut(p))
const offReal = bus.on(`term:${user}:real:out`, (p: any) => onOut(p))
function onOut(p: any) {
  if (!p) return
  if (p.cwd !== undefined) cwd.value = p.cwd
  if (p.home) home.value = p.home
  if (!p.text) return
  if (p.text === '\x00CLEAR') { lines.value = []; return }
  for (const l of String(p.text).split('\n')) {
    lines.value.push({ html: ansiToHtml(l), cls: 'out' })
  }
  if (lines.value.length > 2000) lines.value = lines.value.slice(-1500)
  scroll()
}

onMounted(async () => {
  banner()
  try {
    const h = await api.get<{ realShell: boolean }>('/api/term/capability')
    realAvailable.value = h.realShell
    if (!h.realShell && mode.value === 'real') mode.value = 'virtual'
  } catch { /* 默认允许 */ }
})
onBeforeUnmount(() => { offVirtual(); offReal() })

function banner() {
  if (mode.value === 'real') {
    lines.value = [
      { html: 'OpenOS <b>真实终端</b>（受控执行：个人沙箱 · 10s 超时 · 64KB 截断）', cls: 'dim' },
      { html: '已连接事件总线 <span style="color:#7ecbff">term:' + user + ':real</span> · 输入 help 查看 shell 提示', cls: 'dim' },
    ]
  } else {
    lines.value = [
      { html: `OpenOS 虚拟终端 — 已通过事件总线连接 (term:${user})`, cls: 'dim' },
      { html: '输入 <b>help</b> 查看可用命令；右上角可切换<b>真实终端</b>。', cls: 'dim' },
    ]
  }
  cwd.value = mode.value === 'real' ? '' : `/home/${user}`
}

function switchMode(m: 'virtual' | 'real') {
  if (m === mode.value) return
  if (m === 'real' && !realAvailable.value) return
  mode.value = m
  banner()
  if (m === 'real') {
    // 切到真实模式立即拉取提示符（空命令回 cwd/home）
    bus.remote(inTopic.value, '')
  }
}

function promptText(): string {
  if (mode.value === 'real') {
    const short = home.value && cwd.value.startsWith(home.value)
      ? '~' + cwd.value.slice(home.value.length)
      : cwd.value || '~'
    return short
  }
  return cwd.value.replace('/home/' + user, '~') || '~'
}

function scroll() { nextTick(() => { if (bodyEl.value) bodyEl.value.scrollTop = bodyEl.value.scrollHeight }) }

function run() {
  const cmd = input.value
  input.value = ''
  history.value.push(cmd)
  histIdx.value = -1
  lines.value.push({ html: `<span style="color:#7ecbff">${esc(promptText())} $ ${esc(cmd)}</span>`, cls: 'echo' })
  if (cmd.trim() === 'clear') { lines.value = []; return }
  if (mode.value === 'real' && cmd.trim() === 'help') {
    lines.value.push({ html: ansiToHtml('\x1b[1m真实终端\x1b[0m：服务端 sh -c 受控执行。cd 由服务端跟踪（受限沙箱内），其余命令真实运行。'), cls: 'dim' })
    return
  }
  bus.remote(inTopic.value, cmd)
  scroll()
}

function esc(s: string) { return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;') }

const COMMANDS = ['help', 'pwd', 'ls', 'cd', 'cat', 'echo', 'touch', 'mkdir', 'rm', 'open', 'apps', 'goder', 'whoami', 'uname', 'date', 'clear']

/** Tab 补全：首词补命令，open 的第二个词补应用 id。 */
function complete() {
  const parts = input.value.split(' ')
  if (parts.length <= 1) {
    const matches = COMMANDS.filter((c) => c.startsWith(parts[0]))
    if (matches.length === 1) input.value = matches[0] + ' '
    else if (matches.length > 1) {
      lines.value.push({ html: esc(matches.join('  ')), cls: 'dim' })
      scroll()
    }
    return
  }
  if (parts[0] === 'open' && mode.value === 'virtual') {
    const ids = apps.apps.map((a) => a.id).filter((id) => id.startsWith(parts[parts.length - 1]))
    if (ids.length === 1) {
      parts[parts.length - 1] = ids[0]
      input.value = parts.join(' ')
    } else if (ids.length > 1) {
      lines.value.push({ html: esc(ids.join('  ')), cls: 'dim' })
      scroll()
    }
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter') { run(); e.preventDefault() }
  else if (e.key === 'Tab') { complete(); e.preventDefault() }
  else if (e.key === 'ArrowUp') {
    if (!history.value.length) return
    histIdx.value = histIdx.value < 0 ? history.value.length - 1 : Math.max(0, histIdx.value - 1)
    input.value = history.value[histIdx.value]
    e.preventDefault()
  } else if (e.key === 'ArrowDown') {
    if (histIdx.value < 0) return
    histIdx.value++
    if (histIdx.value >= history.value.length) { histIdx.value = -1; input.value = '' }
    else input.value = history.value[histIdx.value]
    e.preventDefault()
  } else if (e.key === 'l' && e.ctrlKey) {
    lines.value = []
    e.preventDefault()
  }
}
</script>

<template>
  <div class="term" @pointerup="inputEl?.focus()">
    <div class="term-head">
      <div class="mode-switch">
        <button :class="{ on: mode === 'virtual' }" @click="switchMode('virtual')">虚拟</button>
        <button
          :class="{ on: mode === 'real' }"
          :disabled="!realAvailable"
          :title="realAvailable ? '服务端受控执行（个人沙箱）' : '管理员已关闭真实终端'"
          @click="switchMode('real')"
        >真实</button>
      </div>
      <span class="mode-hint">{{ mode === 'real' ? '⣿ 服务端 sh 受控执行' : '⣿ 虚拟文件系统' }}</span>
    </div>
    <div ref="bodyEl" class="body">
      <div v-for="(l, i) in lines" :key="i" class="line" :class="l.cls" v-html="l.html"></div>
      <div class="line input-line">
        <span class="prompt">{{ promptText() }} $</span>
        <input
          ref="inputEl"
          v-model="input"
          class="cmd"
          spellcheck="false"
          autofocus
          @keydown="onKey"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.term {
  width: 100%; background: rgba(18, 18, 22, .96); color: #e8e8ec;
  font-family: var(--os-mono); font-size: 13px; display: flex; flex-direction: column;
  cursor: text;
}
html.dark .term { background: rgba(12, 12, 15, .98); }
.term-head {
  flex: none; display: flex; align-items: center; gap: 10px; padding: 5px 12px;
  border-bottom: 1px solid rgba(255,255,255,.08); background: rgba(255,255,255,.03);
}
.mode-switch { display: flex; gap: 2px; background: rgba(255,255,255,.08); border-radius: 7px; padding: 2px; }
.mode-switch button {
  border: none; background: transparent; color: #9a9aa2; font-size: 11.5px; padding: 2px 12px;
  border-radius: 5px; cursor: pointer; font-family: inherit;
}
.mode-switch button.on { background: #4a4a52; color: #fff; }
.mode-switch button:disabled { opacity: .35; cursor: not-allowed; }
.mode-hint { font-size: 11px; color: #8a8a92; }
.body { flex: 1; overflow-y: auto; padding: 10px 14px 12px; }
.line { white-space: pre-wrap; word-break: break-all; line-height: 1.55; }
.echo { color: #7ecbff; }
.out { color: #e8e8ec; }
.dim { color: #8a8a92; }
.input-line { display: flex; gap: 8px; align-items: baseline; }
.prompt { color: #63e6a2; flex: none; }
.cmd {
  flex: 1; background: transparent; border: none; color: #fff;
  font-family: var(--os-mono); font-size: 13px; caret-color: #63e6a2;
}
</style>
