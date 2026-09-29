<script setup lang="ts">
import { ref } from 'vue'

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
  const map: Record<string, string> = { '/': '÷', '*': '×', '-': '−', '+': '+', '=': '=', Enter: '=', Escape: 'AC', Backspace: 'AC' }
  const k = map[e.key] ?? (/[0-9.]/.test(e.key) ? e.key : null)
  if (k) { press(k); e.preventDefault() }
}
</script>

<template>
  <div class="calc" tabindex="0" @keydown="onKey">
    <div class="display">{{ display }}</div>
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
  width: 100%; background: #1c1c1e; display: flex; flex-direction: column;
  outline: none; user-select: none;
}
.display {
  flex: 1; display: flex; align-items: flex-end; justify-content: flex-end;
  padding: 8px 18px 6px; color: #fff; font-size: 44px; font-weight: 300;
  font-variant-numeric: tabular-nums; min-height: 70px; overflow: hidden;
}
.pad { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1px; background: #333; padding: 1px; }
.pad button {
  height: 52px; border: none; background: #505054; color: #fff; font-size: 19px;
  cursor: pointer; transition: filter .08s;
}
.pad button:hover { filter: brightness(1.15); }
.pad button:active { filter: brightness(.8); }
.pad .zero { grid-column: span 2; }
.pad .fn { background: #656569; color: #fff; }
.pad .op { background: #ff9f0a; font-size: 22px; }
</style>
