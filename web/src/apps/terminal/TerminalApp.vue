<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { api } from '../../api/client'
import { bus } from '../../bus/bus'
import { icons } from '../../ui'
import { ansiToHtml } from './ansi'

const props = defineProps<{ win: WinState }>()
const session = useSession()
const user = session.user!.username

type Line = { html: string; cls?: string }
const lines = ref<Line[]>([])
const input = ref('')
const cwd = ref('')
const home = ref('')          // 真实沙箱 home（~ 展开基准）
const enabled = ref(true)     // OPENOS_REAL_SHELL 能力（探测前乐观，失败按未启用处理）
const history = ref<string[]>([])
const histIdx = ref(-1)
const bodyEl = ref<HTMLElement>()
const inputEl = ref<HTMLInputElement>()

// 终端命令通道：发 term:<user>，收 term:<user>:out
const off = bus.on(`term:${user}:out`, (p: any) => onOut(p))
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
    enabled.value = h.realShell
    if (!h.realShell) {
      lines.value.push({ html: ansiToHtml('\x1b[1m终端未启用\x1b[0m：管理员可通过 OPENOS_REAL_SHELL=on（受限沙箱）/ =full（解除目录限制）开启。'), cls: 'dim' })
      return
    }
  } catch { /* 探测失败按可用处理，由服务端兜底提示 */ }
  // 挂载即拉取提示符（空命令回 cwd/home）
  bus.remote(`term:${user}`, '')
})
onBeforeUnmount(() => { off() })

function banner() {
  lines.value = [
    { html: 'OpenOS <b>终端</b>（受控执行：个人沙箱 · 10s 超时 · 64KB 截断）', cls: 'dim' },
    { html: '已连接事件总线 <span style="color:#7ecbff">term:' + user + '</span> · 输入 help 查看 shell 提示', cls: 'dim' },
  ]
  cwd.value = ''
}

function promptText(): string {
  const short = home.value && cwd.value.startsWith(home.value)
    ? '~' + cwd.value.slice(home.value.length)
    : cwd.value || '~'
  return short
}

function scroll() { nextTick(() => { if (bodyEl.value) bodyEl.value.scrollTop = bodyEl.value.scrollHeight }) }

function run() {
  const cmd = input.value
  input.value = ''
  history.value.push(cmd)
  histIdx.value = -1
  lines.value.push({ html: `<span style="color:#7ecbff">${esc(promptText())} $ ${esc(cmd)}</span>`, cls: 'echo' })
  if (cmd.trim() === 'clear') { lines.value = []; return }
  if (cmd.trim() === 'help') {
    lines.value.push({ html: ansiToHtml('\x1b[1m终端\x1b[0m：服务端 sh -c 受控执行。cd 由服务端跟踪（受限沙箱内），其余命令真实运行。'), cls: 'dim' })
    return
  }
  bus.remote(`term:${user}`, cmd)
  scroll()
}

function esc(s: string) { return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;') }

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter') { run(); e.preventDefault() }
  else if (e.key === 'Tab') { e.preventDefault() } // 真实 shell 无前端补全，吞掉防焦点跳走
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
    <!-- 融合窗体工具栏：标题（用户 — 终端）+ 状态提示 + 清屏；整条可拖动窗口（控件自身不参与） -->
    <div class="os-tb term-tb" data-os-drag>
      <div class="tb-title">{{ user }} — 终端</div>
      <span class="grow" />
      <span class="mode-hint">{{ enabled ? 'sh 受控执行 · 沙箱内' : '未启用' }}</span>
      <button class="os-tb-btn dark-btn" title="清屏（Ctrl+L）" v-html="icons.eraser" @click="lines = []" />
    </div>
    <div ref="bodyEl" class="body">
      <div v-for="(l, i) in lines" :key="i" class="line" :class="l.cls" v-html="l.html"></div>
      <div class="line input-line">
        <span class="prompt">{{ promptText() }} $</span>
        <input
          ref="inputEl"
          v-model="input"
          class="cmd os-flat"
          spellcheck="false"
          autofocus
          :disabled="!enabled"
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
/* 融合工具栏沿用终端深色（Terminal Pro 外观），避免亮色工具条压在黑底上 */
.term-tb {
  background: rgba(32, 32, 38, .92); border-bottom: 1px solid rgba(255, 255, 255, .08);
}
.term-tb .tb-title { color: #c8c8ce; font-size: 13px; font-weight: 600; }
.dark-btn {
  background: rgba(255, 255, 255, .1); border-color: rgba(255, 255, 255, .16); color: #d4d4da; font-size: 12px;
}
.dark-btn:hover { background: rgba(255, 255, 255, .16); }
.mode-hint { font-size: 11px; color: #8a8a92; white-space: nowrap; }
.grow { flex: 1; }
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
.cmd:disabled { opacity: .5; }
</style>
