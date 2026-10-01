<script setup lang="ts">
// 计算器（macOS 拟合）：圆形按键 / 亮暗双主题 / 副显示算式（按 = 或 Enter 才求值）/
// 括号支持（嵌套、隐式乘法、= 时自动闭合、未闭合时 ( 键橘黄高亮）/
// 打开即可键盘直输（挂载抢焦点 + 焦点回收）/ 键盘输入按键按压反馈 / 运算符选中高亮。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useDesktop } from '../../stores/desktop'

const desktop = useDesktop()
const cur = ref('0')                        // 主显示：当前输入数字或结果
const tokens = ref<Tok[]>([])               // 已确认算式（数字/运算符/括号），按 = 后以 '=' 收尾
const fresh = ref(true)                     // true=待输入新数字（再按数字将整体替换主显示）
const flashKey = ref<string | null>(null)   // 键盘输入命中的按键（按压视觉反馈）
const rootEl = ref<HTMLElement>()
let flashTimer: ReturnType<typeof setTimeout> | undefined

type Tok = number | '(' | ')' | '+' | '−' | '×' | '÷' | '='
const OPS = ['+', '−', '×', '÷']

const keys = [
  ['AC', '(', ')', '÷'],
  ['7', '8', '9', '×'],
  ['4', '5', '6', '−'],
  ['1', '2', '3', '+'],
  ['0', '.', '='],
]
const opKeys = ['÷', '×', '−', '+', '=']
const fnKeys = ['AC', '(', ')']

// 递归下降求值：括号 > 乘除 > 加减；除 0 得 NaN（显示"错误"）
function evalSeq(seq: Tok[]): number {
  let pos = 0
  function expr(): number {
    let v = term()
    while (seq[pos] === '+' || seq[pos] === '−') {
      const o = seq[pos++] as '+' | '−'
      const r = term()
      v = o === '+' ? v + r : v - r
    }
    return v
  }
  function term(): number {
    let v = factor()
    while (seq[pos] === '×' || seq[pos] === '÷') {
      const o = seq[pos++] as '×' | '÷'
      const r = factor()
      v = o === '×' ? v * r : r === 0 ? NaN : v / r
    }
    return v
  }
  function factor(): number {
    if (seq[pos] === '(') {
      pos++
      const v = expr()
      if (seq[pos] === ')') pos++
      return v
    }
    return seq[pos++] as number
  }
  return expr()
}

function fmt(n: number): string {
  if (!isFinite(n)) return '错误'
  const s = String(Math.round(n * 1e10) / 1e10)
  return s.length > 12 ? n.toExponential(6) : s
}

// 括号内子算式值（按 ) 闭合前 / ⌫ 删除前用于主显示）：
// withEnd=true 取最内层未闭合括号内全部（含刚收入的数字，不含新 ')'）；
// withEnd=false 取末位 ')' 括住的子序列（排除该 ')' 自身）。
function innerValue(withEnd: boolean): number {
  const ts = tokens.value
  let d = 0, i = ts.length - 1
  const excl = !withEnd && ts[i] === ')'
  if (excl) i--
  for (; i >= 0; i--) {
    const t = ts[i]
    if (t === ')') d++
    else if (t === '(') { if (d === 0) break; d-- }
  }
  return evalSeq(excl ? ts.slice(i + 1, -1) : ts.slice(i + 1))
}

function press(k: string) {
  const err = cur.value === '错误'
  const tail = () => tokens.value.at(-1)
  if (k >= '0' && k <= '9') {
    if (tail() === '=') tokens.value = []           // 结果后输入新数字：另起算式
    else if (tail() === ')') tokens.value.push('×') // 闭合后接数字 = 隐式乘法
    else if (fresh.value && typeof tail() === 'number') tokens.value.pop() // ⌫ 弹 ) 留下的镜像数字：新数替代
    cur.value = fresh.value || cur.value === '0' ? k : cur.value + k
    fresh.value = false
  } else if (k === '.') {
    if (err) return
    if (tail() === '=') tokens.value = []
    else if (tail() === ')') tokens.value.push('×')
    else if (fresh.value && typeof tail() === 'number') tokens.value.pop()
    if (fresh.value) { cur.value = '0.'; fresh.value = false }
    else if (!cur.value.includes('.')) cur.value += '.'
  } else if (k === 'AC') {
    cur.value = '0'; tokens.value = []; fresh.value = true
  } else if (k === '⌫') {
    backspace()
  } else if (k === '%') {
    if (err) return
    cur.value = fmt(parseFloat(cur.value) / 100)
  } else if (k === '(') {
    if (err) return
    if (tail() === '=') tokens.value = [parseFloat(cur.value), '×', '('] // 结果后接括号 = 结果 × (…)
    else if (!fresh.value) tokens.value.push(parseFloat(cur.value), '×', '(') // 数字后接括号 = 隐式乘法
    else if (tail() === ')') tokens.value.push('×', '(')
    else if (typeof tail() === 'number') { tokens.value.pop(); tokens.value.push('(') } // 丢弃镜像数字开新括号
    else tokens.value.push('(')
    cur.value = '0'; fresh.value = true
  } else if (k === ')') {
    if (err || openDepth.value <= 0) return
    const t = tail()
    if (t === '(' && fresh.value) return          // 空括号无效
    if (!fresh.value) tokens.value.push(parseFloat(cur.value))
    else if (t !== ')' && typeof t !== 'number') return // 运算符后直接闭合无效（如 (3+)）
    const inner = innerValue(true)                // 新 ')' 闭合前的括号内值
    tokens.value.push(')')
    cur.value = fmt(inner); fresh.value = true
  } else if (k === '=') {
    if (err || tokens.value.length === 0 || tail() === '=') return
    const seq = [...tokens.value]
    if (!fresh.value) seq.push(parseFloat(cur.value))
    else {
      const t = seq.at(-1)
      if (t === undefined) return                 // 空算式
      if (t === '(') seq.pop()                    // 末尾悬空 ( 丢弃
      else if (typeof t === 'string' && t !== ')') seq.push(parseFloat(cur.value)) // 末尾运算符=重复操作数
    }
    let d = 0
    for (const t of seq) { if (t === '(') d++; else if (t === ')') d-- }
    while (d-- > 0) seq.push(')')                 // 未闭合括号自动补齐
    const r = evalSeq(seq)
    tokens.value = [...seq, '=']
    cur.value = fmt(r)
    fresh.value = true
  } else { // 运算符：算式末尾追加（连按运算符 = 替换末位，macOS 同）
    if (err) return
    const v = parseFloat(cur.value)
    const t = tail()
    const op = k as Tok
    if (t === '=') tokens.value = [v, op]
    else if (!fresh.value) tokens.value.push(v, op)
    else if (t === ')' || typeof t === 'number') tokens.value.push(op) // ) 后或镜像数字：复用已有操作数
    else if (typeof t === 'string' && OPS.includes(t)) tokens.value[tokens.value.length - 1] = op
    else return                                    // 缺左操作数（开头或 ( 后直接按运算符）
    fresh.value = true
  }
}

// 退格：输入中删末位；数字删空（或待输入态）则回退算式——
// 弹出末 token 并把其内容带回主显示，可连续回退
function backspace() {
  if (!fresh.value) {
    if (cur.value.length > 1) {
      cur.value = cur.value.slice(0, -1)
      if (cur.value === '-') cur.value = '0'
      return
    }
    cur.value = '0'
  }
  const t = tokens.value.at(-1)
  if (t === undefined || t === '=') return
  if (t === ')') {
    const inner = innerValue(false)               // 末位 ')' 括住的值
    tokens.value.pop()
    cur.value = fmt(inner); fresh.value = true
  } else if (typeof t === 'number') {
    tokens.value.pop()
    cur.value = String(t); fresh.value = false    // 数字带回主显示，可继续删末位
  } else if (OPS.includes(t as string)) {
    tokens.value.pop()
    const prev = tokens.value.at(-1)
    cur.value = typeof prev === 'number' ? String(prev) : cur.value
    fresh.value = typeof prev !== 'number'        // 前面是数字则带回可编辑，否则待输入
  } else { // '('
    tokens.value.pop()
    cur.value = '0'; fresh.value = true
  }
}

function onKey(e: KeyboardEvent) {
  const map: Record<string, string> = {
    '/': '÷', '*': '×', x: '×', X: '×', '-': '−', '+': '+', '=': '=', Enter: '=',
    '(': '(', ')': ')', Escape: 'AC', Backspace: '⌫', Delete: 'AC', '%': '%', ',': '.',
  }
  const k = map[e.key] ?? (/[0-9.]/.test(e.key) ? e.key : null)
  if (!k) return
  e.preventDefault()
  flash(k)
  press(k)
}

// 键盘输入的按压反馈：长按（repeat）期间持续高亮，停按 150ms 后消失
function flash(k: string) {
  flashKey.value = k
  clearTimeout(flashTimer)
  flashTimer = setTimeout(() => (flashKey.value = null), 150)
}

// 焦点回收：打开即抢焦点；点击（含按钮）后焦点拉回根元素，保证键盘持续可用
function claimFocus(e?: Event) {
  if (!e || e.target !== rootEl.value) rootEl.value?.focus({ preventScroll: true })
}
// 点击窗口标题栏等外壳区域（不在 .calc 内）时也把键盘焦点带回；点击其他窗口/桌面不抢
function onDocPointerUp(e: PointerEvent) {
  const root = rootEl.value
  const t = e.target
  if (!root || !(t instanceof Node) || root.contains(t)) return
  const win = root.closest('.window')
  if (win?.contains(t)) root.focus({ preventScroll: true })
}
onMounted(() => {
  rootEl.value?.focus({ preventScroll: true })
  document.addEventListener('pointerup', onDocPointerUp)
})
onUnmounted(() => {
  clearTimeout(flashTimer)
  document.removeEventListener('pointerup', onDocPointerUp)
})

// 副显示算式（如 "12 + (34 ×"）；主显示随位数收缩字号（macOS：超长数字自动变小）
const exprText = computed(() =>
  tokens.value.map(t => (typeof t === 'number' ? fmt(t) : t)).join(' '))
const fontSize = computed(() => {
  const n = cur.value.length
  if (n <= 8) return '44px'
  if (n <= 10) return '38px'
  if (n <= 12) return '32px'
  return '24px'
})
// 未闭合括号数（( 键高亮提示用）
const openDepth = computed(() => {
  let d = 0
  for (const t of tokens.value) { if (t === '(') d++; else if (t === ')') d-- }
  return d
})
// 运算符选中态：已按运算符且尚未输入下一个数字时，该键保持高亮（macOS 白底橘字）
const pendingOp = computed(() => {
  const t = tokens.value.at(-1)
  return fresh.value && typeof t === 'string' && OPS.includes(t) ? t : null
})
</script>

<template>
  <div
    ref="rootEl" class="calc" :class="{ dark: desktop.dark }"
    tabindex="0" @keydown="onKey" @click="claimFocus" @focusin="claimFocus"
  >
    <div class="screen">
      <div class="expr">{{ exprText }}</div>
      <div class="display" :style="{ fontSize }">{{ cur }}</div>
    </div>
    <div class="pad">
      <template v-for="(row, ri) in keys" :key="ri">
        <button
          v-for="k in row" :key="k"
          :class="{ op: opKeys.includes(k), fn: fnKeys.includes(k), zero: k === '0', lit: pendingOp === k || (k === '(' && openDepth > 0), flash: flashKey === k }"
          @click="press(k)"
        >{{ k }}</button>
      </template>
    </div>
  </div>
</template>

<style scoped>
.calc {
  width: 100%; background: #f2f2f4; display: flex; flex-direction: column;
  outline: none; user-select: none;
}
/* 亮色：macOS 计算器浅色外观；暗色沿用经典深灰 */
.calc.dark { background: #1c1c1e; }
.screen {
  flex: 1; display: flex; flex-direction: column; align-items: flex-end;
  justify-content: flex-end; padding: 10px 20px 8px; min-height: 76px;
  overflow: hidden;
}
.expr {
  width: 100%; min-height: 18px; text-align: right; font-size: 13px;
  color: #86868b; white-space: nowrap; overflow: hidden;
  text-overflow: ellipsis; font-variant-numeric: tabular-nums;
}
.calc.dark .expr { color: rgba(255, 255, 255, .55); }
.display {
  color: #1d1d1f; font-weight: 300; line-height: 1;
  font-variant-numeric: tabular-nums; white-space: nowrap;
}
.calc.dark .display { color: #fff; }
.pad {
  display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; padding: 12px;
  --key: #e3e3e8; --key-fn: #d4d4da; --key-fg: #1d1d1f;
}
/* 暗色只换键面变量，避免高优先级元素选择器压过下方 fn/op/lit 的配色 */
.calc.dark .pad { --key: #505054; --key-fn: #656569; --key-fg: #fff; }
.pad button {
  height: 46px; border: none; border-radius: 999px; font-size: 18px;
  cursor: pointer; transition: filter .08s, transform .05s;
  background: var(--key); color: var(--key-fg); font-family: inherit;
}
.pad button:hover { filter: brightness(1.06); }
.pad button:active, .pad button.flash { filter: brightness(.82); transform: scale(.97); }
.pad .zero { grid-column: span 2; }
.pad .fn { background: var(--key-fn); }
.pad .op { background: #ff9f0a; color: #fff; font-size: 21px; }
/* 运算符选中态（macOS）：白底橘字，输入下一个数字后自动恢复 */
.pad .op.lit { background: #fff; color: #ff9f0a; }
/* 括号未闭合提示： ( 键转橘黄 */
.pad .fn.lit { background: #ff9f0a; color: #fff; }
</style>
