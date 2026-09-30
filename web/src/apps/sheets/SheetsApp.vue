<script setup lang="ts">
// 表格应用（CSV/TSV）：公式引擎 + Excel 剪贴板互通 + 动态行列 + 自动保存。
// 打开…/存储/另存为走 OsFilePicker；按扩展名区分逗号/制表符分隔。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsFilePicker, toast } from '../../ui'
import { colName, cellName, evalCell, parseCSV } from './engine'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()

const initialPath: string = props.win.props?.path || ''
const filePath = ref('')
const rows = ref(60)
const cols = ref(26)
const grid = ref<string[][]>([])
const loaded = ref(false)

// 选区：anchor 起点，focus 当前焦点（支持 shift 扩展的区域选择）
const anchor = ref({ r: 0, c: 0 })
const sel = ref({ r: 0, c: 0 })
const editing = ref(false)
const editInCell = ref(false)
const editVal = ref('')
const dirty = ref(false)
const saveHint = ref('')
const picker = ref<null | 'open' | 'save'>(null)
const cellEl = ref<HTMLInputElement>()
const fxEl = ref<HTMLInputElement>()
const rootEl = ref<HTMLElement>()

function blankGrid(): string[][] {
  return Array.from({ length: rows.value }, () => Array(cols.value).fill(''))
}
grid.value = blankGrid()

const isTsv = computed(() => filePath.value.endsWith('.tsv'))
const fileName = computed(() => filePath.value ? filePath.value.split('/').pop()! : (isTsv.value ? '未命名表格.tsv' : '未命名表格.csv'))

function syncTitle() {
  windows.setTitle(props.win.id, (dirty.value ? '● ' : '') + fileName.value + ' — 表格')
}
watch([fileName, dirty], syncTitle)

// ---- 撤销 / 重做 ----
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
  markDirty()
}
function redo() {
  const s = redoStack.value.pop()
  if (!s) return
  undoStack.value.push(snapshot())
  grid.value = JSON.parse(s)
  markDirty()
}

function cell(r: number, c: number): string { return grid.value[r]?.[c] || '' }
const display = (r: number, c: number) => evalCell(cell(r, c), (rr, cc) => cell(rr, cc))

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

// ---- 载入（csv/tsv 按分隔符解析；行列按数据量自动扩张） ----
function loadText(text: string) {
  const parsed = isTsv.value
    ? text.replace(/\r/g, '').split('\n').filter((l, i, arr) => l !== '' || i < arr.length - 1).map((l) => l.split('\t'))
    : parseCSV(text)
  rows.value = Math.max(60, parsed.length + 5)
  cols.value = Math.max(26, Math.max(...parsed.map((r) => r.length), 0) + 2)
  grid.value = Array.from({ length: rows.value }, (_, r) =>
    Array.from({ length: cols.value }, (_, c) => parsed[r]?.[c] ?? ''))
}

function serialize(): string {
  const delim = isTsv.value ? '\t' : ','
  const quote = (v: string) => {
    if (isTsv.value) return /[\t\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
    return /[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v
  }
  const lines: string[] = []
  for (const row of grid.value) {
    if (!row.some((c) => c !== '')) continue // 跳过完全空行（含尾部）
    lines.push(row.map(quote).join(delim))
  }
  return lines.join('\n')
}

onMounted(async () => {
  if (initialPath) {
    try {
      const d = await fsApi.read(initialPath)
      if (!d.isBinary) loadText(d.content)
      filePath.value = initialPath
    } catch (e: any) { toast('打开失败：' + e.message, { type: 'error' }) }
  }
  loaded.value = true
  syncTitle()
  rootEl.value?.addEventListener('paste', onPaste)
})
onBeforeUnmount(() => {
  rootEl.value?.removeEventListener('paste', onPaste)
  clearTimeout(autoTimer)
})

// ---- 编辑与导航 ----
function clickCell(r: number, c: number, extend = false) {
  if (extend) sel.value = { r, c }
  else { anchor.value = { r, c }; sel.value = { r, c } }
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
    // v-for 内的模板 ref 是数组，取实际挂载的那个
    const raw = inCell ? cellEl.value : fxEl.value
    const el = (Array.isArray(raw) ? raw[0] : raw) as HTMLInputElement | undefined
    el?.focus()
    el?.select()
  })
}
function commitEdit() {
  if (!editing.value) return
  pushUndo()
  grid.value[sel.value.r][sel.value.c] = editVal.value
  editing.value = false
  markDirty()
}

function onGridKey(e: KeyboardEvent) {
  if (editing.value) {
    if (e.key === 'Escape') editing.value = false
    return
  }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') { e.preventDefault(); save(); return }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'o') { e.preventDefault(); picker.value = 'open'; return }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'z') { e.shiftKey ? redo() : undo(); e.preventDefault(); return }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'y') { redo(); e.preventDefault(); return }
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'c') { copyRange(); e.preventDefault(); return }
  const { r, c } = sel.value
  const move = (nr: number, nc: number, extend: boolean) => {
    // 触到边界自动扩张（Excel 习惯）
    if (nr >= rows.value) rows.value += 20
    if (nc >= cols.value) cols.value += 6
    while (grid.value.length < rows.value) grid.value.push(Array(cols.value).fill(''))
    for (const row of grid.value) while (row.length < cols.value) row.push('')
    sel.value = { r: Math.max(0, nr), c: Math.max(0, nc) }
    if (!extend) anchor.value = { r: sel.value.r, c: sel.value.c }
  }
  switch (e.key) {
    case 'Enter': startEdit(cell(r, c)); e.preventDefault(); break
    case 'Tab': move(r, c + (e.shiftKey ? -1 : 1), false); e.preventDefault(); break
    case 'F2': startEdit(cell(r, c)); e.preventDefault(); break
    case 'ArrowUp': move(r - 1, c, e.shiftKey); e.preventDefault(); break
    case 'ArrowDown': move(r + 1, c, e.shiftKey); e.preventDefault(); break
    case 'ArrowLeft': move(r, c - 1, e.shiftKey); e.preventDefault(); break
    case 'ArrowRight': move(r, c + 1, e.shiftKey); e.preventDefault(); break
    case 'Home': move(r, 0, e.shiftKey); e.preventDefault(); break
    case 'Delete': case 'Backspace':
      pushUndo()
      for (let rr = range.value.r1; rr <= range.value.r2; rr++) for (let cc = range.value.c1; cc <= range.value.c2; cc++) grid.value[rr][cc] = ''
      markDirty()
      e.preventDefault(); break
    default:
      if (e.key.length === 1 && !e.ctrlKey && !e.metaKey) startEdit(e.key)
  }
}

// ---- 剪贴板（与 Excel / Numbers 互通 TSV） ----
function copyRange() {
  const out: string[] = []
  for (let r = range.value.r1; r <= range.value.r2; r++) {
    const cs: string[] = []
    for (let c = range.value.c1; c <= range.value.c2; c++) cs.push(display(r, c))
    out.push(cs.join('\t'))
  }
  navigator.clipboard?.writeText(out.join('\n')).catch(() => {})
}
function onPaste(e: ClipboardEvent) {
  if (editing.value) return
  const text = e.clipboardData?.getData('text/plain')
  if (!text) return
  e.preventDefault()
  const rowsIn = text.replace(/\r/g, '').split('\n').filter((l, i, arr) => l !== '' || i < arr.length - 1)
  // 需要的空间先扩出来
  const needR = sel.value.r + rowsIn.length, needC = sel.value.c + Math.max(...rowsIn.map((l) => l.split('\t').length))
  if (needR > rows.value) rows.value = needR + 5
  if (needC > cols.value) cols.value = needC + 2
  while (grid.value.length < rows.value) grid.value.push(Array(cols.value).fill(''))
  for (const row of grid.value) while (row.length < cols.value) row.push('')
  pushUndo()
  rowsIn.forEach((row, dr) => {
    row.split('\t').forEach((v, dc) => {
      const r = sel.value.r + dr, c = sel.value.c + dc
      if (r < rows.value && c < cols.value) grid.value[r][c] = v
    })
  })
  markDirty()
}

// ---- 行列操作 ----
function insertRow() {
  pushUndo()
  const at = range.value.r1
  grid.value.splice(at, 0, Array(cols.value).fill(''))
  grid.value.length = rows.value
  markDirty()
}
function insertCol() {
  pushUndo()
  const at = range.value.c1
  for (const row of grid.value) row.splice(at, 0, '')
  for (const row of grid.value) row.length = cols.value
  markDirty()
}
function deleteRow() {
  pushUndo()
  grid.value.splice(range.value.r1, range.value.r2 - range.value.r1 + 1)
  while (grid.value.length < rows.value) grid.value.push(Array(cols.value).fill(''))
  markDirty()
}
function deleteCol() {
  pushUndo()
  const n = range.value.c2 - range.value.c1 + 1
  for (const row of grid.value) row.splice(range.value.c1, n)
  for (const row of grid.value) while (row.length < cols.value) row.push('')
  markDirty()
}

// ---- 保存 / 打开 / 另存为 ----
async function writeOut() {
  if (!filePath.value) return
  try {
    await fsApi.write(filePath.value, serialize())
    dirty.value = false
    const t = new Date()
    saveHint.value = `已存储 ${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
    bus.emit('fs:changed', { path: filePath.value, kind: 'write' }, { remote: false })
  } catch (e: any) {
    saveHint.value = ''
    toast('保存失败：' + e.message, { type: 'error' })
  }
}
function save() {
  if (filePath.value) { writeOut(); return }
  picker.value = 'save'
}
async function onPickerConfirm(p: string) {
  const m = picker.value
  picker.value = null
  if (m === 'open') {
    try {
      const d = await fsApi.read(p)
      if (d.isBinary) { toast('二进制表格文件无法打开', { type: 'error' }); return }
      filePath.value = p
      loadText(d.content)
      dirty.value = false
      undoStack.value = []
      redoStack.value = []
    } catch (e: any) { toast('打开失败：' + e.message, { type: 'error' }) }
  } else {
    filePath.value = p
    await writeOut()
  }
}

let autoTimer = 0
function markDirty() {
  if (!loaded.value) return
  dirty.value = true
  if (!filePath.value) return
  clearTimeout(autoTimer)
  autoTimer = window.setTimeout(writeOut, 1500)
}

function exportFile() {
  const blob = new Blob(['\ufeff' + serialize()], { type: 'text/csv' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName.value
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <div ref="rootEl" class="sheets" tabindex="0" @keydown="onGridKey">
    <div class="os-tb">
      <button class="os-tb-btn" title="打开…（⌘O）" @click="picker = 'open'">打开…</button>
      <button class="os-tb-btn" title="存储（⌘S）" @click="save">存储</button>
      <button class="os-tb-btn" title="另存为…" @click="picker = 'save'">另存为…</button>
      <span class="os-tb-sep" />
      <span class="cellpos mono">{{ rangeName }}</span>
      <input v-if="editing && !editInCell" ref="fxEl" v-model="editVal" class="fx mono" spellcheck="false"
        @keydown.enter.prevent="commitEdit" @keydown.esc="editing = false" @blur="commitEdit" />
      <input v-else class="fx mono" :value="selRaw" placeholder="点击单元格或输入公式，如 =SUM(A1:A5)" readonly @pointerdown="startEdit(selRaw, false)" />
      <span class="os-tb-sep" />
      <button class="os-tb-btn" title="撤销（⌘Z）" @click="undo">↩</button>
      <button class="os-tb-btn" title="重做（⌘Y）" @click="redo">↪</button>
      <span class="os-tb-sep" />
      <button class="os-tb-btn" title="在上方插入行" @click="insertRow">＋行</button>
      <button class="os-tb-btn" title="在左侧插入列" @click="insertCol">＋列</button>
      <button class="os-tb-btn" title="删除选中行" @click="deleteRow">－行</button>
      <button class="os-tb-btn" title="删除选中列" @click="deleteCol">－列</button>
      <span class="grow" data-os-drag />
      <span class="meta">{{ saveHint || (dirty ? '未存储' : '') }}</span>
      <button class="os-tb-btn" title="导出到本机" @click="exportFile">导出</button>
    </div>

    <div class="grid-wrap">
      <table class="grid">
        <thead>
          <tr>
            <th class="corner"></th>
            <th v-for="c in cols" :key="c" :class="{ selc: c - 1 >= range.c1 && c - 1 <= range.c2 }">{{ colName(c - 1) }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r">
            <th :class="{ selr: r - 1 >= range.r1 && r - 1 <= range.r2 }">{{ r }}</th>
            <td
              v-for="c in cols" :key="c"
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
      <span>{{ rangeStats || '支持 =A1+B2、=SUM(A1:A10)、=AVG/MIN/MAX/COUNT · Shift+方向键选区 · Ctrl+C/V 与 Excel 互通 · 边缘自动扩行扩列' }}</span>
    </footer>

    <OsFilePicker
      v-if="picker" :mode="picker" :title="picker === 'open' ? '打开表格' : '存储表格为'"
      :default-dir="`/home/${session.user!.username}/表格`" :default-name="fileName"
      :exts="picker === 'open' ? ['csv', 'tsv'] : undefined"
      @confirm="onPickerConfirm" @cancel="picker = null"
    />
  </div>
</template>

<style scoped>
.sheets { width: 100%; display: flex; flex-direction: column; background: var(--os-window); outline: none; }
.grow { flex: 1; }
.meta { font-size: 11.5px; color: var(--os-text-secondary); white-space: nowrap; }
.cellpos { min-width: 86px; text-align: center; padding: 3px 8px; border-radius: 6px; background: var(--os-active); font-size: 12px; flex: none; }
.fx {
  flex: 1; min-width: 120px; padding: 4px 10px; border-radius: 7px; border: 1px solid var(--os-border-strong);
  background: var(--os-input-bg); color: var(--os-text); font-size: 12.5px;
}
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
