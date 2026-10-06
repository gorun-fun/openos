<script setup lang="ts">
// 桌面小组件：时钟 / 便签 / 系统监控 / 宿主自定义（widgetRegistry 注册）。
// 拖动把手移动，释放后吸附网格并与其他小组件/快捷方式互不覆盖；
// 右键（经桌面菜单）移除或换色，经桌面右键菜单或 defineExpose 的 add() 添加。
// 位置持久化；自定义小组件经 { widget, update } props 读写自己的持久化字段。
// type 未注册（宿主下线组件 / 宿主回退旧版）渲染「已失效」占位卡，可移除
// 不静默清除——宿主升级回来布局仍在。
//
// 便签小组件与便签应用（NotesApp）共用 notes 仓库：每张便签可单独钉到桌面，
// 尺寸为网格的整数倍（sc×sr 格，精确到像素对齐），右下角把手缩放时吸附网格。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useSession } from '../stores/session'
import { useWindows } from '../stores/windows'
import { useNotes, NOTE_COLORS } from '../stores/notes'
import { bus } from '../bus/bus'
import { api } from '../api/client'
import { GRID, cellAt, findFree, gridMetrics, isFree, posOf, registerItem, unregisterItem, type GridRect } from './grid'
import { widgetDef } from './widgetRegistry'
import { shortDate } from '../i18n'

interface Widget {
  type: string
  c: number
  r: number
  /** 便签小组件：横向/纵向跨格数（网格整数倍）与关联便签 id */
  sc?: number
  sr?: number
  noteId?: string
  /** 自定义小组件实例标识（非单例类型可多实例，作网格登记键） */
  id?: string
  /** 自定义小组件的任意持久化字段（经 update() 合并写入） */
  [k: string]: any
}

const session = useSession()
const windows = useWindows()
const notes = useNotes()
/** v2：网格坐标（c/r 格子）。旧版为像素（x/y），首次加载时迁移。
 *  权威存储在服务端 user_settings 的 desktop.widgets 键（同一账号换设备布局一致），
 *  localStorage 仅作即时缓存与离线兜底；服务端无数据时把本地布局一次性迁移上云。 */
const SETTINGS_KEY = 'desktop.widgets'
const KEY = () => `openos_widgets_v2_${session.user!.username}`
const OLD_KEY = () => `openos_widgets_${session.user!.username}`
const NOTE_KEY = () => `openos_note_${session.user!.username}`
let serverReady = false // 服务端数据已就位（就位后的变更才回写服务端）
let saveTimer = 0 // 服务端写回防抖
let lastSynced = '' // 最近一次已知服务端内容（无变化不回写，防访问即改库）

const list = ref<Widget[]>([])
/** 时钟/监控/自定义小组件按实际尺寸换算的格子数（挂载后测量）；便签用自身 sc/sr。 */
const spans = ref<Record<string, { sc: number; sr: number }>>({})
const now = ref(new Date())
const online = ref(0)
const aiOn = ref(false)
const usage = ref({ used: 0, quota: 1 })
let timer = 0
const offs: (() => void)[] = []

const keyOf = (w: { type: string; noteId?: string; id?: string }) =>
  w.type === 'note' ? `w:note:${w.noteId}` : (w.id ? `w:${w.type}:${w.id}` : `w:${w.type}`)

/** 占用格数：便签用自身 sc/sr；其余优先测量结果，注册表声明 span 的作初始兜底。 */
const spanOf = (w: Widget) => {
  if (w.type === 'note') return { sc: w.sc || 2, sr: w.sr || 2 }
  const def = widgetDef(w.type)
  return spans.value[w.type] || (def?.span ? { ...def.span } : { sc: 2, sr: 1 })
}

/** 全量重建网格占位注册（服务端数据替换本地缓存后调用）。 */
function registerAll() {
  for (const w of list.value) {
    const sp = spanOf(w)
    registerItem(keyOf(w), { c: w.c, r: w.r, sc: sp.sc, sr: sp.sr, kind: 'widget' })
  }
}
function unregisterAll() {
  for (const w of list.value) unregisterItem(keyOf(w))
}

/** 服务端同步：本地缓存先上屏（含旧格式迁移），服务端数据到达后校正；无数据时迁移上云。 */
async function syncServer() {
  try {
    const s = await api.get<Record<string, string>>('/api/settings')
    serverReady = true // 同步判定已定，后续变更可回写
    if (s[SETTINGS_KEY] !== undefined) {
      let next: Widget[] | null = null
      try {
        const parsed = JSON.parse(s[SETTINGS_KEY])
        if (Array.isArray(parsed)) next = parsed
      } catch { /* 数据损坏：保留本地缓存并回写覆盖 */ }
      if (next === null) {
        persistServer()
      } else {
        if (JSON.stringify(next) !== JSON.stringify(list.value)) {
          unregisterAll()
          list.value = next
          registerAll()
          localStorage.setItem(KEY(), JSON.stringify(list.value)) // 刷新本地缓存
          void measure() // 服务端布局可能带本页未测量的组件类型
        }
        lastSynced = JSON.stringify(next)
        pruneOrphans()
      }
    } else if (list.value.length) {
      persistServer() // 旧版本布局只在 localStorage：一次性迁移到服务端
    }
  } catch { /* 离线容忍：继续用本地缓存 */ }
  serverReady = true
}

onMounted(() => {
  notes.load()
  try {
    const v2 = localStorage.getItem(KEY())
    if (v2) {
      list.value = JSON.parse(v2)
    } else {
      const old: any[] = JSON.parse(localStorage.getItem(OLD_KEY()) || 'null')
      if (old) {
        list.value = old.map((w) => ({
          type: String(w.type),
          c: Math.max(0, Math.round((w.x - GRID.left) / GRID.cw)),
          r: Math.max(0, Math.round((w.y - GRID.top) / GRID.ch)),
        }))
      } else {
        // 首次默认：时钟 + 监控（左上角）
        list.value = [{ type: 'clock', c: 0, r: 0 }, { type: 'monitor', c: 0, r: 1 }]
      }
    }
  } catch { list.value = [] }
  migrateLegacyNote()
  pruneOrphans()
  void syncServer()
  void measure()
  timer = window.setInterval(() => {
    now.value = new Date()
    if (now.value.getSeconds() % 30 === 0) refreshMonitor()
  }, 1000)
  refreshMonitor()

  // 与便签应用同步钉选状态（仅本页：notes:pin 钉/撤，notes:query 查询，notes:widgets 应答）
  offs.push(
    bus.on('notes:pin', (p: any) => {
      if (!p?.id) return
      if (p.on === false) remove('note', p.id)
      else addNote(p.id)
    }),
    bus.on('notes:query', () => emitNotes()),
  )
  emitNotes()
})
onBeforeUnmount(() => {
  clearInterval(timer)
  offs.forEach((off) => off())
})

/** 旧版单一便签小组件（无 noteId）→ 文本迁入 notes 仓库并建立关联。 */
function migrateLegacyNote() {
  const legacy = list.value.filter((w) => w.type === 'note' && !w.noteId)
  if (legacy.length) {
    const text = localStorage.getItem(NOTE_KEY()) || ''
    const n = notes.create(text)
    for (const w of legacy) w.noteId = n.id
  }
  for (const w of list.value) {
    if (w.type === 'note') {
      if (!w.sc) w.sc = 2
      if (!w.sr) w.sr = 2
    }
  }
}

/** 便签已被删除（如便签应用中删除）→ 撤下对应小组件。 */
function pruneOrphans() {
  const dead = list.value.filter((w) => w.type === 'note' && !notes.byId(w.noteId || ''))
  if (!dead.length) return
  for (const w of dead) unregisterItem(keyOf(w))
  list.value = list.value.filter((w) => !dead.includes(w))
  save()
}
watch(
  () => notes.notes.map((n) => n.id).join(','),
  () => {
    const before = list.value.length
    pruneOrphans()
    if (list.value.length !== before) emitNotes()
  },
)

/** 测量时钟/监控/自定义小组件的内容自然尺寸 → 换算占用格数并登记。
 *  卡片自身恒撑满占用格（见 widgetStyle），不能作测量对象，须测 .w-body 里
 *  未受卡片约束的内容根；便签尺寸即 sc/sr、注册表声明 span 的组件免测。 */
async function measure() {
  await nextTick()
  const els = document.querySelectorAll<HTMLElement>('.widgets .widget')
  els.forEach((el) => {
    const type = el.dataset.type!
    if (type === 'note') return
    const declared = widgetDef(type)?.span
    if (declared) {
      spans.value[type] = { ...declared }
      return
    }
    const body = el.querySelector<HTMLElement>(':scope > .w-body > *')
    if (!body) return
    const sc = Math.max(1, Math.ceil((body.offsetWidth + 8) / GRID.cw))
    const sr = Math.max(1, Math.ceil((body.offsetHeight + 8) / GRID.ch))
    spans.value[type] = { sc, sr }
  })
  relayout()
}

async function refreshMonitor() {
  try {
    const h = await api.get<{ online: string[]; ai: boolean }>('/api/health')
    online.value = h.online?.length || 0
    aiOn.value = h.ai
    usage.value = await api.get<{ used: number; quota: number }>('/api/fs/usage')
  } catch { /* ignore */ }
}

function save() {
  localStorage.setItem(KEY(), JSON.stringify(list.value))
  persistServer()
}
/** 服务端写回（400ms 防抖；内容与最近已知服务端一致时跳过，防 measure 重排造成无谓回写）。 */
function persistServer() {
  if (!serverReady) return
  const v = JSON.stringify(list.value)
  if (v === lastSynced) return
  clearTimeout(saveTimer)
  saveTimer = window.setTimeout(() => {
    lastSynced = v
    api.put('/api/settings', { key: SETTINGS_KEY, value: v }).catch(() => { lastSynced = '' })
  }, 400)
}

/** 向本页便签应用广播当前钉到桌面的便签 id 集合。 */
function emitNotes() {
  bus.emit('notes:widgets', { ids: list.value.filter((w) => w.type === 'note').map((w) => w.noteId!) }, { remote: false })
}

function add(type: string) {
  const def = widgetDef(type)
  if (!def) return
  if (def.singleton && list.value.some((w) => w.type === type)) return
  const w: Widget = type === 'note'
    ? { type, c: 0, r: 0, sc: 2, sr: 2, noteId: notes.create().id }
    : { type, c: 0, r: 0, ...def.defaults, ...(def.singleton ? {} : { id: Date.now().toString(36) }) }
  list.value.push(w)
  void nextTick(async () => {
    await measure() // measure 内会 relayout，把新组件放进最近空位
    save()
    emitNotes()
  })
}

/** 钉一张已有便签到桌面（便签应用 notes:pin 调用）。 */
function addNote(id: string) {
  if (!notes.byId(id) || list.value.some((w) => w.type === 'note' && w.noteId === id)) return
  list.value.push({ type: 'note', c: 0, r: 0, sc: 2, sr: 2, noteId: id })
  void nextTick(async () => {
    await measure()
    save()
    emitNotes()
  })
}

/** 自定义小组件合并持久化字段（{ widget, update } props 的 update 回调）。 */
function updateWidget(w: Widget, patch: Record<string, any>) {
  Object.assign(w, patch)
  const sp = spanOf(w)
  registerItem(keyOf(w), { c: w.c, r: w.r, sc: sp.sc, sr: sp.sr, kind: 'widget' })
  save()
}

function remove(type: string, noteId?: string) {
  const dead = list.value.filter((w) => w.type === type && (type !== 'note' || w.noteId === noteId))
  if (!dead.length) return
  for (const w of dead) unregisterItem(keyOf(w))
  list.value = list.value.filter((w) => !dead.includes(w))
  save()
  emitNotes()
}

/** 桌面右键菜单：便签小组件换色。 */
function cycleNoteColor(noteId?: string) {
  if (noteId) notes.cycleColor(noteId)
}

/** 视口变化/加载后重排：吸附网格并解决重叠（保持既有位置优先）。 */
function relayout() {
  for (const w of list.value) {
    const sp = spanOf(w)
    const rect = findFree(w.c, w.r, sp.sc, sp.sr, 'widget', keyOf(w))
    w.c = rect.c
    w.r = rect.r
    registerItem(keyOf(w), { c: w.c, r: w.r, sc: sp.sc, sr: sp.sr, kind: 'widget' })
  }
  save()
}

// ---- 指针跟踪：优先 pointer capture（指针移出元素仍派发），失败时退化到 window 监听 ----
function trackPointer(el: HTMLElement, e: PointerEvent, move: (ev: PointerEvent) => void, up: (ev: PointerEvent) => void) {
  let done = false
  const end = (ev: PointerEvent) => {
    if (done) return
    done = true
    el.removeEventListener('pointermove', move)
    el.removeEventListener('pointerup', end)
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', end)
    up(ev)
  }
  try { el.setPointerCapture(e.pointerId) } catch { /* 无活动指针（合成事件/旧内核）：window 兜底 */ }
  el.addEventListener('pointermove', move)
  el.addEventListener('pointerup', end)
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', end)
}

// ---- 拖动：跟手移动（响应式位置），释放后吸附到最近空格 ----
const dragging = ref<{ key: string; x: number; y: number } | null>(null)
/** 拖动落点预览：松手后小组件将吸附到的格子（虚线框，与 up 的吸附计算严格同源）。 */
const ghost = ref<GridRect | null>(null)
function onHandleDown(e: PointerEvent, w: Widget) {
  const el = (e.currentTarget as HTMLElement).parentElement!
  const startX = e.clientX, startY = e.clientY
  const orig = posOf(w)
  const origX = orig.x, origY = orig.y
  const move = (ev: PointerEvent) => {
    const x = Math.min(Math.max(4, origX + ev.clientX - startX), window.innerWidth - el.offsetWidth - 4)
    const y = Math.min(Math.max(34, origY + ev.clientY - startY), window.innerHeight - el.offsetHeight - 4)
    dragging.value = { key: keyOf(w), x, y }
    // 落点预览与释放吸附同源：同一指针位置算出的就是真实落格（预览即结果）
    const cell = cellAt(ev.clientX - el.offsetWidth / 2, ev.clientY - 10)
    const sp = spanOf(w)
    ghost.value = findFree(cell.c, cell.r, sp.sc, sp.sr, 'widget', keyOf(w))
  }
  const up = (ev: PointerEvent) => {
    dragging.value = null
    ghost.value = null
    const cell = cellAt(ev.clientX - el.offsetWidth / 2, ev.clientY - 10)
    const sp = spanOf(w)
    const rect = findFree(cell.c, cell.r, sp.sc, sp.sr, 'widget', keyOf(w))
    w.c = rect.c
    w.r = rect.r
    registerItem(keyOf(w), { c: w.c, r: w.r, sc: sp.sc, sr: sp.sr, kind: 'widget' })
    save()
  }
  trackPointer(el, e, move, up)
}

// ---- 便签缩放：右下角把手拖动，跨格数按四舍五入吸附网格；与其他项冲突时回弹 ----
function onResizeDown(e: PointerEvent, w: Widget) {
  const grip = e.currentTarget as HTMLElement
  const origin = posOf(w)
  const start = { sc: w.sc || 2, sr: w.sr || 2 }
  const move = (ev: PointerEvent) => {
    const { cols, rows } = gridMetrics()
    // +8 补回格间隙，使指针贴近格线时正好吸附整数格
    w.sc = Math.min(Math.max(1, Math.round((ev.clientX - origin.x + 8) / GRID.cw)), Math.max(1, cols - w.c))
    w.sr = Math.min(Math.max(1, Math.round((ev.clientY - origin.y + 8) / GRID.ch)), Math.max(1, rows - w.r))
  }
  const up = () => {
    const sp = spanOf(w)
    if (!isFree({ c: w.c, r: w.r, sc: sp.sc, sr: sp.sr }, keyOf(w))) {
      w.sc = start.sc // 压到其他小组件/快捷方式：回弹保持互不覆盖
      w.sr = start.sr
      return
    }
    registerItem(keyOf(w), { c: w.c, r: w.r, sc: sp.sc, sr: sp.sr, kind: 'widget' })
    save()
  }
  trackPointer(grip, e, move, up)
}

// ---- 便签小组件数据绑定（notes 仓库） ----
const noteOf = (w: Widget) => notes.byId(w.noteId || '')
const textOf = (w: Widget) => noteOf(w)?.text || ''
const colorOf = (w: Widget) => NOTE_COLORS[noteOf(w)?.color ?? 0] || NOTE_COLORS[0]
function onNoteInput(w: Widget, e: Event) {
  const n = noteOf(w)
  if (n) notes.setText(n.id, (e.target as HTMLTextAreaElement).value)
}

/** 小组件定位样式：拖动中跟手，否则吸附网格。所有卡片恒撑满占用的格子
 *  （跨格数 × 格宽/高 − 8 格间隙，与网格精确对齐），占用与视觉一致——
 *  注册表占多少格、肉眼就看得到多少格，拖动落点所见即所得；便签附加纸面底色。 */
function widgetStyle(w: Widget) {
  const p = posOf(w)
  const sp = spanOf(w)
  const s: Record<string, string> = dragging.value?.key === keyOf(w)
    ? { left: dragging.value.x + 'px', top: dragging.value.y + 'px' }
    : { left: p.x + 'px', top: p.y + 'px' }
  s.width = sp.sc * GRID.cw - 8 + 'px'
  s.height = sp.sr * GRID.ch - 8 + 'px'
  if (w.type === 'note') s.background = colorOf(w).bg
  return s
}

/** 落点预览框几何：与便签小组件同一套格子换算（sc×sr 格减格间隙，精确对齐网格）。 */
const ghostStyle = computed(() => {
  if (!ghost.value) return {}
  const p = posOf(ghost.value)
  return {
    left: p.x + 'px',
    top: p.y + 'px',
    width: ghost.value.sc * GRID.cw - 8 + 'px',
    height: ghost.value.sr * GRID.ch - 8 + 'px',
  }
})

const clockTime = () => `${String(now.value.getHours()).padStart(2, '0')}:${String(now.value.getMinutes()).padStart(2, '0')}`
const clockDate = () => shortDate(now.value)
const fmtMB = (n: number) => (n / 1048576).toFixed(1) + ' MB'

defineExpose({ add, remove, cycleNoteColor, relayout })
</script>

<template>
  <div class="widgets">
    <!-- 拖动落点预览：松手后小组件将吸附的格子（虚线框；在 DOM 上先于各小组件，垫在其下） -->
    <div v-if="ghost" class="drop-ghost" :style="ghostStyle" aria-hidden="true" />
    <div
      v-for="w in list" :key="keyOf(w)"
      class="widget"
      :class="{ 'widget-note': w.type === 'note' }"
      :data-type="w.type"
      :data-note="w.noteId"
      :style="widgetStyle(w)"
    >
      <div class="handle" @pointerdown="onHandleDown($event, w)" @dblclick.prevent title="拖动移动" />

      <!-- 内容层：卡片撑满占用格，内容自然尺寸居中（便签的 textarea/角标相对它铺满） -->
      <div class="w-body">
        <!-- 时钟 -->
        <div v-if="w.type === 'clock'" class="clock">
          <div class="time">{{ clockTime() }}</div>
          <div class="date">{{ clockDate() }}</div>
        </div>

        <!-- 便签：内容与便签应用实时同步，右下角把手缩放（吸附网格） -->
        <template v-else-if="w.type === 'note'">
          <textarea
            :value="textOf(w)"
            :style="{ color: colorOf(w).text }"
            placeholder="记点什么…（自动保存）"
            spellcheck="false"
            @input="onNoteInput(w, $event)"
            @pointerdown.stop
          />
          <div class="resize" title="拖动缩放（吸附网格）" @pointerdown.stop.prevent="onResizeDown($event, w)" />
        </template>

        <!-- 系统监控 -->
        <div v-else-if="w.type === 'monitor'" class="monitor">
          <div class="row"><span>👥 在线</span><b>{{ online }}</b></div>
          <div class="row"><span>✦ AI</span><b>{{ aiOn ? '已接入' : '演示' }}</b></div>
          <div class="row"><span>🪟 窗口</span><b>{{ windows.list.length }}</b></div>
          <div class="row"><span>💾 存储</span><b :title="fmtMB(usage.used) + ' / ' + fmtMB(usage.quota)">{{ fmtMB(usage.used) }}</b></div>
        </div>

        <!-- 已失效小组件（type 不在注册表：宿主下线该组件 / 宿主回退旧版后布局
             残留）：占位提示 + 移除按钮，不静默清除——宿主升级回来布局仍在 -->
        <div v-else-if="!widgetDef(w.type)" class="unknown">
          <div class="u-title">小组件已失效</div>
          <div class="u-type" :title="w.type">{{ w.type }}</div>
          <button class="u-rm" @click="remove(w.type)">移除</button>
        </div>

        <!-- 宿主/第三方注册的自定义小组件（widgetRegistry） -->
        <component
          v-else
          :is="widgetDef(w.type)?.component"
          :widget="w"
          :update="(patch: Record<string, any>) => updateWidget(w, patch)"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.widgets { position: absolute; inset: 0; pointer-events: none; }
/* 拖动落点预览：松手后小组件将吸附的格子（与图标层 Shortcuts 的 ghost 同款虚线格框） */
.drop-ghost {
  position: absolute; pointer-events: none;
  border-radius: 16px;
  border: 1.5px dashed rgba(255,255,255,.5);
  background: rgba(255,255,255,.07);
}
.widget {
  position: absolute; pointer-events: auto; border-radius: 16px;
  background: rgba(28, 28, 32, 0.55);
  backdrop-filter: blur(24px) saturate(1.4);
  -webkit-backdrop-filter: blur(24px) saturate(1.4);
  border: 1px solid rgba(255,255,255,.14);
  box-shadow: 0 10px 34px rgba(0,0,0,.28);
  color: #f2f3f7; overflow: hidden; animation: os-fade-in .2s ease;
}
.handle {
  position: absolute; top: 0; left: 50%; transform: translateX(-50%);
  width: 48px; height: 14px; border-radius: 0 0 7px 7px; background: transparent;
  cursor: grab; z-index: 2; touch-action: none;
  display: flex; align-items: center; justify-content: center;
}
.handle::after {
  content: ''; width: 34px; height: 4px; border-radius: 2px;
  background: rgba(255,255,255,.28);
}
.handle:active { cursor: grabbing; }
.handle:active::after { background: rgba(255,255,255,.45); }

/* 内容层：铺满卡片、内容自然尺寸居中（占 2×2 格的卡片即显示在四格区域正中）。
   内容根禁止 flex 收缩/拉伸，保持自然宽度（max-content/固定宽），
   measure 以它换算占用格数——被压缩会导致测格数永远偏小。 */
.w-body { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; }
.w-body > * { flex: none; }

.clock { padding: 18px 22px 14px; text-align: center; min-width: 120px; width: max-content; }
.clock .time { font-size: 34px; font-weight: 200; font-variant-numeric: tabular-nums; letter-spacing: 1px; }
.clock .date { font-size: 12px; opacity: .75; margin-top: 2px; }

/* 便签小组件：纸面配色由内联样式注入，去掉玻璃拟态保证纸感 */
.widget-note {
  border-color: rgba(0,0,0,.14);
  backdrop-filter: none;
  -webkit-backdrop-filter: none;
}
.widget-note .handle::after { background: rgba(0,0,0,.22); }
.widget-note textarea {
  position: absolute; inset: 0; width: 100%; height: 100%;
  resize: none; border: none; outline: none; background: transparent;
  padding: 20px 12px 12px; font-size: 13px; line-height: 1.7; font-family: inherit;
}
.widget-note textarea::placeholder { color: rgba(0,0,0,.35); }
.resize {
  position: absolute; right: 0; bottom: 0; width: 22px; height: 22px;
  cursor: nwse-resize; touch-action: none; z-index: 3;
  background: linear-gradient(135deg,
    transparent 0 52%, rgba(0,0,0,.22) 52% 60%, transparent 60% 68%,
    rgba(0,0,0,.22) 68% 76%, transparent 76% 84%, rgba(0,0,0,.22) 84% 92%, transparent 92%);
}

.monitor { padding: 12px 16px; min-width: 150px; width: max-content; display: flex; flex-direction: column; gap: 7px; }
.monitor .row { display: flex; justify-content: space-between; gap: 14px; font-size: 12px; }
.monitor .row span { opacity: .72; }
.monitor .row b { font-weight: 600; font-variant-numeric: tabular-nums; }

/* 已失效小组件占位（type 未注册）：居中提示 + 移除入口（右键菜单同款能力） */
.unknown {
  padding: 14px 16px; min-width: 150px; max-width: 210px; text-align: center;
  display: flex; flex-direction: column; align-items: center; gap: 5px;
}
.u-title { font-size: 12.5px; color: rgba(255, 255, 255, .78); }
.u-type {
  font-size: 11px; color: rgba(255, 255, 255, .45); font-family: ui-monospace, monospace;
  max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.u-rm {
  margin-top: 5px; padding: 3px 16px; border-radius: 7px; font-size: 12px; cursor: pointer;
  border: 1px solid rgba(255, 255, 255, .25); background: rgba(255, 255, 255, .1); color: #f2f3f7;
}
.u-rm:hover { background: rgba(255, 255, 255, .18); }
</style>
