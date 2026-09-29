<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsToolbar, OsButton } from '../../ui'
import { COLS, ROWS, colName, cellName, evalCell, parseCSV, toCSV } from './engine'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()

const grid = ref<string[][]>(Array.from({ length: ROWS }, () => Array(COLS).fill('')))
// 选区：anchor 起点，focus 当前焦点（支持 shift 扩展的区域选择）
const anchor = ref({ r: 0, c: 0 })
const sel = ref({ r: 0, c: 0 })
const editing = ref(false)
const editInCell = ref(false)
const editVal = ref('')
const fileName = ref((props.win.props?.path || '').split('/').pop() || '未命名表格.csv')
const filePath = ref<string>(props.win.props?.path || '')
const cellEl = ref<HTMLInputElement>()
const fxEl = ref<HTMLInputElement>()
const rootEl = ref<HTMLElement>()

// 撤销 / 重做（快照栈，上限 50）
const undoStack = ref<string[]>([])
const redoStack = ref<string[]>([])
const snapshot = () => JSON.stringify(grid.value)
function pushUndo() {
  undoStack.value.push(snapshot())
  if (undoStack.value.length > 50) undoStack.value.shift()
  redoStack.value = []
}
function undo() {
  const s = undoStack.value.pop()
  if (!s) return
  redoStack.value.push(snapshot())
  grid.value = JSON.parse(s)
}
function redo() {
  const s = redoStack.value.pop()
  if (!s) return
  undoStack.value.push(snapshot())
  grid.value = JSON.parse(s)
}

function cell(r: number, c: number): string { return grid.value[r]?.[c] || '' }
const display = (r: number, c: number) => evalCell(cell(r, c), (rr, cc) => cell(rr, cc))

// 选区矩形（含单格）
const range = computed(() => ({
  r1: Math.min(anchor.value.r, sel.value.r),
  r2: Math.max(anchor.value.r, sel.value.r),
  c1: Math.min(anchor.value.c, sel.value.c),
  c2: Math.max(anchor.value.c, sel.value.c),
}))
const inRange = (r: number, c: number) =>
  r >= range.value.r1 && r <= range.value.r2 && c >= range.value.c1 && c <= range.value.c2

const rangeName = computed(() => {
  const { r1, r2, c1, c2 } = range.value
  if (r1 === r2 && c1 === c2) return cellName(r1, c1)
  return `${cellName(r1, c1)}:${cellName(r2, c2)}`
})

// 选区数值合计（状态栏实时显示）
const rangeStats = computed(() => {
  const nums: number[] = []
  for (let r = range.value.r1; r <= range.value.r2; r++) {
    for (let c = range.value.c1; c <= range.value.c2; c++) {
      const v = display(r, c)
      if (v !== '' && !isNaN(Number(v))) nums.push(Number(v))
    }
  }
  if (!nums.length) return ''
  const sum = nums.reduce((s, v) => s + v, 0)
  const parts = [`计数 ${nums.length}`, `求和 ${Math.round(sum * 1e10) / 1e10}`]
  if (nums.length > 1) parts.push(`平均 ${Math.round((sum / nums.length) * 1e10) / 1e10}`)
  return parts.join(' · ')
})

const selRaw = computed(() => cell(sel.value.r, sel.value.c))

onMounted(async () => {
  if (filePath.value) {
    try {
      const data = await fsApi.read(filePath.value)
      loadCSV(data.content)
    } catch { /* 新表 */ }
  }
  windows.setTitle(props.win.id, fileName.value + ' — 表格')
  rootEl.value?.addEventListener('paste', onPaste)
})
onBeforeUnmount(() => rootEl.value?.removeEventListener('paste', onPaste))

function loadCSV(text: string) {
  const rows = parseCSV(text)
  grid.value = Array.from({ length: ROWS }, (_, r) =>
    Array.from({ length: COLS }, (_, c) => rows[r]?.[c] ?? ''))
}

function clickCell(r: number, c: number, extend = false) {
  if (extend) {
    sel.value = { r, c } // anchor 不变 → 扩展选区
  } else {
    anchor.value = { r, c }
    sel.value = { r, c }
  }
  editing.value = false
}
function dblClickCell(r: number, c: number) {
  anchor.value = { r, c }
  sel.value = { r, c }
  startEdit(cell(r, c))
}
function startEdit(v: string, inCell = true) {
  editing.value = true
  editInCell.value = inCell
  editVal.value = v
  requestAnimationFrame(() => {
    const el = inCell ? cellEl.value : fxEl.value
    el?.focus()
    el?.select()
  })
}

function commitEdit() {
  if (!editing.value) return
  pushUndo()
  grid.value[sel.value.r][sel.value.c] = editVal.value
  editing.value = false
}

function onGridKey(e: KeyboardEvent) {
  if (editing.value) {
    if (e.key === 'Escape') editing.value = false
    return
  }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'z') { e.shiftKey ? redo() : undo(); e.preventDefault(); return }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'y') { redo(); e.preventDefault(); return }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'c') { copyRange(); e.preventDefault(); return }
  const { r, c } = sel.value
  const move = (nr: number, nc: number, extend: boolean) => {
    nr = Math.max(0, Math.min(ROWS - 1, nr))
    nc = Math.max(0, Math.min(COLS - 1, nc))
    sel.value = { r: nr, c: nc }
    if (!extend) anchor.value = { r: nr, c: nc }
  }
  switch (e.key) {
    case 'Enter': startEdit(cell(r, c)); e.preventDefault(); break
    case 'F2': startEdit(cell(r, c)); e.preventDefault(); break
    case 'ArrowUp': move(r - 1, c, e.shiftKey); e.preventDefault(); break
    case 'ArrowDown': move(r + 1, c, e.shiftKey); e.preventDefault(); break
    case 'ArrowLeft': move(r, c - 1, e.shiftKey); e.preventDefault(); break
    case 'ArrowRight': move(r, c + 1, e.shiftKey); e.preventDefault(); break
    case 'Home': move(r, 0, e.shiftKey); e.preventDefault(); break
    case 'Delete': case 'Backspace': pushUndo(); for (let rr = range.value.r1; rr <= range.value.r2; rr++) for (let cc = range.value.c1; cc <= range.value.c2; cc++) grid.value[rr][cc] = ''; e.preventDefault(); break
    default:
      if (e.key.length === 1 && !e.ctrlKey && !e.metaKey) startEdit(e.key)
  }
}

// ---- 剪贴板（与 Excel / Numbers 互通 TSV） ----
function copyRange() {
  const rows: string[] = []
  for (let r = range.value.r1; r <= range.value.r2; r++) {
    const cols: string[] = []
    for (let c = range.value.c1; c <= range.value.c2; c++) cols.push(display(r, c))
    rows.push(cols.join('\t'))
  }
  navigator.clipboard?.writeText(rows.join('\n')).catch(() => {})
}

function onPaste(e: ClipboardEvent) {
  if (editing.value) return // 编辑态交给原生输入框
  const text = e.clipboardData?.getData('text/plain')
  if (!text) return
  e.preventDefault()
  const rows = text.replace(/\r/g, '').split('\n').filter((l, i, arr) => l !== '' || i < arr.length - 1)
  pushUndo()
  rows.forEach((row, dr) => {
    row.split('\t').forEach((v, dc) => {
      const r = sel.value.r + dr, c = sel.value.c + dc
      if (r < ROWS && c < COLS) grid.value[r][c] = v
    })
  })
}

// ---- 行列操作 ----
function insertRow() {
  pushUndo()
  const at = range.value.r1
  grid.value.splice(at, 0, Array(COLS).fill(''))
  grid.value.length = ROWS
}
function insertCol() {
  pushUndo()
  const at = range.value.c1
  for (const row of grid.value) row.splice(at, 0, '')
  for (const row of grid.value) row.length = COLS
}
function deleteRow() {
  pushUndo()
  grid.value.splice(range.value.r1, range.value.r2 - range.value.r1 + 1)
  while (grid.value.length < ROWS) grid.value.push(Array(COLS).fill(''))
}
function deleteCol() {
  pushUndo()
  const n = range.value.c2 - range.value.c1 + 1
  for (const row of grid.value) row.splice(range.value.c1, n)
  for (const row of grid.value) while (row.length < COLS) row.push('')
}

async function save() {
  if (!filePath.value) {
    const name = prompt('保存为（CSV 文件名）', fileName.value)
    if (!name) return
    fileName.value = name.endsWith('.csv') ? name : name + '.csv'
    filePath.value = `/home/${session.user!.username}/表格/${fileName.value}`
  }
  await fsApi.write(filePath.value, toCSV(grid.value))
  windows.setTitle(props.win.id, fileName.value + ' — 表格')
  bus.emit('fs:changed', { path: filePath.value, kind: 'write' }, { remote: false })
}

function exportCSV() {
  const blob = new Blob(['\ufeff' + toCSV(grid.value)], { type: 'text/csv' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName.value.endsWith('.csv') ? fileName.value : fileName.value + '.csv'
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <div ref="rootEl" class="sheets" tabindex="0" @keydown="onGridKey">
    <OsToolbar>
      <span class="cellpos mono">{{ rangeName }}</span>
      <span class="sep" />
      <input v-if="editing && !editInCell" ref="fxEl" v-model="editVal" class="fx mono" spellcheck="false"
        @keydown.enter.prevent="commitEdit" @keydown.esc="editing = false" @blur="commitEdit" />
      <input v-else class="fx mono" :value="selRaw" placeholder="点击单元格或直接输入公式，如 =SUM(A1:A5)" readonly @pointerdown="startEdit(selRaw, false)" />
      <span class="sep" />
      <button title="撤销（Ctrl+Z）" @click="undo">↩</button>
      <button title="重做（Ctrl+Y）" @click="redo">↪</button>
      <span class="sep" />
      <button title="在上方插入行" @click="insertRow">＋行</button>
      <button title="在左侧插入列" @click="insertCol">＋列</button>
      <button title="删除选中行" @click="deleteRow">－行</button>
      <button title="删除选中列" @click="deleteCol">－列</button>
      <span class="grow" />
      <OsButton size="sm" @click="exportCSV">导出 CSV</OsButton>
      <OsButton size="sm" variant="primary" @click="save">{{ filePath ? '保存' : '保存到文件' }}</OsButton>
    </OsToolbar>

    <div class="grid-wrap">
      <table class="grid">
        <thead>
          <tr>
            <th class="corner"></th>
            <th v-for="c in COLS" :key="c" :class="{ selc: c - 1 >= range.c1 && c - 1 <= range.c2 }">{{ colName(c - 1) }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in ROWS" :key="r">
            <th :class="{ selr: r - 1 >= range.r1 && r - 1 <= range.r2 }">{{ r }}</th>
            <td
              v-for="c in COLS" :key="c"
              :class="{
                sel: sel.r === r - 1 && sel.c === c - 1,
                inrange: inRange(r - 1, c - 1) && !(sel.r === r - 1 && sel.c === c - 1),
                num: !isNaN(Number(display(r - 1, c - 1))) && display(r - 1, c - 1) !== '',
              }"
              @click="clickCell(r - 1, c - 1, $event.shiftKey)"
              @dblclick="dblClickCell(r - 1, c - 1)"
            >
              <input
                v-if="editing && editInCell && sel.r === r - 1 && sel.c === c - 1"
                ref="cellEl" v-model="editVal" class="cell-input mono" spellcheck="false"
                @keydown.enter.prevent="commitEdit"
                @keydown.esc="editing = false"
                @blur="commitEdit"
              />
              <template v-else>{{ display(r - 1, c - 1) }}</template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <footer class="status">
      <span>{{ rangeStats || '支持 =A1+B2、=SUM(A1:A10)、=AVG/MIN/MAX/COUNT · Shift+方向键选区 · Ctrl+C/V 与 Excel 互通' }}</span>
    </footer>
  </div>
</template>

<style scoped>
.sheets { width: 100%; display: flex; flex-direction: column; background: var(--os-window); outline: none; }
.cellpos { min-width: 86px; text-align: center; padding: 3px 8px; border-radius: 6px; background: var(--os-active); font-size: 12px; }
.fx { flex: 1; min-width: 120px; padding: 5px 10px; border-radius: 7px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); font-size: 12.5px; }
.grid-wrap { flex: 1; overflow: auto; }
.grid { border-collapse: collapse; font-size: 12.5px; table-layout: fixed; }
.corner { width: 40px; }
th {
  background: var(--os-sidebar); border: 1px solid var(--os-border); font-weight: 600;
  color: var(--os-text-secondary); position: sticky; z-index: 2; min-width: 84px; height: 24px; padding: 3px 8px;
}
thead th { top: 0; }
tbody th { left: 0; position: sticky; }
th.selc, th.selr { background: var(--os-selected); color: var(--os-text); }
td {
  border: 1px solid var(--os-border); padding: 3px 8px; height: 24px; white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis; cursor: cell; color: var(--os-text);
}
td.num { text-align: right; font-variant-numeric: tabular-nums; }
td.sel { outline: 2px solid var(--os-accent); outline-offset: -2px; background: var(--os-selected); }
td.inrange { background: var(--os-selected); }
.cell-input { width: 100%; height: 100%; border: none; background: transparent; color: var(--os-text); font-size: 12.5px; padding: 0; }
.status {
  flex: none; padding: 5px 12px; font-size: 11.5px; color: var(--os-text-secondary);
  border-top: 1px solid var(--os-border); background: var(--os-sidebar);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
</style>
