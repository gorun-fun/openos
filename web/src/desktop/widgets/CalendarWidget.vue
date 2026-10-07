<script setup lang="ts">
// 日历桌面小组件：今日大字（周几/日期/农历/节日）+ 迷你月历（节假日红字、
// 右上角「休/班」角标、今日高亮）。点任意日期打开日历应用并定位到该天；
// 浏览的年月经 update() 持久化在小组件条目上（刷新后停留在原月份）。
import { computed } from 'vue'
import { useWindows } from '../../stores/windows'
import { useCalendar, todayStr } from '../../stores/calendar'
import { lunarOf } from '../../apps/calendar/lunar'

const props = defineProps<{ widget: Record<string, any>; update: (patch: Record<string, any>) => void }>()

const windows = useWindows()
const cal = useCalendar()
cal.load()

const now = new Date()
const ym = computed(() => {
  const v = props.widget.ym
  return v && typeof v.y === 'number' && typeof v.m === 'number' ? v : { y: now.getFullYear(), m: now.getMonth() }
})
function nav(delta: number) {
  const { y, m } = ym.value
  props.update({ ym: m + delta < 0 ? { y: y - 1, m: 11 } : m + delta > 11 ? { y: y + 1, m: 0 } : { y, m: m + delta } })
}

const DAY_KEYS = ['一', '二', '三', '四', '五', '六', '日']
const fmt = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`

const todayLunar = lunarOf(now)
const todaySub = todayLunar?.festival || (todayLunar?.day === 1 ? todayLunar.monthCn : todayLunar?.dayCn) || ''
const todayHoliday = cal.holidayOf(todayStr())

/** 迷你月历 42 格（周一起，含前后月补位灰显）。 */
const cells = computed(() => {
  const { y, m } = ym.value
  const first = new Date(y, m, 1)
  const start = new Date(first)
  start.setDate(start.getDate() - ((first.getDay() + 6) % 7))
  const out: { key: string; d: number; inMonth: boolean }[] = []
  for (let i = 0; i < 42; i++) {
    const d = new Date(start)
    d.setDate(start.getDate() + i)
    out.push({ key: fmt(d), d: d.getDate(), inMonth: d.getMonth() === m })
  }
  return out
})

function openAt(key: string) {
  const d = new Date(`${key}T00:00:00`)
  cal.loadYear(d.getFullYear())
  windows.open('calendar', { date: key })
}

/** 迷你格提示：节假日/节日/自定义标签（自由日标记）拼接。 */
function cellTitle(key: string) {
  const parts: string[] = []
  const h = cal.holidayOf(key)
  if (h) parts.push(`${h.name}（${h.off ? '休' : '班'}）`)
  const fest = lunarOf(new Date(key + 'T00:00:00'))?.festival
  if (fest) parts.push(fest)
  const marks = cal.marksOf(key).map((m) => m.text)
  if (marks.length) parts.push(marks.join('、'))
  return parts.join(' · ') || key
}
</script>

<template>
  <div class="calw">
    <!-- 今日 -->
    <div class="head" title="打开日历" @click="openAt(todayStr())">
      <div class="wk">{{ ['周日', '周一', '周二', '周三', '周四', '周五', '周六'][now.getDay()] }}</div>
      <div class="dnum">{{ now.getDate() }}</div>
      <div class="sub">
        <span v-if="todayHoliday" class="hol" :class="{ off: todayHoliday.off }">{{ todayHoliday.name }}{{ todayHoliday.off ? ' · 休' : ' · 班' }}</span>
        <span v-else :class="{ fest: todayLunar?.festival }">{{ todaySub }}</span>
      </div>
    </div>

    <!-- 迷你月历 -->
    <div class="mini">
      <div class="mhead">
        <button class="nav" title="上一月" @click.stop="nav(-1)">‹</button>
        <b>{{ ym.y }}年{{ ym.m + 1 }}月</b>
        <button class="nav" title="下一月" @click.stop="nav(1)">›</button>
      </div>
      <div class="wk-row"><span v-for="w in DAY_KEYS" :key="w">{{ w }}</span></div>
      <div class="mgrid">
        <button
          v-for="c in cells" :key="c.key"
          class="mc" :class="{ out: !c.inMonth }"
          :title="cellTitle(c.key)"
          @click="openAt(c.key)"
        >
          <span class="n" :class="{
            today: c.key === todayStr(),
            offday: cal.holidayOf(c.key)?.off,
            workday: cal.holidayOf(c.key) && !cal.holidayOf(c.key)!.off,
            wknd: [0, 6].includes(new Date(c.key + 'T00:00:00').getDay()),
          }">{{ c.d }}</span>
          <i v-if="cal.holidayOf(c.key)" class="mk" :class="cal.holidayOf(c.key)!.off ? 'xiu' : 'ban'">{{ cal.holidayOf(c.key)!.off ? '休' : '班' }}</i>
          <!-- 底部一行小点：蓝=日程，彩色=自由日标记 -->
          <span v-if="cal.byDate(c.key).length || cal.marksOf(c.key).length" class="dots">
            <i v-if="cal.byDate(c.key).length" class="evdot" />
            <i v-for="m in cal.marksOf(c.key).slice(0, 3)" :key="m.appId" :style="{ background: m.color || '#ffb84d' }" />
          </span>
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.calw { width: 246px; padding: 0 0 10px; user-select: none; }

.head { display: flex; flex-direction: column; align-items: center; padding: 14px 10px 10px; cursor: pointer; }
.head:hover .dnum { opacity: .85; }
.wk { color: #ff6b61; font-size: 13px; font-weight: 700; letter-spacing: 2px; }
.dnum { font-size: 46px; font-weight: 250; line-height: 1.15; font-variant-numeric: tabular-nums; }
.sub { font-size: 12px; color: rgba(255, 255, 255, .78); }
.sub .hol.off { color: #6ee08a; font-weight: 600; }
.sub .hol { font-weight: 600; }
.sub .fest { color: #6ee08a; }

.mini { padding: 0 10px; }
.mhead { display: flex; align-items: center; justify-content: space-between; padding: 0 2px 6px; border-top: 1px solid rgba(255, 255, 255, .12); }
.mhead b { font-size: 12px; }
.nav {
  border: none; background: transparent; color: rgba(255, 255, 255, .8); font-size: 15px;
  width: 22px; height: 22px; border-radius: 6px; cursor: pointer; line-height: 1;
}
.nav:hover { background: rgba(255, 255, 255, .14); }

.wk-row { display: grid; grid-template-columns: repeat(7, 1fr); }
.wk-row span { text-align: center; font-size: 10px; color: rgba(255, 255, 255, .55); }
.wk-row span:first-child, .wk-row span:last-child { color: rgba(255, 130, 120, .8); }

.mgrid { display: grid; grid-template-columns: repeat(7, 1fr); gap: 1px; }
.mc {
  position: relative; border: none; background: transparent; cursor: pointer;
  height: 27px; display: flex; align-items: center; justify-content: center; border-radius: 7px;
}
.mc:hover { background: rgba(255, 255, 255, .12); }
.mc .n { font-size: 11.5px; color: #f2f3f7; font-variant-numeric: tabular-nums; }
.mc.out .n { opacity: .32; }
.mc .n.wknd { color: rgba(255, 140, 130, .92); }
.mc .n.offday { color: #ff7b6f; font-weight: 700; }
.mc .n.workday { color: rgba(242, 243, 247, .75); }
.mc .n.today {
  background: #ff5f57; color: #fff; font-weight: 700;
  min-width: 19px; height: 19px; border-radius: 50%; display: inline-flex; align-items: center; justify-content: center;
}
/* 节假日角标（格右上角）：休=绿、班=红（提示补班），与日历应用的 .mark 同语义 */
.mk {
  position: absolute; top: 0; right: 1px; font-size: 7.5px; font-style: normal; line-height: 1;
  padding: 1px 2px; border-radius: 3px; color: #fff;
}
.mk.xiu { background: rgba(45, 164, 78, .95); }
.mk.ban { background: rgba(255, 95, 87, .85); }
/* 底部小点行：蓝=日程圆点，彩色=自由日标记（最多 3 枚） */
.dots { position: absolute; bottom: 1.5px; left: 0; right: 0; display: flex; gap: 2px; justify-content: center; }
.dots i { width: 3px; height: 3px; border-radius: 50%; }
.dots .evdot { background: #6fb6ff; }
</style>
