<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { useDesktop } from '../../stores/desktop'
import { useApps } from '../../stores/apps'
import { api, type AppDef } from '../../api/client'
import { bus } from '../../bus/bus'
import { shell } from '../../shell'
import {
  OsButton, OsInput, OsSelect, OsToggle, OsPanel, OsTable, OsLayout, OsTabs, OsModal,
  OsEmpty, AppIcon,
} from '../../ui'

const props = defineProps<{ win: WinState }>()
const session = useSession()
const desktop = useDesktop()
const apps = useApps()
const isAdmin = session.user?.role === 'admin'

// 角色名中文显示：优先宿主注入的显示名（GET /api/apps/access 的 labels，随
// loadAccess 加载），再回落内置角色映射，最后原样显示角色 key
const ROLE_LABELS: Record<string, string> = { admin: '管理员', user: '用户', guest: '访客' }
const roleLabels = ref<Record<string, string>>({})
function roleLabel(r: string) { return roleLabels.value[r] || ROLE_LABELS[r] || r }

const tab = ref('appearance')
// 桌面客户端专属：服务器设置（OpenOS 兼容端连接）。标记对象带 settingsUrl =
// 客户端本地设置页地址（设置服务降级时无该字段，入口随之隐藏）
const serverSettingsUrl = (window as any).__OPENOS_DESKTOP__?.settingsUrl as string | undefined
const items = computed(() => {
  // AI 服务全员可见（只读状态 + goder 启用引导），其余管理页仅管理员
  const base = [
    { key: 'appearance', label: '外观', icon: '🎨' },
    { key: 'account', label: '账户', icon: '👤' },
  ]
  if (serverSettingsUrl) base.push({ key: 'server', label: '服务器', icon: '🌐' })
  base.push({ key: 'ai', label: 'AI 服务', icon: '✦' })
  if (isAdmin) {
    base.push(
      { key: 'users', label: '用户管理', icon: '👥' },
      { key: 'access', label: '应用授权', icon: '🔐' },
      { key: 'registry', label: '应用管理', icon: '🧩' },
      { key: 'system', label: '系统信息', icon: 'ℹ️' },
    )
  }
  return base
})

// ---- 外观 ----

// ---- 账户 ----
const oldPwd = ref(''), newPwd = ref(''), pwdMsg = ref('')
async function changePwd() {
  pwdMsg.value = ''
  try {
    await api.put('/api/users/password', { old: oldPwd.value, new: newPwd.value })
    pwdMsg.value = '✓ 密码已修改'
    oldPwd.value = ''; newPwd.value = ''
  } catch (e: any) { pwdMsg.value = e.message }
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
  } catch (e: any) { alert(e.message) }
}
async function setRole(row: any, role: string) {
  await api.put(`/api/users/${row.username}`, { role })
  loadUsers()
}
async function setDisabled(row: any, disabled: boolean) {
  try {
    await api.put(`/api/users/${row.username}`, { disabled })
    loadUsers()
  } catch (e: any) { alert(e.message) }
}
async function removeUser(row: any) {
  if (!confirm(`删除用户 ${row.username}？其文件、设置与磁盘数据将被清理。`)) return
  try { await api.del(`/api/users/${row.username}`); loadUsers() } catch (e: any) { alert(e.message) }
}

// ---- 应用授权矩阵 ----
const roles = ref<string[]>([])
const access = ref<Record<string, string[]>>({})
const allApps = ref<AppDef[]>([])
const accessRole = ref('user')
const accessDirty = ref(false)

async function loadAccess() {
  const data = await api.get<{ roles: string[]; access: Record<string, string[]>; labels?: Record<string, string> }>('/api/apps/access')
  roles.value = data.roles
  access.value = data.access
  roleLabels.value = data.labels || {}
  accessRole.value = data.roles.includes(accessRole.value) ? accessRole.value : data.roles[1] || data.roles[0]
  allApps.value = await api.get<AppDef[]>('/api/apps?all=1')
}
function toggleApp(role: string, id: string) {
  const cur = access.value[role] || []
  access.value[role] = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]
  accessDirty.value = true
}
async function saveAccess() {
  await api.put('/api/apps/access', { role: accessRole.value, apps: access.value[accessRole.value] })
  accessDirty.value = false
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
// 列表 = 全部第三方应用 + 内置网页应用（内置项只读，配置随版本发布）
const registryApps = computed(() => allApps.value.filter((a) => !a.builtin || isWebApp(a)))
const appType = (a: AppDef) => {
  const k = parseCfg(a).kind
  return !a.config ? '未知' : k === 'web' ? '网页应用' : k === 'proxy' ? '代理网页' : '声明式'
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
    regMsg.value = '✓ 已保存'
    regId.value = ''; regName.value = ''
    editing.value = null
    schemaDlg.value = false
    loadAccess()
  } catch (e: any) { regMsg.value = '保存失败：' + e.message }
}
async function removeApp(id: string) {
  if (!confirm(`删除第三方应用 ${id}？`)) return
  await api.del(`/api/apps/${id}`)
  loadAccess()
}

// ---- 网页应用注册（iframe 嵌入，config.kind=web，渲染走 WebApp 容器）----
async function registerWebApp() {
  webMsg.value = ''
  const raw = webUrl.value.trim()
  let u: URL
  try { u = new URL(raw) } catch { webMsg.value = '地址格式不正确'; return }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') { webMsg.value = '地址需为 http(s):// 开头'; return }
  // 与服务端 sanitizeID 同规则（小写、剔除 [a-z0-9-_.] 以外字符）——图标等
  // 派生 URL 必须用最终落库 ID，否则会指向不存在的应用（曾因点号被剥出过 404）
  const id = editing.value ? editing.value.id
    : (webId.value.trim() || 'web-' + u.hostname).toLowerCase().replace(/[^a-z0-9-_.]/g, '')
  if (!id) { webMsg.value = '应用 ID 不能为空'; return }
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
    webMsg.value = '✓ 已保存，Dock 即时可见（新应用需在「应用授权」中给角色开放）'
    webName.value = ''; webUrl.value = ''; webId.value = ''; webHeaders.value = []
    editing.value = null
    webDlg.value = false
    loadAccess()
  } catch (e: any) { webMsg.value = '保存失败：' + e.message }
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
    aiMsg.value = '✓ 已保存并同步到 goder'
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
  if (isAdmin) { loadUsers(); loadAccess() }
})
watch(tab, (t) => { if (t === 'system') loadSystem() })
</script>

<template>
  <OsLayout :win="props.win" :items="items" :active="tab" @select="tab = $event">
    <!-- 外观 -->
    <template v-if="tab === 'appearance'">
      <h2 class="sec">外观</h2>
      <div class="card">
        <div class="group">
          <div class="g-text">
            <div class="g-title">深色模式</div>
            <div class="g-sub">菜单栏、窗口与控件使用深色外观</div>
          </div>
          <OsToggle :model-value="desktop.dark" @update:model-value="(v: any) => desktop.setDark(!!v)" />
        </div>
        <div class="group">
          <div class="g-text">
            <div class="g-title">最大化时自动隐藏 Dock</div>
            <div class="g-sub">窗口铺满全屏，鼠标移到屏幕底部时 Dock 滑出</div>
          </div>
          <OsToggle :model-value="desktop.dockAutoHide" @update:model-value="(v: any) => desktop.setDockAutoHide(!!v)" />
        </div>
      </div>
      <h2 class="sec">壁纸</h2>
      <div class="wallpapers">
        <button
          v-for="(w, i) in desktop.wallpapers" :key="i"
          class="wall" :class="{ active: desktop.wallpaper === i }"
          :style="{ background: w }" :title="`壁纸 ${i + 1}`" @click="desktop.setWallpaper(i)"
        ><span v-if="desktop.wallpaper === i" class="wall-check">✓</span></button>
      </div>
      <h2 class="sec">关于</h2>
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
      <h2 class="sec">登录与安全</h2>
      <div class="card">
        <div class="group">
          <div class="g-text"><div class="g-title">账户</div><div class="g-sub">{{ session.user?.username }} · {{ session.user?.displayName }} · 角色 {{ roleLabel(session.user?.role || '') }}</div></div>
        </div>
        <div class="group">
          <div class="g-text"><div class="g-title">原密码</div></div>
          <OsInput v-model="oldPwd" type="password" placeholder="原密码" />
        </div>
        <div class="group">
          <div class="g-text"><div class="g-title">新密码</div></div>
          <OsInput v-model="newPwd" type="password" placeholder="至少 6 位" />
        </div>
        <div class="group">
          <div class="g-text" />
          <OsButton variant="primary" @click="changePwd">修改密码</OsButton>
          <span v-if="pwdMsg" class="msg">{{ pwdMsg }}</span>
        </div>
      </div>
    </template>

    <!-- 用户管理 -->
    <template v-else-if="tab === 'users'">
      <div class="panel-actions">
        <OsButton variant="primary" size="sm" @click="showCreate = true">＋ 新建用户</OsButton>
      </div>
      <OsTable :columns="[
        { key: 'username', label: '用户名', width: '20%' },
        { key: 'displayName', label: '显示名', width: '20%' },
        { key: 'disabled', label: '状态', width: '12%' },
        { key: 'createdAt', label: '创建时间', width: '24%' },
      ]" :rows="users">
        <template #cell-disabled="{ value }">
          <span class="ustate" :class="{ off: value }">{{ value ? '已禁用' : '正常' }}</span>
        </template>
        <template #ops="{ row }">
          <OsSelect
            :model-value="row.role" :options="roleOptions" style="padding:3px 6px;font-size:12px"
            @update:model-value="(v: any) => setRole(row, String(v))"
          />
          <OsButton size="sm" :variant="row.disabled ? 'primary' : 'plain'" :disabled="row.username === 'admin'" @click="setDisabled(row, !row.disabled)">
            {{ row.disabled ? '启用' : '禁用' }}
          </OsButton>
          <OsButton size="sm" variant="danger" :disabled="row.username === 'admin'" @click="removeUser(row)">删除</OsButton>
        </template>
      </OsTable>
      <p class="hint">提示：禁用即时生效（在线会话被踢下线）；删除会连带清理其文件、设置与磁盘数据。</p>
    </template>

    <!-- 应用授权 -->
    <template v-else-if="tab === 'access'">
      <div class="panel-actions">
        <OsTabs
          :tabs="roles.map(r => ({ key: r, label: roleLabel(r) }))"
          v-model="accessRole"
        />
        <span class="grow" />
        <OsButton variant="primary" size="sm" :disabled="!accessDirty" @click="saveAccess">
          {{ accessDirty ? '保存授权' : '已保存' }}
        </OsButton>
      </div>
      <div class="access-grid">
        <label v-for="a in allApps" :key="a.id" class="access-item">
          <input
            type="checkbox"
            :checked="(access[accessRole] || []).includes(a.id)"
            @change="toggleApp(accessRole, a.id)"
          />
          <span class="an">{{ a.name }}</span>
          <span class="aid">{{ a.id }} · {{ a.category }}</span>
        </label>
      </div>
      <p class="hint">保存后，该角色所有在线用户的 Dock 将即时更新（通过 sys:apps-changed 事件广播）。</p>
    </template>

    <!-- 第三方应用：列表为主，添加/注册走弹窗 -->
    <template v-else-if="tab === 'registry'">
      <div class="reg-head">
        <h2 class="sec" style="margin: 0">应用（{{ registryApps.length }}）</h2>
        <span class="grow" />
        <OsButton size="sm" @click="openAddWeb">＋ 添加网页应用</OsButton>
        <OsButton size="sm" @click="openAddSchema">＋ 注册声明式应用</OsButton>
      </div>

      <OsPanel v-if="registryApps.length" class="reg-list">
        <div v-for="a in registryApps" :key="a.id" class="reg-item">
          <AppIcon :app="a.id" :icon="a.icon" :size="34" />
          <div class="reg-meta">
            <span class="an">{{ a.name }}<em v-if="a.builtin || a.locked" class="builtin-badge">内置</em></span>
            <span class="aid">{{ a.id }} · {{ appType(a) }}</span>
          </div>
          <span class="grow" />
          <OsButton size="sm" @click="bus.emit('sys:open', { app: a.id })">打开</OsButton>
          <!-- 内置应用配置随版本发布、宿主锁定（locked）应用由宿主种子维护，均不可编辑/删除 -->
          <OsButton v-if="!a.builtin && !a.locked" size="sm" @click="isWebApp(a) ? openEditWeb(a) : openEditSchema(a)">编辑</OsButton>
          <OsButton v-if="!a.builtin && !a.locked" size="sm" variant="danger" @click="removeApp(a.id)">删除</OsButton>
        </div>
      </OsPanel>
      <OsEmpty v-else text="还没有第三方应用，点右上角按钮添加" />

      <!-- 弹窗：添加 / 编辑网页应用（iframe 嵌入，可选服务端代理） -->
      <OsModal v-if="webDlg" :title="editing ? '编辑网页应用' : '添加网页应用'" :ok-text="editing ? '保存' : '添加'"
               @ok="registerWebApp" @cancel="webDlg = false; editing = null">
        <div class="dlg-form">
          <div><label>应用名称</label><OsInput v-model="webName" placeholder="企业邮箱" /></div>
          <div><label>网页地址</label><OsInput v-model="webUrl" placeholder="https://example.com/" mono /></div>
          <div v-if="!editing"><label>应用 ID（可选，缺省按域名生成）</label><OsInput v-model="webId" placeholder="web-mail" mono /></div>
          <label class="px-toggle"><input type="checkbox" v-model="webProxy" /> 应用代理（服务端反代嵌入）</label>
          <!-- 自定义请求头：仅代理模式生效（iframe 直嵌无法附带自定义头） -->
          <div v-if="webProxy" class="hdr-list">
            <label>自定义请求头（随代理转发到目标站，如内网系统的认证头）</label>
            <div v-for="(h, i) in webHeaders" :key="i" class="hdr-row">
              <OsInput v-model="h.name" placeholder="Header-Name" mono />
              <OsInput v-model="h.value" placeholder="值" mono />
              <OsButton size="sm" variant="danger" @click="delWebHeader(i)">删除</OsButton>
            </div>
            <OsButton size="sm" @click="addWebHeader">+ 添加请求头</OsButton>
          </div>
          <p class="hint">
            默认 iframe 直嵌，要求目标站点未设置 X-Frame-Options / CSP frame-ancestors 防嵌（图标取站点 favicon）。
            勾选「应用代理」后经 OpenOS 服务端反代加载，可嵌入拒绝 iframe 的站点（含内网系统）；
            自定义请求头随每个代理请求（含 WebSocket 握手）发给目标站，Cookie 类头与目标站登录态自动合并。
            目标站脚本将与桌面同源执行，仅用于可信应用。
          </p>
          <span class="msg">{{ webMsg }}</span>
        </div>
      </OsModal>

      <!-- 弹窗：注册 / 编辑声明式应用（JSON Schema） -->
      <OsModal v-if="schemaDlg" :title="editing ? '编辑声明式应用' : '注册声明式应用'" :ok-text="editing ? '保存' : '注册'"
               @ok="registerApp" @cancel="schemaDlg = false; editing = null">
        <div class="dlg-form">
          <div class="two">
            <div><label>应用 ID（英文）</label><OsInput v-model="regId" placeholder="notice-board" mono :disabled="!!editing" /></div>
            <div><label>应用名称</label><OsInput v-model="regName" placeholder="公告板" /></div>
          </div>
          <label>应用 Schema
            <OsButton size="sm" @click="schemaText = EXAMPLE_SCHEMA">填入示例模板</OsButton>
          </label>
          <textarea v-model="schemaText" class="schema" spellcheck="false" placeholder="粘贴 JSON schema…" />
          <span class="msg">{{ regMsg }}</span>
          <p class="hint">dataSource 支持：static（items）/ rest（url、method+body、headers、auth、path 点路径、interval 轮询、topic 总线推送刷新）。相对路径 url 默认携带登录态。详见 README「第三方接入指南 · 方式一」。</p>
        </div>
      </OsModal>
    </template>
    <!-- 服务器（桌面客户端专属：内嵌客户端本地设置页，改连 OpenOS 兼容端） -->
    <template v-else-if="tab === 'server'">
      <iframe :src="serverSettingsUrl" class="server-frame" title="服务器设置" />
    </template>

    <!-- AI 服务（goder 联动） -->
    <template v-else-if="tab === 'ai'">
      <!-- goder 未启用：全员提示找管理员，管理员另给部署指引 -->
      <div v-if="goderDisabled || goderUnavailable" class="card">
        <div class="group">
          <div class="g-text">
            <div class="g-title">AI 服务未启用</div>
            <div class="g-sub">OpenOS 的 AI 能力由 goder 提供，当前系统未启用 goder，请联系管理员启用。</div>
          </div>
        </div>
        <div v-if="isAdmin" class="group">
          <div class="g-text">
            <div class="g-title">管理员部署指引</div>
            <div class="g-sub g-code">
              {{ goderDisabled ? '检测到 OPENOS_GODER=off：去掉该环境变量并重启服务即可启用。' : '未找到 goder 组件：运行 make goder 构建，或使用自带 goder 的 Docker 镜像（见 README「goder 部署」）。' }}
            </div>
          </div>
        </div>
      </div>

      <template v-else>
        <h2 class="sec">AI 服务（goder 模型配置）</h2>
        <div class="card">
          <div class="group">
            <div class="g-text">
              <div class="g-title">服务状态</div>
              <div class="g-sub">
                <template v-if="aiConfigured">
                  ✓ 已接入 · 模型 {{ aiCfg.model || aiStatus?.model }} · 已下发 goder 各用户（提供方 {{ aiStatus?.goder?.aiProvider || 'OpenOS' }}，保存时设为默认模型）
                  <template v-if="goderSynced">，覆盖 {{ goderSynced }} 个用户</template>
                </template>
                <template v-else>● 未配置——goder 各用户使用自己已有的模型配置</template>
                <template v-if="goderIdle">；goder 将在首次打开时自动启动</template>
              </div>
            </div>
          </div>
          <div v-if="!isAdmin" class="group">
            <div class="g-text">
              <div class="g-title">配置由管理员统一管理</div>
              <div class="g-sub">模型的接入地址与密钥仅管理员可修改；如需更换模型，请联系管理员。</div>
            </div>
          </div>
        </div>

        <OsPanel v-if="isAdmin">
          <div class="form">
            <div class="ai-status" :class="{ on: aiConfigured }">
              {{ aiConfigured ? '✓ AI 服务已接入（goder 使用统一配置的模型）' : '● 未配置——goder 运行在各用户自有模型配置上' }}
            </div>
            <div class="two">
              <div>
                <label>Base URL</label>
                <OsInput v-model="aiCfg.baseUrl" placeholder="https://open.bigmodel.cn/api/paas/v4" mono />
              </div>
              <div>
                <label>模型</label>
                <OsInput v-model="aiCfg.model" placeholder="glm-4-flash" mono />
              </div>
            </div>
            <label>API Key（留空或掩码表示不修改）</label>
            <OsInput v-model="aiCfg.apiKey" type="password" placeholder="sk-…" mono />
            <div class="row-actions">
              <OsButton variant="primary" @click="saveAI">保存并同步到 goder</OsButton>
              <OsButton :disabled="aiTesting" @click="testAI">{{ aiTesting ? '测试中…' : '测试连接' }}</OsButton>
              <span class="msg">{{ aiMsg }}</span>
            </div>
            <div v-if="aiTestResult" class="test-result" :class="{ ok: aiTestResult.ok }">
              <template v-if="aiTestResult.ok">
                ✓ 连接成功 · {{ aiTestResult.latencyMs }}ms · 模型 {{ aiTestResult.model }} 回复「{{ aiTestResult.reply }}」
              </template>
              <template v-else>✕ {{ aiTestResult.error }}</template>
            </div>
            <p class="hint">保存后即时生效，并同步写入所有 goder 用户的模型配置（提供方 OpenOS，设为默认模型，goder 设置页中只读展示）；用户自建的其他模型不受影响。密钥仅存于服务端 SQLite，不会下发到浏览器。</p>
          </div>
        </OsPanel>
      </template>
    </template>

    <!-- 系统信息 -->
    <template v-else-if="tab === 'system'">
      <h2 class="sec">系统状态</h2>
      <OsPanel>
        <div class="stats-grid" v-if="stats">
          <div class="stat"><b>{{ stats.version }}</b><span>版本</span></div>
          <div class="stat"><b>{{ stats.userCount }}</b><span>用户</span></div>
          <div class="stat"><b>{{ stats.appCount }}</b><span>应用</span></div>
          <div class="stat"><b>{{ stats.fileCount }}</b><span>文件</span></div>
          <div class="stat"><b>{{ (stats.storageUsed / 1048576).toFixed(1) }} MB</b><span>存储占用</span></div>
          <div class="stat"><b>{{ (stats.quotaPerUser / 1048576) }} MB</b><span>单用户配额</span></div>
        </div>
        <p class="hint">启动时间：{{ stats?.startedAt }}</p>
      </OsPanel>
      <h2 class="sec">审计日志（最近 200 条）</h2>
      <OsPanel>
        <div class="audit-list">
          <div v-for="a in audit" :key="a.id" class="audit-row">
            <span class="au-time">{{ a.createdAt }}</span>
            <span class="au-user">{{ a.username }}</span>
            <span class="au-act">{{ a.action }}</span>
            <span class="au-detail">{{ a.detail }}</span>
          </div>
          <div v-if="!audit.length" class="hint">暂无日志</div>
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
.an { font-size: 13px; font-weight: 600; }
.aid { font-size: 11px; color: var(--os-text-secondary); }
.schema {
  width: 100%; min-height: 200px; resize: vertical; border-radius: 9px; padding: 10px;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  font-family: var(--os-mono); font-size: 12px; line-height: 1.5;
}
.reg-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
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
