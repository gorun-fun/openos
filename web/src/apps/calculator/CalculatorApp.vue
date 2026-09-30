<script setup lang="ts">
// 计算器（macOS 拟合）：圆形按键 / 亮暗双主题 / 显示字号随位数自适应 / ⌫ 删末位。
import { computed, ref } from 'vue'
import { useDesktop } from '../../stores/desktop'

const desktop = useDesktop()
const display = ref('0')
const acc = ref<number | null>(null)
const op = ref<string | null>(null)
const fresh = ref(true)

const keys = [
  ['AC', '±', '%', '÷'],
  ['7', '8', '9', '×'],
  ['4', '5', '6', '−'],
  ['1', '2', '3', '+'],
  ['0', '.', '='],
]

function apply(a: number, b: number, o: string): number {
  switch (o) {
    case '+': return a + b
    case '−': return a - b
    case '×': return a * b
    case '÷': return b === 0 ? NaN : a / b
    default: return b
  }
}

function fmt(n: number): string {
  if (!isFinite(n)) return '错误'
  const s = String(Math.round(n * 1e10) / 1e10)
  return s.length > 12 ? n.toExponential(6) : s
}

function press(k: string) {
  if (k >= '0' && k <= '9') {
    display.value = fresh.value || display.value === '0' ? k : display.value + k
    fresh.value = false
  } else if (k === '.') {
    if (fresh.value) { display.value = '0.'; fresh.value = false }
    else if (!display.value.includes('.')) display.value += '.'
  } else if (k === 'AC') {
    display.value = '0'; acc.value = null; op.value = null; fresh.value = true
  } else if (k === '⌫') {
    if (fresh.value) return
    display.value = display.value.length > 1 ? display.value.slice(0, -1) : '0'
    if (display.value === '-') display.value = '0'
  } else if (k === '±') {
    display.value = fmt(-parseFloat(display.value))
  } else if (k === '%') {
    display.value = fmt(parseFloat(display.value) / 100)
  } else if (k === '=') {
    if (op.value !== null && acc.value !== null) {
      display.value = fmt(apply(acc.value, parseFloat(display.value), op.value))
      acc.value = null; op.value = null; fresh.value = true
    }
  } else { // 运算符
    const cur = parseFloat(display.value)
    if (op.value !== null && acc.value !== null && !fresh.value) {
      acc.value = apply(acc.value, cur, op.value)
      display.value = fmt(acc.value)
    } else {
      acc.value = cur
    }
    op.value = k
    fresh.value = true
  }
}

function onKey(e: KeyboardEvent) {
  const map: Record<string, string> = {
    '/': '÷', '*': '×', '-': '−', '+': '+', '=': '=', Enter: '=', Escape: 'AC',
    Backspace: '⌫', Delete: 'AC', '%': '%',
  }
  const k = map[e.key] ?? (/[0-9.]/.test(e.key) ? e.key : null)
  if (k) { press(k); e.preventDefault() }
}

// 显示字号随位数收缩（macOS：超长数字自动变小）
const fontSize = computed(() => {
  const n = display.value.length
  if (n <= 8) return '44px'
  if (n <= 10) return '38px'
  if (n <= 12) return '32px'
  return '24px'
})
</script>

<template>
  <div class="calc" :class="{ dark: desktop.dark }" tabindex="0" @keydown="onKey">
    <div class="display" :style="{ fontSize }">{{ display }}</div>
    <div class="pad">
      <template v-for="(row, ri) in keys" :key="ri">
        <button
          v-for="k in row" :key="k"
          :class="{ op: ['÷', '×', '−', '+', '='].includes(k), fn: ['AC', '±', '%'].includes(k), zero: k === '0' }"
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
.display {
  flex: 1; display: flex; align-items: flex-end; justify-content: flex-end;
  padding: 10px 20px 8px; color: #1d1d1f; font-weight: 300;
  font-variant-numeric: tabular-nums; min-height: 72px; overflow: hidden; line-height: 1;
}
.calc.dark .display { color: #fff; }
.pad {
  display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; padding: 12px;
}
.pad button {
  height: 46px; border: none; border-radius: 999px; font-size: 18px;
  cursor: pointer; transition: filter .08s, transform .05s;
  background: #e3e3e8; color: #1d1d1f; font-family: inherit;
}
.calc.dark .pad button { background: #505054; color: #fff; }
.pad button:hover { filter: brightness(1.06); }
.pad button:active { filter: brightness(.82); transform: scale(.97); }
.pad .zero { grid-column: span 2; }
.pad .fn { background: #d4d4da; color: #1d1d1f; }
.calc.dark .pad .fn { background: #656569; color: #fff; }
.pad .op { background: #ff9f0a; color: #fff; font-size: 21px; }
</style>
