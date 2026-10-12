// TermPane.ts — goder 底栏终端面板(os-goder-term)。goder 桌面(wails)形态的
// 内置终端走本地 PTY、嵌入产物不含该模块;OpenOS 嵌入形态经 registry.bottom
// 扩展点注册本面板:接 OpenOS 真实终端通道(term:<user> 总线,服务端 sh -c
// 受控执行、10s 超时/64KB 截断),与「终端」应用同一通道、共享服务端跟踪
// 的工作目录(同用户多面板经 :out 广播互相同步提示符)。
//
// goder 底栏 dock 每次渲染会把当前会话项目 workspace 真实路径写入本元素
// 的 cwd 属性——面板据此静默 cd 跟随项目(cd 由服务端单独处理、回包无
// 输出文本,不污染屏幕;用户手动 cd 后若 dock 因其它状态变化重渲染会把
// 目录拉回项目根,这是「IDE 终端跟随项目」语义的取舍)。
//
// 自包含 shadow DOM 自定义元素:宿主文档样式进不来,样式全部内联在
// shadow 内;--goder-* / --os-mono 等 CSS 变量可穿透 shadow 边界继承。
import { bus } from '../../bus/bus'
import { api } from '../../api/client'
import { ansiToHtml } from '../terminal/ansi'

/** goder 底栏终端面板:cwd 属性由 goder 底栏 dock 推送(项目路径)。 */
export class GoderTermPane extends HTMLElement {
  /** OpenOS 用户名(创建后、挂载前由宿主 GoderStudio 写入) */
  user = ''
  /** 目标工作目录(dock 推送的项目 workspace;空 = 不主动跟随) */
  private desiredCwd = ''
  /** 服务端最近广播的工作目录(空命令与命令回包均携带) */
  private serverCwd = ''
  private home = ''
  private enabled = true
  private booted = false
  private history: string[] = []
  private histIdx = -1
  private offBus?: () => void
  private bodyEl?: HTMLElement
  private promptEl?: HTMLElement
  private inputEl?: HTMLInputElement

  constructor() {
    super()
    this.attachShadow({ mode: 'open' })
  }

  /** dock 写入 cwd('cwd' in el 探测依赖本访问器在原型上) */
  get cwd(): string { return this.desiredCwd }
  set cwd(v: string) {
    this.desiredCwd = v || ''
    this.follow()
  }

  connectedCallback(): void {
    this.renderSkeleton()
    this.addLine('OpenOS <b>终端</b>(受控执行:个人沙箱 · 10s 超时 · 64KB 截断)· 输入 help 查看提示', 'dim')
    if (!this.user) return
    this.offBus = bus.on(`term:${this.user}:out`, (p: any) => this.onOut(p))
    void this.probe()
  }

  disconnectedCallback(): void {
    this.offBus?.()
    this.offBus = undefined
    this.booted = false
  }

  /** 能力探测 + 拉取提示符(空命令回 cwd/home) */
  private async probe(): Promise<void> {
    try {
      const h = await api.get<{ realShell: boolean }>('/api/term/capability')
      this.enabled = h.realShell
      if (!h.realShell) {
        this.addLine('终端未启用:管理员可通过 OPENOS_REAL_SHELL=on(受限沙箱)/ =full(解除目录限制)开启。', 'dim')
        if (this.inputEl) this.inputEl.disabled = true
        return
      }
    } catch { /* 探测失败按可用处理,由服务端兜底提示 */ }
    this.booted = true
    bus.remote(`term:${this.user}`, '')
    this.follow()
  }

  /** 期望目录与服务端目录不一致时静默 cd 跟随(乐观置位防连推重复发送) */
  private follow(): void {
    if (!this.booted || !this.enabled || !this.desiredCwd) return
    if (this.desiredCwd === this.serverCwd) return
    this.serverCwd = this.desiredCwd
    bus.remote(`term:${this.user}`, `cd ${this.desiredCwd}`)
  }

  private onOut(p: any): void {
    if (!p) return
    if (typeof p.cwd === 'string' && p.cwd) this.serverCwd = p.cwd
    if (p.home) this.home = p.home
    this.renderPrompt()
    if (!p.text) return
    if (p.text === '\x00CLEAR') { this.clear(); return }
    for (const l of String(p.text).split('\n')) this.addLine(ansiToHtml(l), 'out')
    this.trim()
    this.scrollBottom()
  }

  // ---- 渲染 ----

  private renderSkeleton(): void {
    const root = this.shadowRoot!
    root.innerHTML = `
      <style>
        :host { display: flex; flex-direction: column; min-height: 0; min-width: 0;
          font-family: var(--goder-font, inherit); font-size: var(--goder-font-size, 13px);
          color: var(--goder-text, #e8e8ec); background: transparent; cursor: text; }
        .body { flex: 1; min-height: 0; overflow-y: auto; padding: 8px 10px 8px;
          font-family: var(--os-mono, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
          font-size: 12px; line-height: 1.55; }
        .body::-webkit-scrollbar { width: 9px; }
        .body::-webkit-scrollbar-thumb { background: var(--goder-scroll-thumb, rgba(255,255,255,.14)); border-radius: 5px; }
        .line { white-space: pre-wrap; word-break: break-all; }
        .out { color: var(--goder-text, #e8e8ec); }
        .echo { color: #7ecbff; }
        .dim { color: var(--goder-text-dim, #8a8a92); }
        .input-line { display: flex; gap: 8px; align-items: baseline; }
        .prompt { color: #63e6a2; flex: none; }
        .cmd { flex: 1; min-width: 0; background: transparent; border: none; outline: none;
          color: var(--goder-text, #fff); font: inherit; caret-color: #63e6a2; }
        .cmd:disabled { opacity: .5; }
      </style>
      <div class="body" part="body">
        <div class="input-line">
          <span class="prompt"></span>
          <input class="cmd" spellcheck="false" autocomplete="off" />
        </div>
      </div>`
    this.bodyEl = root.querySelector('.body')!
    this.promptEl = root.querySelector('.prompt')!
    this.inputEl = root.querySelector('input')!
    this.bodyEl.addEventListener('pointerup', () => this.inputEl?.focus())
    this.inputEl.addEventListener('keydown', (e) => this.onKey(e))
    this.renderPrompt()
  }

  /** 输入行恒在末尾:输出插到它前面 */
  private addLine(html: string, cls?: string): void {
    const div = document.createElement('div')
    div.className = 'line' + (cls ? ' ' + cls : '')
    div.innerHTML = html
    this.bodyEl?.insertBefore(div, this.bodyEl.lastElementChild)
  }

  private trim(): void {
    if (!this.bodyEl) return
    const lines = this.bodyEl.querySelectorAll('.line')
    if (lines.length > 2000) {
      for (let i = 0; i < lines.length - 1500; i++) lines[i].remove()
    }
  }

  private clear(): void {
    if (!this.bodyEl) return
    this.bodyEl.querySelectorAll('.line').forEach((n) => n.remove())
  }

  private scrollBottom(): void {
    if (this.bodyEl) this.bodyEl.scrollTop = this.bodyEl.scrollHeight
  }

  private promptText(): string {
    const short = this.home && this.serverCwd.startsWith(this.home)
      ? '~' + this.serverCwd.slice(this.home.length)
      : this.serverCwd || '~'
    return short
  }

  private renderPrompt(): void {
    if (this.promptEl) this.promptEl.textContent = this.promptText() + ' $'
  }

  // ---- 输入 ----

  private run(cmd: string): void {
    this.history.push(cmd)
    this.histIdx = -1
    this.addLine(`<span style="color:#7ecbff">${escHtml(this.promptText())} $ ${escHtml(cmd)}</span>`, 'echo')
    if (cmd.trim() === 'help') {
      this.addLine(ansiToHtml('\x1b[1m终端\x1b[0m:服务端 sh -c 受控执行。cd 由服务端跟踪(受限沙箱内),其余命令真实运行。'), 'dim')
      this.scrollBottom()
      return
    }
    bus.remote(`term:${this.user}`, cmd)
    this.scrollBottom()
  }

  private onKey(e: KeyboardEvent): void {
    const input = this.inputEl
    if (!input) return
    if (e.key === 'Enter') {
      const v = input.value
      input.value = ''
      if (v.trim()) this.run(v)
      e.preventDefault()
    } else if (e.key === 'Tab') {
      e.preventDefault() // 真实 shell 无前端补全,吞掉防焦点跳走
    } else if (e.key === 'ArrowUp') {
      if (!this.history.length) return
      this.histIdx = this.histIdx < 0 ? this.history.length - 1 : Math.max(0, this.histIdx - 1)
      input.value = this.history[this.histIdx]
      e.preventDefault()
    } else if (e.key === 'ArrowDown') {
      if (this.histIdx < 0) return
      this.histIdx++
      if (this.histIdx >= this.history.length) { this.histIdx = -1; input.value = '' }
      else input.value = this.history[this.histIdx]
      e.preventDefault()
    } else if (e.key === 'l' && e.ctrlKey) {
      this.clear()
      e.preventDefault()
    }
  }
}

function escHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

if (!customElements.get('os-goder-term')) customElements.define('os-goder-term', GoderTermPane)
