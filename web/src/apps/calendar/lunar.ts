// 农历换算（公历 ⇄ 农历 1900-2100）：经典 lunisolar 速查表算法的 TypeScript 移植，
// 源自 MIT 协议的 calendar.js / solarlunar 一脉（表数据未改动），提供日历所需的
// 农历月日、干支年、生肖与农历/公历节日。仅做只读换算，无副作用。
//
//   lunarOf(new Date())            → { monthCn:'八月', dayCn:'廿二', yearGanZhi:'丙午', animal:'马', … }
//   cellSub(new Date())            → 月历格副行文字（节日优先，初一回显月名）

// 1900-2100 各年农历闰月/大小月位图（0xf=闰月月号；0x10000=闰月大月；bit 4..16=正月至十二月大月）
const LUNAR_INFO = [
  0x04bd8, 0x04ae0, 0x0a570, 0x054d5, 0x0d260, 0x0d950, 0x16554, 0x056a0, 0x09ad0, 0x055d2, // 1900-1909
  0x04ae0, 0x0a5b6, 0x0a4d0, 0x0d250, 0x1d255, 0x0b540, 0x0d6a0, 0x0ada2, 0x095b0, 0x14977, // 1910-1919
  0x04970, 0x0a4b0, 0x0b4b5, 0x06a50, 0x06d40, 0x1ab54, 0x02b60, 0x09570, 0x052f2, 0x04970, // 1920-1929
  0x06566, 0x0d4a0, 0x0ea50, 0x06e95, 0x05ad0, 0x02b60, 0x186e3, 0x092e0, 0x1c8d7, 0x0c950, // 1930-1939
  0x0d4a0, 0x1d8a6, 0x0b550, 0x056a0, 0x1a5b4, 0x025d0, 0x092d0, 0x0d2b2, 0x0a950, 0x0b557, // 1940-1949
  0x06ca0, 0x0b550, 0x15355, 0x04da0, 0x0a5b0, 0x14573, 0x052b0, 0x0a9a8, 0x0e950, 0x06aa0, // 1950-1959
  0x0aea6, 0x0ab50, 0x04b60, 0x0aae4, 0x0a570, 0x05260, 0x0f263, 0x0d950, 0x05b57, 0x056a0, // 1960-1969
  0x096d0, 0x04dd5, 0x04ad0, 0x0a4d0, 0x0d4d4, 0x0d250, 0x0d558, 0x0b540, 0x0b6a0, 0x195a6, // 1970-1979
  0x095b0, 0x049b0, 0x0a974, 0x0a4b0, 0x0b27a, 0x06a50, 0x06d40, 0x0af46, 0x0ab60, 0x09570, // 1980-1989
  0x04af5, 0x04970, 0x064b0, 0x074a3, 0x0ea50, 0x06b58, 0x05ac0, 0x0ab60, 0x096d5, 0x092e0, // 1990-1999
  0x0c960, 0x0d954, 0x0d4a0, 0x0da50, 0x07552, 0x056a0, 0x0abb7, 0x025d0, 0x092d0, 0x0cab5, // 2000-2009
  0x0a950, 0x0b4a0, 0x0baa4, 0x0ad50, 0x055d9, 0x04ba0, 0x0a5b0, 0x15176, 0x052b0, 0x0a930, // 2010-2019
  0x07954, 0x06aa0, 0x0ad50, 0x05b52, 0x04b60, 0x0a6e6, 0x0a4e0, 0x0d260, 0x0ea65, 0x0d530, // 2020-2029
  0x05aa0, 0x076a3, 0x096d0, 0x04afb, 0x04ad0, 0x0a4d0, 0x1d0b6, 0x0d250, 0x0d520, 0x0dd45, // 2030-2039
  0x0b5a0, 0x056d0, 0x055b2, 0x049b0, 0x0a577, 0x0a4b0, 0x0aa50, 0x1b255, 0x06d20, 0x0ada0, // 2040-2049
  0x14b63, 0x09370, 0x049f8, 0x04970, 0x064b0, 0x168a6, 0x0ea50, 0x06b20, 0x1a6c4, 0x0aae0, // 2050-2059
  0x092e0, 0x0d2e3, 0x0c960, 0x0d557, 0x0d4a0, 0x0da50, 0x05d55, 0x056a0, 0x0a6d0, 0x055d4, // 2060-2069
  0x052d0, 0x0a9b8, 0x0a950, 0x0b4a0, 0x0b6a6, 0x0ad50, 0x055a0, 0x0aba4, 0x0a5b0, 0x052b0, // 2070-2079
  0x0b273, 0x06930, 0x07337, 0x06aa0, 0x0ad50, 0x14b55, 0x04b60, 0x0a570, 0x054e4, 0x0d160, // 2080-2089
  0x0e968, 0x0d520, 0x0daa0, 0x16aa6, 0x056d0, 0x04ae0, 0x0a9d4, 0x0a4d0, 0x0d150, 0x0f252, // 2090-2099
  0x0d520, // 2100
]

const GAN = ['甲', '乙', '丙', '丁', '戊', '己', '庚', '辛', '壬', '癸']
const ZHI = ['子', '丑', '寅', '卯', '辰', '巳', '午', '未', '申', '酉', '戌', '亥']
const ANIMALS = ['鼠', '牛', '虎', '兔', '龙', '蛇', '马', '羊', '猴', '鸡', '狗', '猪']
const MONTH_CN = ['正', '二', '三', '四', '五', '六', '七', '八', '九', '十', '冬', '腊']
const DAY_NUM = ['日', '一', '二', '三', '四', '五', '六', '七', '八', '九', '十']
const DAY_TEN = ['初', '十', '廿', '卅']

// 农历节日（闰月不计节日）；公历节日另表。除夕经「次日为正月初一」判定。
const LUNAR_FESTIVALS: Record<string, string> = {
  '1-1': '春节', '1-15': '元宵节', '2-2': '龙抬头', '5-5': '端午节',
  '7-7': '七夕', '7-15': '中元节', '8-15': '中秋节', '9-9': '重阳节', '12-8': '腊八节',
}
const SOLAR_FESTIVALS: Record<string, string> = {
  '1-1': '元旦', '2-14': '情人节', '3-8': '妇女节', '3-12': '植树节', '4-1': '愚人节',
  '5-1': '劳动节', '5-4': '青年节', '6-1': '儿童节', '7-1': '建党节', '8-1': '建军节',
  '9-10': '教师节', '10-1': '国庆节', '12-24': '平安夜', '12-25': '圣诞节',
}

/** 农历 y 年闰月月号（0 = 无闰月）。 */
export function leapMonth(y: number): number { return LUNAR_INFO[y - 1900] & 0xf }

/** 农历 y 年闰月天数（无闰月为 0）。 */
export function leapDays(y: number): number {
  return leapMonth(y) ? (LUNAR_INFO[y - 1900] & 0x10000 ? 30 : 29) : 0
}

/** 农历 y 年 m 月（非闰月）天数。 */
export function monthDays(y: number, m: number): number {
  return LUNAR_INFO[y - 1900] & (0x10000 >> m) ? 30 : 29
}

/** 农历 y 年全年天数。 */
export function lYearDays(y: number): number {
  let sum = 348
  for (let i = 0x8000; i > 0x8; i >>= 1) sum += LUNAR_INFO[y - 1900] & i ? 1 : 0
  return sum + leapDays(y)
}

function dayCn(d: number): string {
  if (d === 10) return '初十'
  if (d === 20) return '二十'
  if (d === 30) return '三十'
  return DAY_TEN[Math.floor(d / 10)] + DAY_NUM[d % 10]
}

export interface LunarInfo {
  /** 农历年（以正月初一分界，非公历年） */
  year: number
  /** 农历月 1-12 */
  month: number
  /** 农历日 1-30 */
  day: number
  /** 当前月是否闰月 */
  isLeap: boolean
  /** 月名（如「八月」「闰四月」） */
  monthCn: string
  /** 日名（如「十五」「廿二」） */
  dayCn: string
  /** 干支年（如「丙午」） */
  yearGanZhi: string
  /** 生肖（如「马」） */
  animal: string
  /** 当天节日（农历/公历/除夕），无则缺省 */
  festival?: string
}

/** 公历日期 → 农历信息；超出 1900.1.31~2100.12.31 换算区间返回 null。 */
export function lunarOf(date: Date): LunarInfo | null {
  const y = date.getFullYear(), m = date.getMonth() + 1, d = date.getDate()
  if (y < 1900 || y > 2100) return null
  if (y === 1900 && (m < 2 || (m === 1 && d < 31))) return null
  // 以 1900-01-31（庚子年正月初一）为原点求天数偏移
  let offset = Math.floor((Date.UTC(y, m - 1, d) - Date.UTC(1900, 0, 31)) / 86400000)
  let i = 0, temp = 0
  for (i = 1900; i < 2101 && offset > 0; i++) { temp = lYearDays(i); offset -= temp }
  if (offset < 0) { offset += temp; i-- }
  const year = i
  const leap = leapMonth(year)
  let isLeap = false
  for (i = 1; i < 13 && offset > 0; i++) {
    if (leap > 0 && i === leap + 1 && !isLeap) {
      --i // 闰月插在 leap 月之后：先扣闰月
      isLeap = true
      temp = leapDays(year)
    } else {
      temp = monthDays(year, i)
    }
    if (isLeap && i === leap + 1) isLeap = false
    offset -= temp
  }
  if (offset === 0 && leap > 0 && i === leap + 1) {
    if (isLeap) isLeap = false
    else { isLeap = true; --i }
  }
  if (offset < 0) { offset += temp; --i }
  const month = i, day = offset + 1

  const info: LunarInfo = {
    year, month, day, isLeap,
    monthCn: (isLeap ? '闰' : '') + MONTH_CN[month - 1] + '月',
    dayCn: dayCn(day),
    yearGanZhi: GAN[(year - 4) % 10] + ZHI[(year - 4) % 12],
    animal: ANIMALS[(year - 4) % 12],
    festival: LUNAR_FESTIVALS[`${month}-${day}`] || SOLAR_FESTIVALS[`${m}-${d}`],
  }
  // 除夕：次日是（翌年）正月初一
  if (!info.festival) {
    const next = new Date(date); next.setDate(next.getDate() + 1)
    const nl = lunarOf(next)
    if (nl && nl.month === 1 && nl.day === 1) info.festival = '除夕'
  }
  return info
}

/** 月历格副行文字：节日（红）＞ 月名（初一回显）＞ 农历日名。 */
export function cellSub(date: Date): { text: string; red: boolean } {
  const l = lunarOf(date)
  if (!l) return { text: '', red: false }
  if (l.festival) return { text: l.festival, red: true }
  if (l.day === 1) return { text: l.monthCn, red: false }
  return { text: l.dayCn, red: false }
}
