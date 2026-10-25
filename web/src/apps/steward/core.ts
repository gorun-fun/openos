// core.ts — grf-os 宿主 @grf/core 依赖的等价面（上游化移植）：API 客户端、
// 会话角色（isAdmin/session.loaded）与时间格式化。App 内一律从此处取，勿再
// 各文件手写。
import { api } from '../../api/client'
import { useSession } from '../../stores/session'

/** grf-os 宿主 API 客户端的 openos 等价物（路径带 /api 前缀，鉴权同 REST）。 */
export const grf = api

/** 当前会话是否 admin（角色晚就绪时为 false）。 */
export function isAdmin(): boolean {
  return useSession().user?.role === 'admin'
}

/** 会话就绪态（grf-os session.loaded 的等价物，供「会话晚就绪补一轮」用）。 */
export const session = {
  get loaded(): boolean {
    return !!useSession().user
  },
}

/** ISO 时间 → 「YYYY-MM-DD HH:mm[:ss]」展示。 */
export function fmtTime(v: unknown, withSec = true): string {
  if (!v) return '—'
  const s = String(v).replace('T', ' ')
  return withSec ? s.slice(0, 19) : s.slice(0, 16)
}
