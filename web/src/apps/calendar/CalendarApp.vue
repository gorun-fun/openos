<script setup lang="ts">
// 日历应用：中国节假日（含调休「休/班」标记）+ 农历/节日副行 + 日程管理。
// 数据在 stores/calendar.ts（事件持久化 + 节假日按年加载 + 提醒引擎），
// 农历换算在 ./lunar.ts；提醒到期由通知中心横幅弹出（桌面级，应用关掉也提醒）。
// 窗体为融合工具栏形态（chrome:'merged'）。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useCalendar, REMIND_OPTIONS, todayStr, type CalEvent, type CalMark, type RemindOffset } from '../../stores/calendar'
import { cellSub, lunarOf } from './lunar'
import { icons, OsEmpty } from '../../ui'

const props = defineProps<{ win: WinState; date?: string }>()
const windows = useWindows()
const cal = useCalendar()

// ---- 视图状态：当前展示月 + 选中日 ----
const today = new Date()
const view = ref({ y: today.getFullYear(), m: today.getMonth() }) // m: 0-11
const selected = ref(props.date || todayStr())

const DAY_KEYS = ['一', '二', '三', '四', '五', '六', '日']
const fmt = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
const monIdx = (d: Date) => (d.getDay() + 6) % 7 // 周一为 0

/** 月历 42 格（6 周，周一起）；含前后月补位（灰显但可选中）。 */
const cells = computed(() => {
  const first = new Date(view.value.y, view.value.m, 1)
  const start = new Date(first)
  start.setDate(start.getDate() - monIdx(first))
  const out: { date: Date; key: string; inMonth: boolean; isToday: boolean }[] = []
  for (let i = 0; i < 42; i++) {
    const d = new Date(start)
    d.setDate(start.getDate() + i)
    out.push({ date: d, key: fmt(d), inMonth: d.getMonth() === view.value.m, isToday: fmt(d) === todayStr() })
  }
  return out
})

const viewLabel = computed(() => `${view.value.y}年${view.value.m + 1}月`)
/** 选中日详情：农历全称 / 节假日。 */
const selectedInfo = computed(() => {
  const d = new Date(`${selected.value}T00:00:00`)
  const l = lunarOf(d)
  return {
    weekday: `周${DAY_KEYS[monIdx(d)]}`,
    label: `${d.getMonth() + 1}月${d.getDate()}日`,
    lunar: l ? `${l.yearGanZhi}${l.animal}年 · ${l.isLeap ? '闰' : ''}${l.monthCn}${l.dayCn}` : '',
    festival: l?.festival,
    holiday: cal.holidayOf(selected.value),
  }
})
const dayEvents = computed(() => cal.byDate(selected.value))

function prevMonth() { const v = view.value; view.value = v.m === 0 ? { y: v.y - 1, m: 11 } : { y: v.y, m: v.m - 1 } }
function nextMonth() { const v = view.value; view.value = v.m === 11 ? { y: v.y + 1, m: 0 } : { y: v.y, m: v.m + 1 } }
function goToday() {
  const d = new Date()
  view.value = { y: d.getFullYear(), m: d.getMonth() }
  selected.value = todayStr()
}

function pick(key: string) {
  selected.value = key
  const d = new Date(`${key}T00:00:00`)
  if (d.getMonth() !== view.value.m || d.getFullYear() !== view.value.y) view.value = { y: d.getFullYear(), m: d.getMonth() }
}
// 翻页跨年时按需加载相邻年份节假日（年内已缓存不再请求）
watch(view, (v) => { cal.loadYear(v.y - 1); cal.loadYear(v.y); cal.loadYear(v.y + 1) }, { immediate: true })
// 外部带日期打开（小组件/通知跳转）
watch(() => props.date, (d) => { if (d) pick(d) })

// ---- 事件编辑（新建/修改共用一份内联表单） ----
const editing = ref<{ id?: string; title: string; date: string; time: string; remind: RemindOffset } | null>(null)
const titleInput = ref<HTMLInputElement>()

function startAdd(date?: string) {
  editing.value = { title: '', date: date || selected.value, time: '', remind: 'at' }
  void nextTick(() => titleInput.value?.focus())
}
function startEdit(e: CalEvent) {
  editing.value = { id: e.id, title: e.title, date: e.date, time: e.time || '', remind: e.remind }
  void nextTick(() => titleInput.value?.focus())
}
function cancelEdit() { editing.value = null }
function submitEdit() {
  const f = editing.value
  if (!f || !f.title.trim()) { titleInput.value?.focus(); return }
  if (f.id) {
    cal.updateEvent(f.id, { title: f.title.trim(), date: f.date, time: f.time || undefined, remind: f.remind })
  } else {
    cal.addEvent({ title: f.title.trim(), date: f.date, time: f.time || undefined, remind: f.remind })
    selected.value = f.date
  }
  editing.value = null
}
function removeEvent(e: CalEvent) {
  if (editing.value?.id === e.id) editing.value = null
  cal.removeEvent(e.id)
}
function onFormKey(e: KeyboardEvent) {
  if (e.key === 'Enter') { e.preventDefault(); submitEdit() }
  if (e.key === 'Escape') { e.preventDefault(); cancelEdit() }
}

const remindLabel = (r: RemindOffset) => REMIND_OPTIONS.find((o) => o.value === r)?.label || r
const isWeekend = (key: string) => { const w = new Date(`${key}T00:00:00`).getDay(); return w === 0 || w === 6 }

/** 自定义标签配色：文字用标记色，底色为其 14% 透明混合（兼容任意 CSS 颜色格式）。 */
function tagStyle(m: CalMark) {
  const c = m.color || 'var(--os-accent)'
  return { color: c, background: `color-mix(in srgb, ${c} 14%, transparent)` }
}

// ---- 月历上下滚动翻月：wheel 累计位移超阈值翻页（冷却期防触摸板惯性连翻），触摸滑动同理 ----
const flipDir = ref(0) // 1=下一月（内容上滑入场），-1=上一月；0 无动画
let wheelAcc = 0
let wheelCooldown = 0
let touchY0 = 0

function flip(dir: number) {
  flipDir.value = dir
  if (dir > 0) nextMonth()
  else prevMonth()
}
function onWheel(e: WheelEvent) {
  if (!e.deltaY) return
  const now = performance.now()
  if (now < wheelCooldown) { wheelAcc = 0; return }
  wheelAcc += e.deltaY
  if (Math.abs(wheelAcc) >= 60) {
    flip(wheelAcc > 0 ? 1 : -1)
    wheelAcc = 0
    wheelCooldown = now + 400
  }
}
function onTouchStart(e: TouchEvent) { touchY0 = e.touches[0]?.clientY ?? 0 }
function onTouchEnd(e: TouchEvent) {
  const dy = touchY0 - (e.changedTouches[0]?.clientY ?? touchY0)
  if (Math.abs(dy) >= 46) flip(dy > 0 ? 1 : -1)
}

onMounted(() => {
  windows.setTitle(props.win.id, '日历')
  cal.load()
  if (props.date) pick(props.date)
})
</script>

<template>
  <div class="cal-app">
    <!-- 融合工具栏：整条可拖动窗口 -->
    <div class="os-tb cal-bar" data-os-drag>
      <button class="os-tb-btn" :title="'上一月'" v-html="icons.chevL" @click="prevMonth" />
      <button class="os-tb-btn" :title="'下一月'" v-html="icons.chevR" @click="nextMonth" />
      <button class="os-tb-btn txt" title="回到今天" @click="goToday">今天</button>
      <div class="tb-title">{{ viewLabel }}</div>
      <span class="grow" />
      <button class="os-tb-btn primary" title="新建日程" v-html="icons.note" @click="startAdd()" />
    </div>

    <div class="cal-body">
      <!-- 月历（上下滚动/滑动翻月） -->
      <div class="month" @wheel.prevent="onWheel" @touchstart="onTouchStart" @touchend="onTouchEnd">
        <div class="week-head">
          <span v-for="w in DAY_KEYS" :key="w" class="wh" :class="{ rest: w === '六' || w === '日' }">{{ w }}</span>
        </div>
        <div :key="`${view.y}-${view.m}`" class="grid" :class="flipDir > 0 ? 'slide-up' : flipDir < 0 ? 'slide-down' : ''">
          <button
            v-for="c in cells" :key="c.key"
            class="cell" :class="{ out: !c.inMonth, sel: selected === c.key }"
            @click="pick(c.key)"
          >
            <span class="drow">
              <span class="num" :class="{
                today: c.isToday,
                rest: isWeekend(c.key),
                off: cal.holidayOf(c.key)?.off,
              }">{{ c.date.getDate() }}</span>
              <span class="sub" :class="{ fest: cellSub(c.date).red }">{{ cellSub(c.date).text }}</span>
            </span>
            <span v-if="cal.holidayOf(c.key)" class="mark" :class="cal.holidayOf(c.key)!.off ? 'rest' : 'work'">
              {{ cal.holidayOf(c.key)!.off ? '休' : '班' }}
            </span>
            <span v-if="cal.marksOf(c.key).length" class="tags" :title="cal.marksOf(c.key).map((m) => m.text).join('、')">
              <i v-for="m in cal.marksOf(c.key).slice(0, 2)" :key="m.appId" :style="tagStyle(m)">{{ m.text }}</i>
              <em v-if="cal.marksOf(c.key).length > 2">…</em>
            </span>
            <span v-if="cal.byDate(c.key).length" class="dots">
              <i v-for="e in cal.byDate(c.key).slice(0, 3)" :key="e.id" :class="{ muted: !e.time }" />
              <em v-if="cal.byDate(c.key).length > 3">…</em>
            </span>
          </button>
        </div>
      </div>

      <!-- 选中日详情 + 日程 -->
      <aside class="day">
        <header class="day-head">
          <div class="weekday" :class="{ red: selectedInfo.weekday === '周六' || selectedInfo.weekday === '周日' }">{{ selectedInfo.weekday }}</div>
          <div class="dlabel">{{ selectedInfo.label }}</div>
          <div class="lunar">{{ selectedInfo.lunar }}</div>
          <div class="badges">
            <span v-if="selectedInfo.holiday" class="badge" :class="selectedInfo.holiday.off ? 'rest' : 'work'">
              {{ selectedInfo.holiday.name }} · {{ selectedInfo.holiday.off ? '休' : '班' }}
            </span>
            <span v-else-if="selectedInfo.festival" class="badge fest">{{ selectedInfo.festival }}</span>
            <span v-for="m in cal.marksOf(selected)" :key="'mk-' + m.appId" class="badge tagb" :style="tagStyle(m)">{{ m.text }}</span>
          </div>
        </header>

        <div class="sec-title">
          <span>日程（{{ dayEvents.length }}）</span>
          <button class="mini" title="新建日程" @click="startAdd()">＋ 新建</button>
        </div>

        <!-- 新建/编辑表单 -->
        <form v-if="editing" class="eform" @submit.prevent="submitEdit" @keydown="onFormKey">
          <input ref="titleInput" v-model="editing.title" class="os-flat inp title" placeholder="日程标题…" maxlength="80" />
          <div class="row">
            <label>日期</label>
            <input v-model="editing.date" type="date" class="os-flat inp" required />
          </div>
          <div class="row">
            <label>时间</label>
            <input v-model="editing.time" type="time" class="os-flat inp" />
          </div>
          <div class="row">
            <label>提醒</label>
            <select v-model="editing.remind" class="os-flat inp">
              <option v-for="o in REMIND_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
          </div>
          <div class="btns">
            <button type="button" class="os-tb-btn txt" @click="cancelEdit">取消</button>
            <button type="submit" class="os-tb-btn primary txt" :disabled="!editing.title.trim()">保存</button>
          </div>
        </form>

        <div class="elist">
          <div v-for="e in dayEvents" :key="e.id" class="ev" @click="startEdit(e)">
            <span class="etime">{{ e.time || '全天' }}</span>
            <div class="emain">
              <b>{{ e.title }}</b>
              <small v-if="e.remind !== 'none'">{{ remindLabel(e.remind) }}</small>
            </div>
            <button class="del" title="删除日程" @click.stop="removeEvent(e)">
              <svg viewBox="0 0 16 16" width="13" height="13"><path d="M3 4.5h10M6.5 4.5V3.2a.7.7 0 0 1 .7-.7h1.6a.7.7 0 0 1 .7.7v1.3M4.6 4.5l.5 8a1 1 0 0 0 1 .9h3.8a1 1 0 0 0 1-.9l.5-8" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/></svg>
            </button>
          </div>
          <OsEmpty v-if="!dayEvents.length && !editing" text="这一天还没有日程" />
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.cal-app { width: 100%; height: 100%; display: flex; flex-direction: column; background: var(--os-window); overflow: hidden; }
.cal-bar { gap: 2px; }
.cal-bar .tb-title { font-size: 13.5px; font-weight: 700; margin-left: 6px; }
.cal-bar .primary { background: var(--os-accent); border-color: var(--os-accent); color: #fff; }
.cal-bar .primary:hover { background: var(--os-accent-hover); }
.cal-bar .txt { font-size: 12px; padding: 3px 9px; }
.grow { flex: 1; }

.cal-body { flex: 1; display: flex; min-height: 0; }

/* ---- 月历 ---- */
.month { flex: 1; display: flex; flex-direction: column; padding: 10px 12px 12px; min-width: 0; }
.week-head { display: grid; grid-template-columns: repeat(7, 1fr); margin-bottom: 4px; }
.wh { text-align: center; font-size: 12px; color: var(--os-text-secondary); }
.wh.rest { color: #e5484d; }
/* 网格线（hairline）：容器画上/左缘、格子画右/下缘，避免相邻双边框。用 inset
   box-shadow 而非 border——border 会被 Chrome 取整成整像素（0.5px 声明无效，
   retina 屏 1px 边框渲染为 2 物理像素显粗），shadow 走连续合成：高密度屏
   （≥2x）用 0.5px 恰为 1 物理像素细线，普通屏保持 1px（0.5px 会淡至不可见）。 */
.grid {
  flex: 1; display: grid; grid-template-columns: repeat(7, 1fr); grid-auto-rows: 1fr; min-height: 0;
  box-shadow: inset 1px 0 0 var(--os-border), inset 0 1px 0 var(--os-border);
}
.cell {
  position: relative; display: flex; flex-direction: column; align-items: center; justify-content: start;
  padding-top: 4px; border-radius: 0; border: none;
  box-shadow: inset -1px 0 0 var(--os-border), inset 0 -1px 0 var(--os-border);
  background: transparent; cursor: pointer;
  color: var(--os-text); transition: background .1s; min-height: 52px;
}
@media (-webkit-min-device-pixel-ratio: 2), (min-resolution: 2dppx) {
  .grid { box-shadow: inset 0.5px 0 0 var(--os-border), inset 0 0.5px 0 var(--os-border); }
  .cell { box-shadow: inset -0.5px 0 0 var(--os-border), inset 0 -0.5px 0 var(--os-border); }
}
.cell:hover { background: var(--os-hover); }
.cell.out { opacity: .38; }
.cell.sel { background: var(--os-selected); }
/* 翻月动画：新月份沿滚动方向滑入 */
@keyframes calSlideUp { from { opacity: 0; transform: translateY(14px); } }
@keyframes calSlideDown { from { opacity: 0; transform: translateY(-14px); } }
.grid.slide-up { animation: calSlideUp .18s ease-out; }
.grid.slide-down { animation: calSlideDown .18s ease-out; }
.num {
  position: relative; z-index: 1; display: inline-flex; align-items: center; justify-content: center;
  min-width: 26px; height: 26px; border-radius: 50%; font-size: 14.5px; font-variant-numeric: tabular-nums;
}
.num.rest { color: #e5484d; }
.num.today { background: #e5484d; color: #fff; font-weight: 700; }
/* 数字与农历/节日同行（农历在数字右侧） */
.drow { display: inline-flex; align-items: center; gap: 4px; max-width: 100%; overflow: hidden; }
.drow .num { flex: none; }
.sub { font-size: 10.5px; color: var(--os-text-secondary); line-height: 1.3; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 传统节日文字用绿色（与「休」一致；red 类名随语义更名 fest） */
.sub.fest { color: #2da44e; }
/* 调休角标：休（绿）/ 班（红，提示补班） */
.mark {
  position: absolute; top: 3px; right: 4px; z-index: 2;
  font-size: 9.5px; font-style: normal; line-height: 1; padding: 2px 3px; border-radius: 4px; font-weight: 600;
}
.mark.rest { background: rgba(45, 164, 78, .18); color: #2da44e; }
.mark.work { background: rgba(229, 72, 77, .16); color: #e5484d; }
/* 自由标签（应用登记的日标记）：最多展示 2 个，其余省略号（title 看全文） */
.tags { display: inline-flex; gap: 2px; align-items: center; justify-content: center; margin-top: 1px; max-width: 100%; overflow: hidden; }
.tags i {
  font-size: 9.5px; font-style: normal; font-weight: 600; line-height: 1;
  padding: 2px 3px; border-radius: 4px; max-width: 64px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.tags em { font-size: 9px; font-style: normal; color: var(--os-text-secondary); }
/* 事件小圆点 */
.dots { display: inline-flex; gap: 3px; align-items: center; margin-top: 2px; }
.dots i { width: 4px; height: 4px; border-radius: 50%; background: var(--os-accent); }
.dots i.muted { background: var(--os-text-secondary); opacity: .55; }
.dots em { font-size: 9px; font-style: normal; color: var(--os-text-secondary); }

/* ---- 选中日侧栏 ---- */
.day { width: 268px; flex: none; border-left: 1px solid var(--os-border); display: flex; flex-direction: column; padding: 14px 14px 10px; min-height: 0; }
.day-head .weekday { font-size: 22px; font-weight: 800; }
.day-head .weekday.red { color: #e5484d; }
.day-head .dlabel { font-size: 13px; color: var(--os-text); margin-top: 2px; }
.day-head .lunar { font-size: 12px; color: var(--os-text-secondary); margin-top: 2px; }
.badges { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
.badge { font-size: 11px; font-weight: 600; padding: 2.5px 8px; border-radius: 999px; }
.badge.rest { background: rgba(45, 164, 78, .16); color: #2da44e; }
.badge.work { background: rgba(120, 120, 128, .16); color: var(--os-text-secondary); }
.badge.fest { background: rgba(45, 164, 78, .16); color: #2da44e; }

.sec-title {
  display: flex; align-items: center; justify-content: space-between;
  font-size: 12px; font-weight: 700; color: var(--os-text-secondary);
  margin: 14px 0 8px; padding-top: 12px; border-top: 1px solid var(--os-border);
}
.mini {
  border: 1px solid var(--os-border); background: transparent; color: var(--os-text);
  font-size: 11.5px; padding: 2px 8px; border-radius: 999px; cursor: pointer;
}
.mini:hover { background: var(--os-hover); }

/* 表单 */
.eform { display: flex; flex-direction: column; gap: 8px; padding: 10px; border-radius: 12px; background: var(--os-hover); margin-bottom: 10px; }
.eform .row { display: flex; align-items: center; gap: 8px; }
.eform .row label { font-size: 12px; color: var(--os-text-secondary); width: 30px; flex: none; }
.inp {
  flex: 1; min-width: 0; border: 1px solid var(--os-border); border-radius: 8px;
  background: var(--os-window); color: var(--os-text); font-size: 12.5px; padding: 5px 8px;
}
.inp.title { font-size: 13px; }
.inp:focus { outline: none; border-color: var(--os-accent); }
.btns { display: flex; justify-content: flex-end; gap: 8px; }
.eform .primary { background: var(--os-accent); border-color: var(--os-accent); color: #fff; }
.eform .primary:disabled { opacity: .45; }

/* 日程列表 */
.elist { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 6px; min-height: 0; }
.ev {
  display: flex; gap: 10px; align-items: center; padding: 8px 10px; border-radius: 10px;
  background: var(--os-hover); cursor: pointer; position: relative;
}
.ev:hover { background: var(--os-selected); }
.etime {
  flex: none; font-size: 11.5px; font-variant-numeric: tabular-nums; color: var(--os-text-secondary);
  width: 40px; text-align: right;
}
.emain { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.emain b { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.emain small { font-size: 10.5px; color: var(--os-text-secondary); }
.del {
  flex: none; width: 22px; height: 22px; border-radius: 6px; border: none;
  background: transparent; color: var(--os-text-secondary); cursor: pointer;
  display: inline-flex; align-items: center; justify-content: center; opacity: 0; transition: opacity .1s;
}
.ev:hover .del { opacity: 1; }
.del:hover { background: rgba(229, 72, 77, .14); color: #e5484d; }
</style>
