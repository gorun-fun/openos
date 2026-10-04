// perms.ts — 功能级权限点的统一门控（P2 2026-10）。
//
// 语义与服务端 auth.RequirePerm / store.HasPermJudge 严格一致：
//   - admin 角色直通全部；
//   - 权限集含 `*` 通配全部；
//   - 其余按精确 code 匹配。
//
// 用法：
//   import { can } from '@openos/framework'
//   <OsButton v-if="can('room.view')" />          // 推荐（响应式）
//   <OsButton v-perm="'room.view'" />             // 简写指令（元素级移除）
// 权限快照随会话走（/api/auth/me、login/refresh 响应的 perms 字段），
// 宿主接管模式下由宿主 exchange 时注入。
import type { App, Directive } from 'vue'
import { useSession } from '../stores/session'

/** 当前登录用户是否持有某功能权限点。 */
export function can(perm: string): boolean {
  const s = useSession()
  const u = s.user
  if (!u) return false
  if (u.role === 'admin') return true
  const perms = u.perms || []
  return perms.includes('*') || perms.includes(perm)
}

/** v-perm="'room.view'"：无权限时整个元素不渲染（挂载期与更新期都判）。 */
export const vPerm: Directive<HTMLElement, string | undefined> = {
  mounted(el, binding) {
    if (binding.value && !can(binding.value)) el.parentNode?.removeChild(el)
  },
  updated(el, binding) {
    if (binding.value && !can(binding.value) && el.parentNode) el.parentNode.removeChild(el)
  },
}

/** createOpenOS 时安装 v-perm 指令。 */
export function installPermDirective(app: App) {
  app.directive('perm', vPerm)
}
