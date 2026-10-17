<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { useDesktop } from '../../stores/desktop'
import { useApps } from '../../stores/apps'
import { api, type AppDef } from '../../api/client'
import { bus } from '../../bus/bus'
import { shell } from '../../shell'
import { t, setLocale, locale, LOCALES, LOCALE_SETTINGS_KEY, type Locale } from '../../i18n'
import { osConfirm, toast } from '../../ui'
import {
  OsButton, OsInput, OsSelect, OsToggle, OsPanel, OsTable, OsLayout, OsTabs, OsModal,
  OsEmpty, AppIcon,
} from '../../ui'

const props = defineProps<{ win: WinState }>()
const session = useSession()
const desktop = useDesktop()
const apps = useApps()
const isAdmin = session.user?.role === 'admin'
// SSO 模式（宿主组壳注入 login.mode='sso'）：身份与密码托管于宿主统一身份系统
const ssoLogin = computed(() => (shell.login.mode === 'sso' ? shell.login : null))
function openSsoPassword() {
  if (ssoLogin.value?.passwordUrl) window.open(ssoLogin.value.passwordUrl, '_blank', 'noopener')
}

// 角色名本地化显示：优先宿主注入的显示名（GET /api/apps/access 的 labels，随
// loadAccess 加载），再回落内置角色映射（t() 渲染期求值，切换语言即时变），最后原样显示角色 key
const roleLabels = ref<Record<string, string>>({})
function roleLabel(r: string) {
  const def = roleDefs.value.find((x) => x.key === r)
  return (def && def.label) || roleLabels.value[r] || t(`settings.role.${r}`)
}

const tab = ref('appearance')
// 桌面客户端专属：服务器设置（OpenOS 兼容端连接）。标记对象带 settingsUrl =
// 客户端本地设置页地址（设置服务降级时无该字段，入口随之隐藏）
const serverSettingsUrl = (window as any).__OPENOS_DESKTOP__?.settingsUrl as string | undefined
const items = computed(() => {
  // AI 服务全员可见（只读状态 + goder 启用引导），其余管理页仅管理员
  const base = [
    { key: 'appearance', label: t('settings.tab.appearance'), icon: '🎨' },
    { key: 'account', label: t('settings.tab.account'), icon: '👤' },
  ]
  if (serverSettingsUrl) base.push({ key: 'server', label: t('settings.tab.server'), icon: '🌐' })
  base.push({ key: 'ai', label: t('settings.tab.ai'), icon: '✦' })
  if (isAdmin) {
    base.push(
      { key: 'users', label: t('settings.tab.users'), icon: '👥' },
      { key: 'access', label: t('settings.tab.roles'), icon: '🔐' },
      { key: 'webapps', label: t('settings.tab.webapps'), icon: '🌐' },
      { key: 'registry', label: t('settings.tab.registry'), icon: '🧩' },
      { key: 'system', label: t('settings.tab.system'), icon: 'ℹ️' },
    )
  }
  return base
})

// ---- 外观 ----

// 语言：即时生效并写账号设置（多端一致；登录页切换仅本地缓存）
const curLocale = ref<Locale>(locale())
function onLocaleChange(v: string | undefined) {
  if (v !== 'zh-CN' && v !== 'en-US') return
  const l = v as Locale
  curLocale.value = l
  setLocale(l)
  void api.put('/api/settings', { key: LOCALE_SETTINGS_KEY, value: l }).catch(() => {})
}

// ---- 账户 ----
const oldPwd = ref(''), newPwd = ref(''), pwdMsg = ref('')
async function changePwd() {
  pwdMsg.value = ''
  try {
    await api.put('/api/users/password', { old: oldPwd.value, new: newPwd.value })
    pwdMsg.value = t('settings.acc.pwdDone')
    oldPwd.value = ''; newPwd.value = ''
  } catch (e: any) { pwdMsg.value = e.message }
}

// 用户名形如 UUID（宿主 SSO 体系以身份源 uid 自动开户）时，界面主位置只展示
// 显示名；UUID 仅在用户管理保留截断形式（悬停 title 看全量）
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
function shortUN(u?: string) { return u && UUID_RE.test(u) ? u.slice(0, 8) + '…' : (u || '') }

// 显示名自助编辑（PUT /api/users/display-name），保存后同步 session 供菜单栏等即刷
const displayName = ref(''), dnMsg = ref('')
const accountSub = computed(() => {
  const u = session.user
  if (!u) return ''
  const who = UUID_RE.test(u.username)
    ? [u.displayName || shortUN(u.username)]
    : [u.username, u.displayName].filter(Boolean)
  return who.concat(t('settings.roleWith', { role: roleLabel(u.role) })).join(' · ')
})
async function saveDisplayName() {
  dnMsg.value = ''
  const name = displayName.value.trim()
  if (!name) { dnMsg.value = t('settings.acc.dnEmpty'); return }
  try {
    await api.put('/api/users/display-name', { displayName: name })
    if (session.user) session.user.displayName = name
    dnMsg.value = t('settings.acc.saved')
  } catch (e: any) { dnMsg.value = e.message }
}

// ---- 用户管理 ----
interface UserRow { id: number; username: string; displayName: string; role: string; disabled: boolean; createdAt: string }
const users = ref<UserRow[]>([])
const showCreate = ref(false)
const nu = ref({ username: '', password: '', role: 'user', displayName: '' })
// 创建用户的角色下拉跟随系统实际角色（含宿主注入的自定义角色）
const roleOptions = computed(() => roles.value.map(r => ({ value: r, label: roleLabel(r) })))

async function loadUsers() { users.value = await api.get<UserRow[]>('/api/users') }

async function createUser() {
  try {
    await api.post('/api/users', nu.value)
    showCreate.value = false
    nu.value = { username: '', password: '', role: 'user', displayName: '' }
    loadUsers()
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}
async function setRole(row: any, role: string) {
  await api.put(`/api/users/${row.username}`, { role })
  loadUsers()
}
async function setDisabled(row: any, disabled: boolean) {
  try {
    await api.put(`/api/users/${row.username}`, { disabled })
    loadUsers()
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}
async function removeUser(row: any) {
  if (!(await osConfirm({ title: t('settings.users.delTitle'), body: t('settings.users.delBody', { name: row.displayName || row.username }), danger: true, confirmText: t('common.delete') }))) return
  try { await api.del(`/api/users/${row.username}`); loadUsers() } catch (e: any) { toast(e.message, { type: 'error' }) }
}
// 管理员在用户管理直接改显示名（与账户页自助接口互为补充，走 admin 通道）
const dnEdit = ref<{ row: any } | null>(null)
const dnEditVal = ref('')
function editDisplayName(row: any) {
  dnEdit.value = { row }
  dnEditVal.value = row.displayName || ''
}
async function saveDisplayNameEdit() {
  const name = dnEditVal.value.trim()
  if (!name) { toast(t('settings.acc.dnEmpty'), { type: 'error' }); return }
  const row = dnEdit.value!.row
  try {
    await api.put(`/api/users/${row.username}`, { displayName: name })
    row.displayName = name
    const su = session.user
    if (su && su.username === row.username) su.displayName = name // 管理员改的是自己时，菜单栏即刷
    dnEdit.value = null
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}

// ---- 角色与权限（应用授权 + 功能级权限点双区，P2 2026-10）----
const roles = ref<string[]>([])
const access = ref<Record<string, string[]>>({})
const allApps = ref<AppDef[]>([])
const accessRole = ref('user')
const accessDirty = ref(false)
// 角色目录与权限点目录（GET /api/roles；老服务端无此端点时优雅降级为纯应用授权）
interface RoleDefVM { key: string; label: string; builtin: boolean; managedBy: string; perms: string[]; apps: string[] }
interface PermDefVM { code: string; group: string; label: string; managedBy: string }
const roleDefs = ref<RoleDefVM[]>([])
const permDefs = ref<PermDefVM[]>([])
const rolePerms = ref<Record<string, string[]>>({})
const permsDirty = ref(false)

const isAdminUser = computed(() => session.user?.role === 'admin')
const activeRoleDef = computed(() => roleDefs.value.find((r) => r.key === accessRole.value))
// 宿主声明了角色编辑回写通道（GET /api/roles hostRolesWritable）：宿主托管角色
// 的编辑经 openos 转发宿主权威落库，设置页解锁可编辑
const hostWritable = ref(false)
/** 只读查看者不可编辑；宿主托管角色在宿主未声明回写通道前同样只读（权威在宿主库） */
const roleReadonly = computed(() => !isAdminUser.value || (activeRoleDef.value?.managedBy === 'host' && !hostWritable.value))
// 权限点按声明的 group（业务模块，宿主声明如「客房管理」）分组展示；组名为空回落
// 归属标签（宿主业务权限/系统权限），openos 内置组 "system" 映射为系统权限文案。
// 目录顺序即展示顺序（host 组在前、system 在后，组内按 code）。
const permGroupsUI = computed(() => {
  const out: { key: string; group: string; managedBy: string; perms: PermDefVM[] }[] = []
  const idx = new Map<string, (typeof out)[number]>()
  for (const p of permDefs.value) {
    const group = p.group && p.group !== 'system' ? p.group : ''
    const mk = p.managedBy + '\u0000' + group
    let g = idx.get(mk)
    if (!g) {
      g = { key: mk, group, managedBy: p.managedBy, perms: [] }
      idx.set(mk, g)
      out.push(g)
    }
    g.perms.push(p)
  }
  return out
})
const rolesDirty = computed(() => accessDirty.value || permsDirty.value)

async function loadAccess() {
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
function toggleApp(role: string, id: string) {
  const cur = access.value[role] || []
  access.value[role] = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]
  accessDirty.value = true
}
function togglePerm(role: string, code: string) {
  const cur = rolePerms.value[role] || []
  rolePerms.value[role] = cur.includes(code) ? cur.filter((x) => x !== code) : [...cur, code]
  permsDirty.value = true
}
async function saveAccess() {
  const role = accessRole.value
  if (roleDefs.value.length) {
    // 新通道：目录 + 权限点 + 应用授权一并保存（PUT /api/roles/{key}）
    const def = roleDefs.value.find((r) => r.key === role)
    await api.put(`/api/roles/${encodeURIComponent(role)}`, {
      label: roleLabel(role),
      perms: rolePerms.value[role] || [],
      apps: access.value[role] || [],
    })
    if (def) def.perms = [...(rolePerms.value[role] || [])]
  } else {
    // 老服务端兼容：仅应用授权
    await api.put('/api/apps/access', { role, apps: access.value[role] })
  }
  accessDirty.value = false
  permsDirty.value = false
  apps.refresh() // 自己桌面立即生效
}

// ---- 应用管理（第三方 + 内置网页应用的列表、弹窗式添加/编辑）----
const schemaText = ref('')
const regName = ref('')
const regId = ref('')
const regMsg = ref('')
const schemaDlg = ref(false)
const webName = ref('')
const webUrl = ref('')
const webId = ref('')
const webMsg = ref('')
const webDlg = ref(false)
// 代理嵌入开关：config.kind=proxy，目标站经服务端反代（可嵌入拒绝 iframe 的站点）
const webProxy = ref(false)
// 自定义请求头（仅代理模式生效——iframe 直嵌无法附带自定义头）：行式编辑，
// 保存时合并为 config.headers 对象（同名行后者覆盖前者）
const webHeaders = ref<{ name: string; value: string }[]>([])
function addWebHeader() { webHeaders.value.push({ name: '', value: '' }) }
function delWebHeader(i: number) { webHeaders.value.splice(i, 1) }
// 编辑中的应用（null = 添加模式；编辑时 ID 只读，提交走 upsert）
const editing = ref<AppDef | null>(null)

// config 在 API 里可能是字符串（旧数据双重编码）或嵌套对象（种子/新数据），统一解析
const parseCfg = (a: AppDef): any => {
  if (!a.config) return {}
  if (typeof a.config === 'string') { try { return JSON.parse(a.config) } catch { return {} } }
  return a.config
}
const isWebApp = (a: AppDef) => parseCfg(a).kind === 'web' || parseCfg(a).kind === 'proxy'
// 应用管理列表 = 声明式第三方应用（网页应用归「网页应用」分区管理）
const registryApps = computed(() => allApps.value.filter((a) => !a.builtin && !isWebApp(a)))
// 网页应用列表 = 全部网页应用（含内置/商店安装的 kind=web|proxy）
const webApps = computed(() => allApps.value.filter((a) => isWebApp(a)))
const appType = (a: AppDef) => {
  const k = parseCfg(a).kind
  return !a.config ? t('settings.registry.tUnknown')
    : k === 'web' ? t('settings.registry.tWeb')
    : k === 'proxy' ? t('settings.registry.tProxy')
    : t('settings.registry.tSchema')
}
const appUrl = (a: AppDef) => parseCfg(a).url || ''

// ---- 统一代理状态（/px/ 通用转发通道是否可用；OPENOS_PROXY=off 时关闭）----
const pxStatus = ref<{ enabled: boolean } | null>(null)
async function loadPxStatus() {
  try { pxStatus.value = await api.get<{ enabled: boolean }>('/api/proxy/status') } catch { pxStatus.value = null }
}

function openAddWeb() {
  editing.value = null
  webName.value = ''; webUrl.value = ''; webId.value = ''; webMsg.value = ''; webProxy.value = false
  webHeaders.value = []
  webDlg.value = true
}
function openEditWeb(a: AppDef) {
  editing.value = a
  webName.value = a.name
  webId.value = a.id
  webUrl.value = parseCfg(a).url || ''
  webProxy.value = parseCfg(a).kind === 'proxy'
  const hs = parseCfg(a).headers || {}
  webHeaders.value = Object.entries(hs).map(([name, value]) => ({ name, value: String(value) }))
  webMsg.value = ''
  webDlg.value = true
}
function openAddSchema() {
  editing.value = null
  regId.value = ''; regName.value = ''; schemaText.value = ''; regMsg.value = ''
  schemaDlg.value = true
}
function openEditSchema(a: AppDef) {
  editing.value = a
  regId.value = a.id
  regName.value = a.name
  schemaText.value = a.config || ''
  regMsg.value = ''
  schemaDlg.value = true
}

const EXAMPLE_SCHEMA = JSON.stringify({
  title: '团队公告板',
  dataSource: {
    kind: 'static',
    items: [
      { title: 'v1.0 发布', body: 'OpenOS 桌面系统正式上线', author: 'admin', date: '周一' },
      { title: '新功能', body: '支持第三方声明式应用注册', author: 'goder', date: '周三' },
      { title: '修复', body: '窗口缩放在小屏下的边界问题', author: 'user', date: '周五' },
    ],
  },
  views: [
    { type: 'cards', title: '卡片', fields: [
      { key: 'title', label: '标题', primary: true },
      { key: 'body', label: '内容' },
      { key: 'author', label: '作者' },
    ] },
    { type: 'table', title: '列表', columns: [
      { key: 'title', label: '标题' }, { key: 'author', label: '作者' }, { key: 'date', label: '日期' },
    ] },
  ],
}, null, 2)

async function registerApp() {
  regMsg.value = ''
  try {
    JSON.parse(schemaText.value) // 校验
    await api.post('/api/apps', {
      id: editing.value ? editing.value.id : regId.value, name: regName.value, icon: 'default',
      category: '第三方', config: schemaText.value, width: 760, height: 560,
    })
    regMsg.value = t('settings.acc.saved')
    regId.value = ''; regName.value = ''
    editing.value = null
    schemaDlg.value = false
    loadAccess()
  } catch (e: any) { regMsg.value = t('settings.registry.saveFail', { msg: e.message }) }
}
async function removeApp(id: string) {
  if (!(await osConfirm({ title: t('settings.registry.delTitle'), body: t('settings.registry.delBody', { id }), danger: true, confirmText: t('common.delete') }))) return
  await api.del(`/api/apps/${id}`)
  loadAccess()
}

// ---- 网页应用注册（iframe 嵌入，config.kind=web，渲染走 WebApp 容器）----
async function registerWebApp() {
  webMsg.value = ''
  const raw = webUrl.value.trim()
  let u: URL
  try { u = new URL(raw) } catch { webMsg.value = t('settings.registry.badUrl'); return }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') { webMsg.value = t('settings.registry.proto'); return }
  // 与服务端 sanitizeID 同规则（小写、剔除 [a-z0-9-_.] 以外字符）——图标等
  // 派生 URL 必须用最终落库 ID，否则会指向不存在的应用（曾因点号被剥出过 404）
  const id = editing.value ? editing.value.id
    : (webId.value.trim() || 'web-' + u.hostname).toLowerCase().replace(/[^a-z0-9-_.]/g, '')
  if (!id) { webMsg.value = t('settings.registry.emptyId'); return }
  // config.headers 仅代理模式写入（直嵌模式带不上自定义头，存了也是死配置）
  const cfg: Record<string, any> = { kind: webProxy.value ? 'proxy' : 'web', url: u.href }
  if (webProxy.value) {
    const hs = webHeaders.value.filter((h) => h.name.trim())
    if (hs.length) cfg.headers = Object.fromEntries(hs.map((h) => [h.name.trim(), h.value]))
  }
  try {
    await api.post('/api/apps', {
      id, name: webName.value.trim() || u.hostname,
      icon: webProxy.value ? `/app/${id}/${u.origin.replace('://', '/')}/favicon.ico` : `${u.origin}/favicon.ico`,
      category: '网络', config: JSON.stringify(cfg), width: 1000, height: 660,
    })
    webMsg.value = t('settings.registry.webSaved')
    webName.value = ''; webUrl.value = ''; webId.value = ''; webHeaders.value = []
    editing.value = null
    webDlg.value = false
    loadAccess()
  } catch (e: any) { webMsg.value = t('settings.registry.saveFail', { msg: e.message }) }
}

// ---- AI 服务（OpenOS 唯一的 AI 应用是 goder，此处配置与其模型配置联动） ----
const aiCfg = ref({ baseUrl: '', apiKey: '', model: '' })
const aiConfigured = ref(false)
const aiMsg = ref('')
const aiTesting = ref(false)
const aiTestResult = ref<any>(null)
// goder 状态（available=false = 未部署/被停用；全员可见的启用引导依据）
const aiStatus = ref<any>(null)
const goderDisabled = computed(() => !!aiStatus.value?.goder?.disabled)
const goderUnavailable = computed(() => !!aiStatus.value?.goder && !aiStatus.value.goder.available)
const goderIdle = computed(() => {
  const g = aiStatus.value?.goder
  return !!g && g.available && !g.disabled && !g.running
})
const goderSynced = computed(() => Number(aiStatus.value?.goder?.aiSyncedUsers) || 0)

async function loadAI() {
  try {
    aiStatus.value = await api.get<any>('/api/ai/status')
  } catch { /* ignore */ }
  if (!isAdmin) return
  try {
    const c = await api.get<{ baseUrl: string; apiKey: string; model: string; configured: boolean }>('/api/ai/config')
    aiCfg.value = { baseUrl: c.baseUrl, apiKey: c.apiKey, model: c.model }
    aiConfigured.value = c.configured
  } catch { /* ignore */ }
}
async function saveAI() {
  aiMsg.value = ''
  try {
    await api.put('/api/ai/config', aiCfg.value)
    aiConfigured.value = true
    aiMsg.value = t('settings.ai.saved')
    await loadAI()
  } catch (e: any) { aiMsg.value = e.message }
}
async function testAI() {
  aiTesting.value = true
  aiTestResult.value = null
  try {
    aiTestResult.value = await api.post<any>('/api/ai/test')
  } catch (e: any) {
    aiTestResult.value = { ok: false, error: e.message }
  } finally {
    aiTesting.value = false
  }
}

// ---- 系统信息 ----
const stats = ref<any>(null)
const audit = ref<any[]>([])
async function loadSystem() {
  try {
    const [s, a] = await Promise.all([
      api.get<any>('/api/stats'),
      api.get<any[]>('/api/audit'),
    ])
    stats.value = s
    audit.value = a
  } catch { /* ignore */ }
}

onMounted(() => {
  loadAI()
  if (isAdmin) { loadUsers(); loadAccess(); loadPxStatus() }
})
watch(tab, (v) => {
  if (v === 'system') loadSystem()
  if (v === 'account') displayName.value = session.user?.displayName || ''
  if (v === 'users') loadUsers() // 账户页/别处改名后切回本页即时同步
  if (v === 'webapps' && !allApps.value.length) loadAccess() // 直达本分区时兜底拉应用列表
})
</script>

<template>
  <OsLayout :win="props.win" :items="items" :active="tab" @select="tab = $event">
    <!-- 外观 -->
    <template v-if="tab === 'appearance'">
      <h2 class="sec">{{ t('settings.appearance') }}</h2>
      <div class="card">
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ t('settings.ap.dark') }}</div>
            <div class="g-sub">{{ t('settings.ap.darkSub') }}</div>
          </div>
          <OsToggle :model-value="desktop.dark" @update:model-value="(v: any) => desktop.setDark(!!v)" />
        </div>
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ t('settings.ap.dockHide') }}</div>
            <div class="g-sub">{{ t('settings.ap.dockHideSub') }}</div>
          </div>
          <OsToggle :model-value="desktop.dockAutoHide" @update:model-value="(v: any) => desktop.setDockAutoHide(!!v)" />
        </div>
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ t('settings.ap.lang') }}</div>
            <div class="g-sub">{{ t('settings.ap.langSub') }}</div>
          </div>
          <OsSelect :model-value="curLocale" :options="LOCALES.map((l) => ({ value: l.value, label: l.label }))" @update:model-value="onLocaleChange" />
        </div>
      </div>
      <h2 class="sec">{{ t('settings.ap.wallpaper') }}</h2>
      <div class="wallpapers">
        <button
          v-for="(w, i) in desktop.wallpapers" :key="i"
          class="wall" :class="{ active: desktop.wallpaper === i }"
          :style="{ background: w }" :title="t('settings.ap.wallItem', { n: i + 1 })" @click="desktop.setWallpaper(i)"
        ><span v-if="desktop.wallpaper === i" class="wall-check">✓</span></button>
      </div>
      <h2 class="sec">{{ t('settings.ap.about') }}</h2>
      <div class="card">
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ shell.brand.logo }} {{ shell.brand.name }}</div>
            <div class="g-sub">{{ shell.brand.sub }}</div>
          </div>
          <!-- 宿主注入的产品版本（brand.version，独立部署时缺省不显示） -->
          <span v-if="shell.brand.version" class="ver">{{ shell.brand.version }}</span>
        </div>
      </div>
    </template>

    <!-- 账户 -->
    <template v-else-if="tab === 'account'">
      <h2 class="sec">{{ t('settings.acc.profile') }}</h2>
      <div class="card">
        <div class="group">
          <div class="g-text"><div class="g-title">{{ t('settings.acc.account') }}</div><div class="g-sub">{{ accountSub }}</div></div>
        </div>
        <div class="group pwd">
          <div class="g-text"><div class="g-title">{{ t('settings.acc.displayName') }}</div></div>
          <OsInput v-model="displayName" :placeholder="t('settings.acc.dnPh')" @keyup.enter="saveDisplayName" />
        </div>
        <div class="group">
          <div class="g-text" />
          <OsButton variant="primary" @click="saveDisplayName">{{ t('settings.acc.saveDn') }}</OsButton>
          <span v-if="dnMsg" class="msg">{{ dnMsg }}</span>
        </div>
      </div>
      <h2 class="sec">{{ t('settings.acc.security') }}</h2>
      <!-- SSO 模式：密码托管于统一身份系统，本地改密表单不渲染；宿主可注入
           passwordUrl 提供「前往统一身份改密」跳转（新开页签，桌面不丢状态） -->
      <div v-if="ssoLogin" class="card">
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ t('settings.acc.ssoTitle') }}</div>
            <div class="g-sub">{{ t('settings.acc.ssoSub') }}</div>
          </div>
          <OsButton v-if="ssoLogin.passwordUrl" variant="primary" @click="openSsoPassword">
            {{ ssoLogin.passwordLabel || t('settings.acc.ssoBtn') }}
          </OsButton>
        </div>
      </div>
      <div v-else class="card">
        <div class="group pwd">
          <div class="g-text"><div class="g-title">{{ t('settings.acc.oldPwd') }}</div></div>
          <OsInput v-model="oldPwd" type="password" :placeholder="t('settings.acc.oldPwd')" />
        </div>
        <div class="group pwd">
          <div class="g-text"><div class="g-title">{{ t('settings.acc.newPwd') }}</div></div>
          <OsInput v-model="newPwd" type="password" :placeholder="t('settings.acc.pwdPh')" />
        </div>
        <div class="group">
          <div class="g-text" />
          <OsButton variant="primary" @click="changePwd">{{ t('settings.acc.changePwd') }}</OsButton>
          <span v-if="pwdMsg" class="msg">{{ pwdMsg }}</span>
        </div>
      </div>
    </template>

    <!-- 用户管理 -->
    <template v-else-if="tab === 'users'">
      <div class="panel-actions">
        <OsButton variant="primary" size="sm" @click="showCreate = true">{{ t('settings.users.create') }}</OsButton>
      </div>
      <OsTable :columns="[
        { key: 'displayName', label: t('settings.users.colDisplayName'), width: '24%' },
        { key: 'username', label: t('settings.users.colUsername'), width: '14%' },
        { key: 'disabled', label: t('settings.users.colStatus'), width: '12%' },
        { key: 'createdAt', label: t('settings.users.colCreatedAt'), width: '22%' },
      ]" :rows="users">
        <template #cell-displayName="{ row }">
          <span class="dname" :class="{ unset: !row.displayName }">{{ row.displayName || t('settings.users.unset') }}</span>
          <button class="mini-edit" :title="t('settings.users.editDn')" @click="editDisplayName(row)">✎</button>
        </template>
        <template #cell-username="{ value }">
          <span class="umono" :title="value">{{ shortUN(value) }}</span>
        </template>
        <template #cell-disabled="{ value }">
          <span class="ustate" :class="{ off: value }">{{ value ? t('settings.users.disabled') : t('settings.users.active') }}</span>
        </template>
        <template #ops="{ row }">
          <OsSelect
            :model-value="row.role" :options="roleOptions" style="padding:3px 6px;font-size:12px"
            @update:model-value="(v: any) => setRole(row, String(v))"
          />
          <!-- 按角色保护（宿主体系下管理员用户名多为 UUID，字面量 'admin' 判定会漏保护
               服务账号）；服务端另有「最后一名管理员」兜底 -->
          <OsButton size="sm" :variant="row.disabled ? 'primary' : 'plain'" :disabled="row.role === 'admin'" @click="setDisabled(row, !row.disabled)">
            {{ row.disabled ? t('settings.users.enable') : t('settings.users.disable') }}
          </OsButton>
          <OsButton size="sm" variant="danger" :disabled="row.role === 'admin'" @click="removeUser(row)">{{ t('common.delete') }}</OsButton>
        </template>
      </OsTable>
      <p class="hint">{{ t('settings.users.hint') }}</p>

      <!-- 弹窗：修改显示名（管理员通道，用户自己走账户页自助） -->
      <OsModal v-if="dnEdit" :title="t('settings.users.editDn')" :ok-text="t('common.save')" @ok="saveDisplayNameEdit" @cancel="dnEdit = null">
        <div class="dlg-form">
          <div><label>{{ t('settings.users.fUser') }}</label><div class="dlg-ro">{{ dnEdit.row.displayName || shortUN(dnEdit.row.username) }}（{{ shortUN(dnEdit.row.username) }}）</div></div>
          <div><label>{{ t('settings.users.fNewDn') }}</label><OsInput v-model="dnEditVal" :placeholder="t('settings.acc.dnPh')" @keyup.enter="saveDisplayNameEdit" /></div>
        </div>
      </OsModal>

      <!-- 弹窗：新建用户（SSO 模式下密码留空 → 服务端分配随机值，登录统一走宿主交换） -->
      <OsModal v-if="showCreate" :title="t('settings.users.newTitle')" :ok-text="t('settings.users.okCreate')" @ok="createUser" @cancel="showCreate = false">
        <div class="dlg-form">
          <div><label>{{ t('settings.users.fUsername') }}</label><OsInput v-model="nu.username" placeholder="zhangsan" mono /></div>
          <div><label>{{ t('settings.users.fDisplayName') }}</label><OsInput v-model="nu.displayName" placeholder="张三" /></div>
          <div><label>{{ t('settings.users.fRole') }}</label><OsSelect v-model="nu.role" :options="roleOptions" /></div>
          <div v-if="!ssoLogin"><label>{{ t('settings.users.fPassword') }}</label><OsInput v-model="nu.password" type="password" :placeholder="t('settings.users.pwdPh')" /></div>
          <p v-else class="hint">{{ t('settings.users.ssoHint') }}</p>
        </div>
      </OsModal>
    </template>

    <!-- 角色与权限（应用授权升级版：权限点 + 应用授权双区） -->
    <template v-else-if="tab === 'access'">
      <div class="panel-actions">
        <OsTabs
          :tabs="roles.map(r => ({ key: r, label: roleLabel(r) }))"
          v-model="accessRole"
        />
        <span class="grow" />
        <OsButton variant="primary" size="sm" :disabled="roleReadonly || !rolesDirty" @click="saveAccess">
          {{ rolesDirty && !roleReadonly ? t('settings.roles.dirty') : t('settings.roles.saved') }}
        </OsButton>
      </div>
      <p v-if="activeRoleDef?.managedBy === 'host' && !hostWritable" class="hint">{{ t('settings.roles.hostManaged') }}</p>
      <p v-else-if="activeRoleDef?.managedBy === 'host'" class="hint">{{ t('settings.roles.hostEditable') }}</p>
      <p v-else-if="!isAdminUser" class="hint">{{ t('settings.roles.readonly') }}</p>
      <div class="roles-grid">
        <!-- 左区：功能级权限点（admin 直通提示 + 按业务模块分组勾选） -->
        <div class="roles-perms">
          <h3 class="sec">{{ t('settings.roles.perms') }}</h3>
          <p v-if="accessRole === 'admin'" class="hint">{{ t('settings.roles.allPerms') }}</p>
          <template v-else>
            <div v-for="g in permGroupsUI" :key="g.key">
              <p v-if="g.perms.length" class="perm-group">
                {{ g.group || t(g.managedBy === 'host' ? 'settings.roles.hostGroup' : 'settings.roles.systemGroup') }}
              </p>
              <label v-for="p in g.perms" :key="p.code" class="perm-item" :class="{ ro: roleReadonly }">
                <input
                  type="checkbox"
                  :disabled="roleReadonly"
                  :checked="(rolePerms[accessRole] || []).includes(p.code)"
                  @change="togglePerm(accessRole, p.code)"
                />
                <span class="an">{{ p.label || p.code }}</span>
                <span class="aid">{{ p.code }}</span>
              </label>
            </div>
            <p v-if="!permDefs.length" class="hint">{{ t('settings.roles.noPerms') }}</p>
          </template>
        </div>
        <!-- 右区：应用授权卡片网格（原 access 分区） -->
        <div class="roles-apps">
          <h3 class="sec">{{ t('settings.roles.apps') }}</h3>
          <div class="access-grid">
            <label v-for="a in allApps" :key="a.id" class="access-item" :class="{ ro: roleReadonly }">
              <input
                type="checkbox"
                :disabled="roleReadonly"
                :checked="(access[accessRole] || []).includes(a.id)"
                @change="toggleApp(accessRole, a.id)"
              />
              <span class="an">{{ a.name }}</span>
              <span class="aid">{{ a.id }} · {{ a.category }}</span>
            </label>
          </div>
          <p class="hint">{{ t('settings.access.hint') }}</p>
        </div>
      </div>
    </template>

    <!-- 网页应用：统一代理的网页嵌入管理（kind=web 直嵌 / kind=proxy 服务端反代） -->
    <template v-else-if="tab === 'webapps'">
      <div class="reg-head">
        <h2 class="sec" style="margin: 0">{{ t('settings.webapps.count', { n: webApps.length }) }}</h2>
        <span class="grow" />
        <OsButton variant="primary" size="sm" @click="openAddWeb">{{ t('settings.webapps.add') }}</OsButton>
      </div>

      <!-- 统一代理状态：开启时网页应用可用应用代理嵌入拒绝 iframe 的站点，
           且网络请求类应用（网络请求/行情等）默认经 /px/ 通用转发发送 -->
      <div class="card" :class="{ off: pxStatus && !pxStatus.enabled }">
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ pxStatus?.enabled ? t('settings.webapps.pxOn') : t('settings.webapps.pxOff') }}</div>
            <div class="g-sub">{{ pxStatus?.enabled ? t('settings.webapps.pxOnSub') : t('settings.webapps.pxOffSub') }}</div>
          </div>
          <span class="px-dot" :class="{ on: pxStatus?.enabled }">{{ pxStatus?.enabled ? 'ON' : 'OFF' }}</span>
        </div>
      </div>

      <OsPanel v-if="webApps.length" class="reg-list">
        <div v-for="a in webApps" :key="a.id" class="reg-item">
          <AppIcon :app="a.id" :icon="a.icon" :size="34" />
          <div class="reg-meta">
            <span class="an">{{ a.name }}<em v-if="a.builtin || a.locked" class="builtin-badge">{{ t('settings.registry.builtin') }}</em></span>
            <span class="aid">{{ a.id }} · {{ appType(a) }} · {{ appUrl(a) }}</span>
          </div>
          <span class="grow" />
          <OsButton size="sm" @click="bus.emit('sys:open', { app: a.id })">{{ t('settings.registry.open') }}</OsButton>
          <!-- 内置应用配置随版本发布、宿主锁定（locked）应用由宿主种子维护，均不可编辑/删除 -->
          <OsButton v-if="!a.builtin && !a.locked" size="sm" @click="openEditWeb(a)">{{ t('settings.registry.edit') }}</OsButton>
          <OsButton v-if="!a.builtin && !a.locked" size="sm" variant="danger" @click="removeApp(a.id)">{{ t('common.delete') }}</OsButton>
        </div>
      </OsPanel>
      <OsEmpty v-else :text="t('settings.webapps.empty')" />

      <!-- 弹窗：添加 / 编辑网页应用（iframe 嵌入，可选服务端代理） -->
      <OsModal v-if="webDlg" :title="editing ? t('settings.registry.webTitleEdit') : t('settings.registry.webTitleAdd')" :ok-text="editing ? t('common.save') : t('settings.registry.addBtn')"
               @ok="registerWebApp" @cancel="webDlg = false; editing = null">
        <div class="dlg-form">
          <div><label>{{ t('settings.registry.fName') }}</label><OsInput v-model="webName" placeholder="企业邮箱" /></div>
          <div><label>{{ t('settings.registry.fUrl') }}</label><OsInput v-model="webUrl" placeholder="https://example.com/" mono /></div>
          <div v-if="!editing"><label>{{ t('settings.registry.fId') }}</label><OsInput v-model="webId" placeholder="web-mail" mono /></div>
          <label class="px-toggle"><input type="checkbox" v-model="webProxy" /> {{ t('settings.registry.proxy') }}</label>
          <!-- 自定义请求头：仅代理模式生效（iframe 直嵌无法附带自定义头） -->
          <div v-if="webProxy" class="hdr-list">
            <label>{{ t('settings.registry.headers') }}</label>
            <div v-for="(h, i) in webHeaders" :key="i" class="hdr-row">
              <OsInput v-model="h.name" placeholder="Header-Name" mono />
              <OsInput v-model="h.value" :placeholder="t('settings.registry.hValue')" mono />
              <OsButton size="sm" variant="danger" @click="delWebHeader(i)">{{ t('common.delete') }}</OsButton>
            </div>
            <OsButton size="sm" @click="addWebHeader">{{ t('settings.registry.addHeader') }}</OsButton>
          </div>
          <p class="hint">{{ t('settings.registry.webHint') }}</p>
          <span class="msg">{{ webMsg }}</span>
        </div>
      </OsModal>
    </template>

    <!-- 第三方应用：声明式应用（JSON Schema）注册，添加/编辑走弹窗 -->
    <template v-else-if="tab === 'registry'">
      <div class="reg-head">
        <h2 class="sec" style="margin: 0">{{ t('settings.registry.count', { n: registryApps.length }) }}</h2>
        <span class="grow" />
        <OsButton size="sm" @click="openAddSchema">{{ t('settings.registry.addSchema') }}</OsButton>
      </div>

      <OsPanel v-if="registryApps.length" class="reg-list">
        <div v-for="a in registryApps" :key="a.id" class="reg-item">
          <AppIcon :app="a.id" :icon="a.icon" :size="34" />
          <div class="reg-meta">
            <span class="an">{{ a.name }}<em v-if="a.builtin || a.locked" class="builtin-badge">{{ t('settings.registry.builtin') }}</em></span>
            <span class="aid">{{ a.id }} · {{ appType(a) }}</span>
          </div>
          <span class="grow" />
          <OsButton size="sm" @click="bus.emit('sys:open', { app: a.id })">{{ t('settings.registry.open') }}</OsButton>
          <!-- 内置应用配置随版本发布、宿主锁定（locked）应用由宿主种子维护，均不可编辑/删除 -->
          <OsButton v-if="!a.builtin && !a.locked" size="sm" @click="isWebApp(a) ? openEditWeb(a) : openEditSchema(a)">{{ t('settings.registry.edit') }}</OsButton>
          <OsButton v-if="!a.builtin && !a.locked" size="sm" variant="danger" @click="removeApp(a.id)">{{ t('common.delete') }}</OsButton>
        </div>
      </OsPanel>
      <OsEmpty v-else :text="t('settings.registry.empty')" />

      <!-- 弹窗：注册 / 编辑声明式应用（JSON Schema） -->
      <OsModal v-if="schemaDlg" :title="editing ? t('settings.registry.schemaTitleEdit') : t('settings.registry.schemaTitleAdd')" :ok-text="editing ? t('common.save') : t('settings.registry.okRegister')"
               @ok="registerApp" @cancel="schemaDlg = false; editing = null">
        <div class="dlg-form">
          <div class="two">
            <div><label>{{ t('settings.registry.fIdEn') }}</label><OsInput v-model="regId" placeholder="notice-board" mono :disabled="!!editing" /></div>
            <div><label>{{ t('settings.registry.fName') }}</label><OsInput v-model="regName" placeholder="公告板" /></div>
          </div>
          <label>{{ t('settings.registry.fSchema') }}
            <OsButton size="sm" @click="schemaText = EXAMPLE_SCHEMA">{{ t('settings.registry.example') }}</OsButton>
          </label>
          <textarea v-model="schemaText" class="schema" spellcheck="false" :placeholder="t('settings.registry.schemaPh')" />
          <span class="msg">{{ regMsg }}</span>
          <p class="hint">{{ t('settings.registry.schemaHint') }}</p>
        </div>
      </OsModal>
    </template>
    <!-- 服务器（桌面客户端专属：内嵌客户端本地设置页，改连 OpenOS 兼容端） -->
    <template v-else-if="tab === 'server'">
      <iframe :src="serverSettingsUrl" class="server-frame" :title="t('settings.tab.server')" />
    </template>

    <!-- AI 服务（goder 联动） -->
    <template v-else-if="tab === 'ai'">
      <!-- goder 未启用：全员提示找管理员，管理员另给部署指引 -->
      <div v-if="goderDisabled || goderUnavailable" class="card">
        <div class="group">
          <div class="g-text">
            <div class="g-title">{{ t('settings.ai.notEnabled') }}</div>
            <div class="g-sub">{{ t('settings.ai.notEnabledSub') }}</div>
          </div>
        </div>
        <div v-if="isAdmin" class="group">
          <div class="g-text">
            <div class="g-title">{{ t('settings.ai.adminGuide') }}</div>
            <div class="g-sub g-code">
              {{ goderDisabled ? t('settings.ai.goderOff') : t('settings.ai.noBinary') }}
            </div>
          </div>
        </div>
      </div>

      <template v-else>
        <h2 class="sec">{{ t('settings.ai.title') }}</h2>
        <div class="card">
          <div class="group">
            <div class="g-text">
              <div class="g-title">{{ t('settings.ai.status') }}</div>
              <div class="g-sub">
                <template v-if="aiConfigured">
                  {{ t('settings.ai.okLine', { model: aiCfg.model || aiStatus?.model, provider: aiStatus?.goder?.aiProvider || 'OpenOS' }) }}<template v-if="goderSynced">{{ t('settings.ai.okSync', { n: goderSynced }) }}</template>
                </template>
                <template v-else>{{ t('settings.ai.off') }}</template>
                <template v-if="goderIdle">{{ t('settings.ai.idle') }}</template>
              </div>
            </div>
          </div>
          <div v-if="!isAdmin" class="group">
            <div class="g-text">
              <div class="g-title">{{ t('settings.ai.managed') }}</div>
              <div class="g-sub">{{ t('settings.ai.managedSub') }}</div>
            </div>
          </div>
        </div>

        <OsPanel v-if="isAdmin">
          <div class="form">
            <div class="ai-status" :class="{ on: aiConfigured }">
              {{ aiConfigured ? t('settings.ai.bannerOk') : t('settings.ai.bannerOff') }}
            </div>
            <div class="two">
              <div>
                <label>Base URL</label>
                <OsInput v-model="aiCfg.baseUrl" placeholder="https://open.bigmodel.cn/api/paas/v4" mono />
              </div>
              <div>
                <label>{{ t('settings.ai.model') }}</label>
                <OsInput v-model="aiCfg.model" placeholder="glm-4-flash" mono />
              </div>
            </div>
            <label>{{ t('settings.ai.key') }}</label>
            <OsInput v-model="aiCfg.apiKey" type="password" placeholder="sk-…" mono />
            <div class="row-actions">
              <OsButton variant="primary" @click="saveAI">{{ t('settings.ai.save') }}</OsButton>
              <OsButton :disabled="aiTesting" @click="testAI">{{ aiTesting ? t('settings.ai.testing') : t('settings.ai.test') }}</OsButton>
              <span class="msg">{{ aiMsg }}</span>
            </div>
            <div v-if="aiTestResult" class="test-result" :class="{ ok: aiTestResult.ok }">
              <template v-if="aiTestResult.ok">
                {{ t('settings.ai.testOk', { ms: aiTestResult.latencyMs, model: aiTestResult.model, reply: aiTestResult.reply }) }}
              </template>
              <template v-else>✕ {{ aiTestResult.error }}</template>
            </div>
            <p class="hint">{{ t('settings.ai.hint') }}</p>
          </div>
        </OsPanel>
      </template>
    </template>

    <!-- 系统信息 -->
    <template v-else-if="tab === 'system'">
      <h2 class="sec">{{ t('settings.sys.status') }}</h2>
      <OsPanel>
        <div class="stats-grid" v-if="stats">
          <div class="stat"><b>{{ stats.version }}</b><span>{{ t('settings.sys.version') }}</span></div>
          <div class="stat"><b>{{ stats.userCount }}</b><span>{{ t('settings.sys.users') }}</span></div>
          <div class="stat"><b>{{ stats.appCount }}</b><span>{{ t('settings.sys.apps') }}</span></div>
          <div class="stat"><b>{{ stats.fileCount }}</b><span>{{ t('settings.sys.files') }}</span></div>
          <div class="stat"><b>{{ (stats.storageUsed / 1048576).toFixed(1) }} MB</b><span>{{ t('settings.sys.storage') }}</span></div>
          <div class="stat"><b>{{ (stats.quotaPerUser / 1048576) }} MB</b><span>{{ t('settings.sys.quota') }}</span></div>
        </div>
        <p class="hint">{{ t('settings.sys.startedAt', { time: stats?.startedAt || '—' }) }}</p>
      </OsPanel>
      <h2 class="sec">{{ t('settings.sys.audit') }}</h2>
      <OsPanel>
        <div class="audit-list">
          <div v-for="a in audit" :key="a.id" class="audit-row">
            <span class="au-time">{{ a.createdAt }}</span>
            <span class="au-user">{{ a.username }}</span>
            <span class="au-act">{{ a.action }}</span>
            <span class="au-detail">{{ a.detail }}</span>
          </div>
          <div v-if="!audit.length" class="hint">{{ t('settings.sys.noAudit') }}</div>
        </div>
      </OsPanel>
    </template>
  </OsLayout>
</template>

<style scoped>
/* macOS 系统设置式分组：卡片外的组标题 + 卡片内的“左说明右控件”行 */
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.sec:not(:first-child) { margin-top: 18px; }
.card {
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 10px; overflow: hidden;
}
.card .group { display: flex; align-items: center; gap: 14px; padding: 11px 14px; min-height: 44px; }
.card .group + .group { border-top: 1px solid var(--os-border); }
.g-text { flex: 1; min-width: 0; }
/* 表单行：label 不参与收缩（防中文逐字竖排），输入框占剩余空间并限宽 */
.card .group.pwd .g-text { flex: none; white-space: nowrap; }
.card .group.pwd .os-input { flex: 1; max-width: 300px; }
.g-title { font-size: 13px; }
.g-sub { font-size: 11.5px; color: var(--os-text-secondary); margin-top: 1px; }
.g-sub.g-code { font-family: var(--os-mono); word-break: break-all; }
.card .group .msg { font-size: 12px; color: var(--os-text-secondary); }

/* 侧栏彩色图标块（SF Symbols 彩色方块语言） */
:deep(.os-sidebar button .ic) { color: #fff; }
:deep(.os-sidebar button[data-key='appearance'] .ic) { background: linear-gradient(180deg, #64d2ff, #0a84ff); }
:deep(.os-sidebar button[data-key='account'] .ic) { background: linear-gradient(180deg, #68d391, #30a14e); }
:deep(.os-sidebar button[data-key='users'] .ic) { background: linear-gradient(180deg, #8e8e93, #636366); }
:deep(.os-sidebar button[data-key='access'] .ic) { background: linear-gradient(180deg, #bf5af2, #8944ab); }
:deep(.os-sidebar button[data-key='registry'] .ic) { background: linear-gradient(180deg, #5ac8fa, #0a84ff); }
:deep(.os-sidebar button[data-key='ai'] .ic) { background: linear-gradient(180deg, #ff375f, #d70015); }
:deep(.os-sidebar button[data-key='server'] .ic) { background: linear-gradient(180deg, #5e5ce6, #3634a3); }
:deep(.os-sidebar button[data-key='system'] .ic) { background: linear-gradient(180deg, #a2a2a7, #77777c); }

/* 服务器设置：客户端本地设置页占满内容区（高度不够时保底 460px） */
.server-frame {
  display: block; width: 100%; height: 100%; min-height: 460px;
  border: none; border-radius: 8px;
}

.wallpapers { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; }
.wall {
  height: 72px; border-radius: 10px; border: 2px solid transparent; cursor: pointer;
  position: relative; transition: transform .12s; box-shadow: inset 0 0 0 1px rgba(0, 0, 0, .06);
}
.wall:hover { transform: scale(1.02); }
.wall.active { border-color: var(--os-accent); box-shadow: 0 0 0 3px rgba(10,132,255,.25); }
.wall-check {
  position: absolute; right: 6px; bottom: 5px; width: 20px; height: 20px; border-radius: 50%;
  background: var(--os-accent); color: #fff; font-size: 12px; line-height: 20px; text-align: center;
}
/* 关于卡片右侧的版本号（宿主 brand.version 注入） */
.ver { font-family: var(--os-mono); font-size: 12px; color: var(--os-text-secondary); white-space: nowrap; }
.row { display: flex; gap: 20px; align-items: center; }
.form { display: flex; flex-direction: column; gap: 8px; }
.form label { font-size: 12px; color: var(--os-text-secondary); display: flex; gap: 8px; align-items: center; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.msg { font-size: 12px; margin-left: 8px; color: var(--os-text-secondary); }
.ustate {
  display: inline-block; font-size: 12px; padding: 1px 10px; border-radius: 999px;
  color: #1d8a3e; background: rgba(48,209,88,.12);
}
/* 用户管理：显示名 + 行内改名小按钮；用户名列等宽字体（UUID 截断形态） */
.dname.unset { color: var(--os-text-secondary); }
.mini-edit {
  margin-left: 6px; padding: 0 5px; border: none; background: transparent;
  color: var(--os-text-secondary); font-size: 12px; line-height: 18px;
  border-radius: 4px; cursor: pointer;
}
.mini-edit:hover { color: var(--os-accent, #2563eb); background: rgba(37,99,235,.08); }
.umono { font-family: var(--os-mono); font-size: 12px; }
.ustate.off { color: #b25b00; background: rgba(255,159,10,.14); }
.panel-actions { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.grow { flex: 1; }
.hint { font-size: 12px; color: var(--os-text-secondary); margin-top: 10px; }
.access-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 8px; }
.access-item {
  display: flex; align-items: center; gap: 8px; background: var(--os-panel-solid);
  border: 1px solid var(--os-border); border-radius: 9px; padding: 9px 12px; cursor: pointer;
}
.access-item:hover { border-color: var(--os-border-strong); }
.access-item.ro, .perm-item.ro { cursor: not-allowed; opacity: .7; }
/* 角色与权限双区布局：左权限点 / 右应用授权（窄屏自动叠放） */
.roles-grid { display: grid; grid-template-columns: minmax(280px, 2fr) 3fr; gap: 18px; align-items: start; }
@media (max-width: 900px) { .roles-grid { grid-template-columns: 1fr; } }
.roles-perms h3.sec, .roles-apps h3.sec { margin: 2px 0 10px; }
.perm-group { font-size: 12px; font-weight: 700; color: var(--os-text-secondary); margin: 12px 0 6px; }
.perm-item {
  display: flex; align-items: center; gap: 8px; padding: 7px 10px; border-radius: 9px;
  border: 1px solid transparent; cursor: pointer;
}
.perm-item:hover { border-color: var(--os-border); background: var(--os-panel-solid); }
.perm-item .aid { flex: none; }
.an { font-size: 13px; font-weight: 600; }
.aid { font-size: 11px; color: var(--os-text-secondary); }
.schema {
  width: 100%; min-height: 200px; resize: vertical; border-radius: 9px; padding: 10px;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  font-family: var(--os-mono); font-size: 12px; line-height: 1.5;
}
.reg-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
/* 统一代理状态卡：开启为常规卡片，关闭时描边提示 */
.card.off { border-color: #b25b00; }
.px-dot {
  font-family: var(--os-mono); font-size: 11px; font-weight: 700; flex: none;
  padding: 2px 8px; border-radius: 999px; color: #b25b00; background: rgba(255, 159, 10, .14);
}
.px-dot.on { color: #1d8a3e; background: rgba(48, 209, 88, .12); }
.card.off { margin-bottom: 12px; }
.reg-list { padding: 4px 8px; }
.reg-item { display: flex; align-items: center; gap: 12px; padding: 8px 4px; border-bottom: 1px solid var(--os-border); }
.reg-item:last-child { border-bottom: none; }
.reg-item:hover { background: var(--os-hover); }
.reg-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.reg-meta .an, .reg-meta .aid { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.builtin-badge {
  font-style: normal; font-size: 10px; font-weight: 600; margin-left: 6px; padding: 1px 5px;
  border-radius: 4px; color: var(--os-text-secondary); background: var(--os-hover);
  border: 1px solid var(--os-border); vertical-align: 1px;
}
/* 弹窗表单：覆盖 OsModal 内容默认居中，改左对齐纵排 */
.dlg-form { text-align: left; display: flex; flex-direction: column; gap: 8px; width: 460px; max-width: 70vw; }
.dlg-form label { font-size: 12px; color: var(--os-text-secondary); display: flex; align-items: center; gap: 8px; }
.dlg-form .hint { margin-top: 2px; }
.dlg-form .px-toggle { cursor: pointer; user-select: none; }
.dlg-form .hdr-list { display: flex; flex-direction: column; gap: 6px; }
.dlg-form .hdr-row { display: flex; gap: 6px; align-items: center; }
.dlg-form .hdr-row > :first-child { flex: 2; }
.dlg-form .hdr-row > :nth-child(2) { flex: 3; }
.dlg-form .schema { max-height: 32vh; }
.dlg-form .dlg-ro { font-size: 13px; padding: 7px 0 1px; }

.ai-status { font-size: 13px; color: #b25b00; background: rgba(255,159,10,.12); padding: 8px 12px; border-radius: 8px; }
.ai-status.on { color: #1d8a3e; background: rgba(48,209,88,.12); }
.row-actions { display: flex; gap: 8px; align-items: center; }
.test-result { font-size: 12.5px; color: #e5484d; background: rgba(229,72,77,.08); padding: 8px 12px; border-radius: 8px; }
.test-result.ok { color: #1d8a3e; background: rgba(48,209,88,.1); }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(130px, 1fr)); gap: 10px; }
.stat { background: var(--os-sidebar); border-radius: 10px; padding: 12px; text-align: center; }
.stat b { display: block; font-size: 20px; }
.stat span { font-size: 11.5px; color: var(--os-text-secondary); }
.audit-list { max-height: 300px; overflow-y: auto; font-size: 12px; }
.audit-row { display: flex; gap: 10px; padding: 5px 4px; border-bottom: 1px solid var(--os-border); align-items: baseline; }
.audit-row:last-child { border-bottom: none; }
.au-time { color: var(--os-text-secondary); flex: none; width: 130px; font-variant-numeric: tabular-nums; }
.au-user { flex: none; width: 80px; font-weight: 600; }
.au-act { flex: none; width: 110px; color: var(--os-accent); font-family: var(--os-mono); font-size: 11px; }
.au-detail { color: var(--os-text-secondary); word-break: break-all; }
</style>
