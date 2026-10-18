// 日历数据仓库：日程事件（user_settings 的 calendar.events 键持久化，与 notes
// 同一模式，走 composables/useUserSetting 同步管线）+ 中国节假日
// （/api/calendar/holidays 按年加载）+ 提醒引擎（30s 轮询，到期经通知中心横幅
// 提醒；已触发记录存 localStorage 防重复——设备级偏好，不上云）。
// 日历应用、桌面日历小组件与提醒引擎三方共享本仓库。
import { defineStore } from 'pinia'
import { useSession } from './session'
import { useNotifications } from './notifications'
import { api } from '../api/client'
import { createUserSetting } from '../composables/useUserSetting'

/** 提醒提前量：none=不提醒，at=事件时刻（全天事件按 09:00） */
export type RemindOffset = 'none' | 'at' | '5m' | '15m' | '1h' | '1d'

export const REMIND_OPTIONS: { value: RemindOffset; label: string }[] = [
  { value: 'none', label: '不提醒' },
  { value: 'at', label: '事件时刻' },
  { value: '5m', label: '提前 5 分钟' },
  { value: '15m', label: '提前 15 分钟' },
  { value: '1h', label: '提前 1 小时' },
  { value: '1d', label: '提前 1 天' },
]

export interface CalEvent {
  id: string
  title: string
  /** YYYY-MM-DD */
  date: string
  /** HH:mm；缺省为全天事件（提醒按当天 09:00） */
  time?: string
  remind: RemindOffset
  note?: string
  created: number
}

export interface HolidayInfo { name: string; off: boolean }

/** 应用的自定义日标记（自由标签）：内存态不持久化，生命周期由来源应用自行维护。 */
export interface CalMark { appId: string; text: string; color?: string }

const EVENTS_KEY = 'calendar.events'
const KEY = () => `openos_calendar_${useSession().user?.username || 'default'}`
const FIRED_KEY = () => `openos_calendar_fired_${useSession().user?.username || 'default'}`

const OFFSET_MS: Record<Exclude<RemindOffset, 'none'>, number> = {
  at: 0, '5m': 5 * 60e3, '15m': 15 * 60e3, '1h': 3600e3, '1d': 86400e3,
}

/** 本地时区的今天（YYYY-MM-DD；toISOString 是 UTC，东八区会差 8 小时）。 */
export function todayStr(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

let loadedFor = ''
let remindTimer = 0

function parseEvents(raw: string | null | undefined): CalEvent[] {
  try {
    const arr = JSON.parse(raw || '[]')
    return Array.isArray(arr)
      ? arr.filter((e) => e && e.id && e.title && e.date).map((e: CalEvent) => ({
          ...e, remind: (REMIND_OPTIONS.some((o) => o.value === e.remind) ? e.remind : 'none') as RemindOffset,
        }))
      : []
  } catch { return [] }
}

const setting = createUserSetting<CalEvent[]>(EVENTS_KEY, {
  cacheKey: KEY,
  parse: parseEvents,
  serialize: (v) => JSON.stringify(v),
})

/** 事件的提醒触发时刻（ms）；全天事件按 09:00。 */
export function remindAtOf(e: CalEvent): number {
  const base = new Date(`${e.date}T${e.time || '09:00'}:00`).getTime()
  return base - OFFSET_MS[(e.remind === 'none' ? 'at' : e.remind) as Exclude<RemindOffset, 'none'>]
}

export const useCalendar = defineStore('calendar', {
  state: (): {
    events: CalEvent[]
    holidays: Record<string, HolidayInfo>
    /** 已请求过节假日的年份（含无数据年份，避免重复请求） */
    yearsAsked: number[]
    /** 自由日标记：日期 → 标签列表（应用经 mark()/OpenOS.calendar.mark 登记） */
    marks: Record<string, CalMark[]>
  } => ({ events: [], holidays: {}, yearsAsked: [], marks: {} }),

  getters: {
    byDate: (s) => (date: string) => s.events.filter((e) => e.date === date).sort((a, b) => (a.time || '99').localeCompare(b.time || '99')),
    holidayOf: (s) => (date: string) => s.holidays[date],
    marksOf: (s) => (date: string) => s.marks[date] || [],
    /** 未来的日程（含今天，按时间升序；给通知跳转等场景用）。 */
    upcoming: (s) => {
      const today = todayStr()
      return s.events.filter((e) => e.date >= today).sort((a, b) => (a.date + (a.time || '~')).localeCompare(b.date + (b.time || '~')))
    },
  },

  actions: {
    /** 加载日程与当年±1 的节假日（幂等；未登录时忽略）。 */
    load() {
      const user = useSession().user?.username
      if (!user || loadedFor === user) return
      loadedFor = user
      // 先用本地缓存即时上屏，再以服务端数据校正
      this.events = setting.readCache() ?? []
      void setting.sync((v, has) => { if (has && v) this.events = v })

      const y = new Date().getFullYear()
      this.loadYear(y - 1)
      this.loadYear(y)
      this.loadYear(y + 1)
    },

    /** 按需加载某年节假日（月历翻页跨年时调用；已请求过的年份跳过）。 */
    loadYear(year: number) {
      if (this.yearsAsked.includes(year)) return
      this.yearsAsked.push(year)
      void api.get<{ year: number; days: { name: string; date: string; isOffDay: boolean }[] }>(`/api/calendar/holidays?year=${year}`)
        .then((d) => {
          for (const day of d.days || []) this.holidays[day.date] = { name: day.name, off: day.isOffDay }
        })
        .catch(() => { /* 失败不重试到下次刷新；节假日标记缺省不展示 */ })
    },

    /** 保存：localStorage 即时写（缓存），服务端 400ms 防抖回写（权威）。 */
    save() {
      setting.commit(this.events)
    },

    addEvent(input: { title: string; date: string; time?: string; remind?: RemindOffset; note?: string }): CalEvent {
      const e: CalEvent = {
        id: Date.now().toString(36) + Math.random().toString(36).slice(2, 6),
        title: input.title.trim() || '（无标题）',
        date: input.date,
        time: input.time || undefined,
        remind: input.remind || 'none',
        note: input.note || undefined,
        created: Date.now(),
      }
      this.events.push(e)
      this.save()
      return e
    },
    updateEvent(id: string, patch: Partial<Omit<CalEvent, 'id' | 'created'>>) {
      const e = this.events.find((x) => x.id === id)
      if (!e) return
      Object.assign(e, patch)
      if (e.time === '') e.time = undefined
      this.save()
    },
    removeEvent(id: string) {
      this.events = this.events.filter((e) => e.id !== id)
      this.save()
    },

    // ---- 自由日标记（内存态，不落库）----
    /** 登记日标记：同 appId 同日替换（应用刷新时重复调用不堆叠）；dates 单日或批量。 */
    mark(dates: string | string[], opts: { appId?: string; text: string; color?: string }) {
      const m: CalMark = { appId: opts.appId || 'external', text: (opts.text || '').trim().slice(0, 12), color: opts.color }
      if (!m.text) return
      for (const d of Array.isArray(dates) ? dates : [dates]) {
        if (!/^\d{4}-\d{2}-\d{2}$/.test(d)) continue
        this.marks[d] = [...(this.marks[d] || []).filter((x) => x.appId !== m.appId), m]
      }
    },
    /** 移除日标记；不传 appId 清除该日全部标记。 */
    unmark(dates: string | string[], appId?: string) {
      for (const d of Array.isArray(dates) ? dates : [dates]) {
        const cur = this.marks[d]
        if (!cur) continue
        const next = appId ? cur.filter((x) => x.appId !== appId) : []
        if (next.length) this.marks[d] = next
        else delete this.marks[d]
      }
    },

    // ---- 提醒引擎：30s 轮询；触发窗口 90s（轮询间隔的 3 倍容错），
    //      已触发记录（事件 id → 触发时刻）存 localStorage，刷新/重开不重复弹。 ----
    startReminders() {
      if (remindTimer) return
      remindTimer = window.setInterval(() => this.tickReminders(), 30_000)
      this.tickReminders()
    },
    tickReminders() {
      if (!this.events.length) return
      let fired: Record<string, number> = {}
      try { fired = JSON.parse(localStorage.getItem(FIRED_KEY()) || '{}') } catch { /* 损坏则重置 */ }
      const now = Date.now()
      const nc = useNotifications()
      let changed = false
      for (const e of this.events) {
        if (e.remind === 'none') continue
        const at = remindAtOf(e)
        if (at <= now && now < at + 90_000 && fired[e.id] !== at) {
          const dayLabel = e.date === todayStr()
            ? '今天'
            : `${Number(e.date.slice(5, 7))}月${Number(e.date.slice(8, 10))}日`
          nc.notify({
            appId: 'calendar',
            title: '日程提醒',
            body: `${e.title} · ${dayLabel}${e.time ? ' ' + e.time : '（全天）'}`,
          })
          fired[e.id] = at
          changed = true
        }
      }
      // 清理 30 天前的触发记录与已删事件的记录
      const alive = new Set(this.events.map((e) => e.id))
      for (const [id, at] of Object.entries(fired)) {
        if (!alive.has(id) || now - at > 30 * 86400e3) { delete fired[id]; changed = true }
      }
      if (changed) {
        try { localStorage.setItem(FIRED_KEY(), JSON.stringify(fired)) } catch { /* 静默 */ }
      }
    },
  },
})
