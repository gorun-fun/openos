// useAccess.ts — 设置应用跨分区共享的数据源 composable（模块级单例状态）。
// 拆分自 SettingsApp.vue：应用目录（allApps）、角色与应用授权（/api/apps/access）、
// 角色目录与权限点（/api/roles）由「角色与权限 / 用户管理 / 网页应用 / 第三方应用 /
// 账户（角色名展示）」多个分区共用，这里收敛为一份状态与加载器；各分区私有的
// 状态与交互一律留在对应 Tab 组件内，勿往这里搬。
import { ref } from 'vue'
import { api, parseAppConfig, type AppDef } from '../../api/client'
import { useSession } from '../../stores/session'
import { t } from '../../i18n'
import { osConfirm } from '../../ui'

// ---- 角色目录 / 权限点目录（GET /api/roles 的视图模型；老服务端无此端点时优雅降级为纯应用授权）----
export interface RoleDefVM { key: string; label: string; builtin: boolean; managedBy: string; perms: string[]; apps: string[] }
export interface PermDefVM { code: string; group: string; label: string; managedBy: string }

// ---- 用户名展示辅助（账户 / 用户管理分区共用）----
// 用户名形如 UUID（宿主 SSO 体系以身份源 uid 自动开户）时，界面主位置只展示
// 显示名；UUID 仅在用户管理保留截断形式（悬停 title 看全量）
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
export function isUUID(u?: string) { return !!u && UUID_RE.test(u) }
export function shortUN(u?: string) { return u && UUID_RE.test(u) ? u.slice(0, 8) + '…' : (u || '') }

// ---- 共享状态（模块级单例：各 Tab 组件拿到的是同一份引用）----
/** 角色列表（含宿主注入的自定义角色，随 loadAccess 合并角色目录） */
export const roles = ref<string[]>([])
/** 应用授权矩阵：角色 → 应用 id 列表 */
export const access = ref<Record<string, string[]>>({})
/** 全量应用（GET /api/apps?all=1，含内置/锁定/隐藏，网页应用与第三方应用分区共用的列表底稿） */
export const allApps = ref<AppDef[]>([])
/** 角色显示名（GET /api/apps/access 的 labels，宿主注入） */
export const roleLabels = ref<Record<string, string>>({})
export const roleDefs = ref<RoleDefVM[]>([])
export const permDefs = ref<PermDefVM[]>([])
/** 角色权限点编辑态（含未保存修改；loadAccess 时从角色目录重建） */
export const rolePerms = ref<Record<string, string[]>>({})
// 宿主声明了角色编辑回写通道（GET /api/roles hostRolesWritable）：宿主托管角色
// 的编辑经 openos 转发宿主权威落库，设置页解锁可编辑
export const hostWritable = ref(false)
// 「角色与权限」分区的编辑态（所选角色 + 双区脏标记）也放这里：分区组件随 tab
// 切换卸载重建，留在组件内会在切走再切回后丢失未保存的勾选与所选角色——勾选
// 数据本就存在共享 access/rolePerms 里，脏标记必须与数据同生命周期
export const accessRole = ref('user')
export const accessDirty = ref(false)
export const permsDirty = ref(false)

/** 角色名本地化显示：优先宿主注入的显示名（GET /api/apps/access 的 labels，随
 *  loadAccess 加载），再回落内置角色映射（t() 渲染期求值，切换语言即时变），最后原样显示角色 key */
export function roleLabel(r: string) {
  const def = roleDefs.value.find((x) => x.key === r)
  return (def && def.label) || roleLabels.value[r] || t(`settings.role.${r}`)
}

/** 加载应用/角色目录与授权矩阵（端点要求 apps.manage，仅管理员）。调用方：
 *  ensureAccess（分区挂载按需加载）与各分区保存/删除后的强制刷新（loadAccess）。
 *  /api/roles 失败时优雅降级为纯应用授权。 */
export async function loadAccess() {
  const data = await api.get<{ roles: string[]; access: Record<string, string[]>; labels?: Record<string, string> }>('/api/apps/access')
  roles.value = data.roles
  access.value = data.access
  roleLabels.value = data.labels || {}
  accessRole.value = data.roles.includes(accessRole.value) ? accessRole.value : data.roles[1] || data.roles[0]
  allApps.value = await api.get<AppDef[]>('/api/apps?all=1')
  // 角色目录（含宿主声明角色）与权限点目录
  try {
    const cat = await api.get<{ roles: RoleDefVM[]; perms: PermDefVM[]; hostRolesWritable?: boolean }>('/api/roles')
    roleDefs.value = cat.roles || []
    permDefs.value = cat.perms || []
    hostWritable.value = !!cat.hostRolesWritable
    rolePerms.value = {}
    for (const r of roleDefs.value) {
      rolePerms.value[r.key] = [...(r.perms || [])]
      if (!roles.value.includes(r.key)) roles.value.push(r.key)
      if (r.label && !roleLabels.value[r.key]) roleLabels.value[r.key] = r.label
    }
  } catch { roleDefs.value = []; permDefs.value = []; hostWritable.value = false }
}

let accessLoaded = false
let accessLoading: Promise<void> | null = null
/** 按需加载（幂等）：原单组件在根 onMounted 对管理员一次性加载，拆分后由首个
 *  需要这份数据的分区挂载时触发；非管理员不拉（端点要求 apps.manage）。
 *  并发去重（快速切 tab 时多个分区同时挂载共用一次在途请求）；失败不标记已
 *  加载——下次进入分区仍会重试，对应原 watch(tab) 的「空则兜底拉取」。 */
export function ensureAccess(): Promise<void> {
  if (accessLoaded || useSession().user?.role !== 'admin') return Promise.resolve()
  if (!accessLoading) {
    accessLoading = loadAccess()
      .then(() => { accessLoaded = true })
      .finally(() => { accessLoading = null })
  }
  return accessLoading
}

// ---- 应用配置解析与展示辅助（网页应用 / 第三方应用分区共用）----
// config 在 API 里可能是字符串（旧数据双重编码）或嵌套对象（种子/新数据），统一解析
// （parseAppConfig 收敛三处同构实现；仅 undefined（缺省/解析失败）兜底为 {}，
//  null 等解析结果原样透传，与原先实现逐语义一致）
export const parseCfg = (a: AppDef): any => {
  const c = parseAppConfig(a)
  return c === undefined ? {} : c
}
export const isWebApp = (a: AppDef) => parseCfg(a).kind === 'web' || parseCfg(a).kind === 'proxy'
export const appType = (a: AppDef) => {
  const k = parseCfg(a).kind
  return !a.config ? t('settings.registry.tUnknown')
    : k === 'web' ? t('settings.registry.tWeb')
    : k === 'proxy' ? t('settings.registry.tProxy')
    : t('settings.registry.tSchema')
}

/** 删除第三方/网页应用（两分区列表共用）：确认后删除并刷新共享目录 */
export async function removeApp(id: string) {
  if (!(await osConfirm({ title: t('settings.registry.delTitle'), body: t('settings.registry.delBody', { id }), danger: true, confirmText: t('common.delete') }))) return
  await api.del(`/api/apps/${id}`)
  loadAccess()
}
