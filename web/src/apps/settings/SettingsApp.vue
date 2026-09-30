<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { WinState } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { useDesktop } from '../../stores/desktop'
import { useApps } from '../../stores/apps'
import { api, type AppDef } from '../../api/client'
import { bus } from '../../bus/bus'
import {
  OsButton, OsInput, OsSelect, OsToggle, OsPanel, OsTable, OsLayout, OsTabs, OsModal,
} from '../../ui'

const props = defineProps<{ win: WinState }>()
const session = useSession()
const desktop = useDesktop()
const apps = useApps()
const isAdmin = session.user?.role === 'admin'

const tab = ref('appearance')
const items = computed(() => {
  const base = [
    { key: 'appearance', label: '外观', icon: '🎨' },
    { key: 'account', label: '账户', icon: '👤' },
  ]
  if (isAdmin) {
    base.push(
      { key: 'users', label: '用户管理', icon: '👥' },
      { key: 'access', label: '应用授权', icon: '🔐' },
      { key: 'registry', label: '第三方应用', icon: '🧩' },
      { key: 'ai', label: 'AI 服务', icon: '✦' },
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
const roleOptions = computed(() => roles.value.map(r => ({ value: r, label: r })))

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
  const data = await api.get<{ roles: string[]; access: Record<string, string[]> }>('/api/apps/access')
  roles.value = data.roles
  access.value = data.access
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

// ---- 第三方应用注册 ----
const schemaText = ref('')
const regName = ref('')
const regId = ref('')
const regMsg = ref('')
const registryApps = computed(() => allApps.value.filter((a) => !a.builtin))

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
      id: regId.value, name: regName.value, icon: 'default',
      category: '第三方', config: schemaText.value, width: 760, height: 560,
    })
    regMsg.value = '✓ 注册成功，已可在 Dock 使用'
    regId.value = ''; regName.value = ''
    loadAccess()
  } catch (e: any) { regMsg.value = '注册失败：' + e.message }
}
async function removeApp(id: string) {
  if (!confirm(`删除第三方应用 ${id}？`)) return
  await api.del(`/api/apps/${id}`)
  loadAccess()
}

// ---- AI 服务（goder 接入配置） ----
const aiCfg = ref({ baseUrl: '', apiKey: '', model: '' })
const aiConfigured = ref(false)
const aiMsg = ref('')
const aiTesting = ref(false)
const aiTestResult = ref<any>(null)

async function loadAI() {
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
    aiMsg.value = '✓ 已保存并即时生效'
    loadAI()
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
  if (isAdmin) { loadUsers(); loadAccess(); loadAI() }
})
watch(tab, (t) => { if (t === 'system') loadSystem() })
</script>

<template>
  <OsLayout :win="props.win" :items="items" :active="tab" status="OpenOS · 系统设置" @select="tab = $event">
    <!-- 外观 -->
    <template v-if="tab === 'appearance'">
      <OsPanel title="壁纸">
        <div class="wallpapers">
          <button
            v-for="(w, i) in desktop.wallpapers" :key="i"
            class="wall" :class="{ active: desktop.wallpaper === i }"
            :style="{ background: w }" @click="desktop.setWallpaper(i)"
          >{{ i + 1 }}</button>
        </div>
      </OsPanel>
      <OsPanel title="主题" style="margin-top: 12px">
        <div class="row">
          <OsToggle :model-value="desktop.dark" label="深色模式" @update:model-value="(v: any) => desktop.setDark(!!v)" />
        </div>
        <div class="row" style="margin-top: 10px">
          <OsToggle
            :model-value="desktop.dockAutoHide"
            label="最大化时自动隐藏 Dock（鼠标移到屏幕底部滑出）"
            @update:model-value="(v: any) => desktop.setDockAutoHide(!!v)"
          />
        </div>
      </OsPanel>
    </template>

    <!-- 账户 -->
    <template v-else-if="tab === 'account'">
      <OsPanel :title="`账户 · ${session.user?.username}（${session.user?.role}）`">
        <div class="form">
          <label>原密码</label>
          <OsInput v-model="oldPwd" type="password" placeholder="原密码" />
          <label>新密码</label>
          <OsInput v-model="newPwd" type="password" placeholder="至少 6 位" />
          <div><OsButton variant="primary" @click="changePwd">修改密码</OsButton>
            <span v-if="pwdMsg" class="msg">{{ pwdMsg }}</span></div>
        </div>
      </OsPanel>
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
          :tabs="roles.map(r => ({ key: r, label: r }))"
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

    <!-- 第三方应用注册 -->
    <template v-else-if="tab === 'registry'">
      <OsPanel title="注册声明式应用（JSON Schema）">
        <div class="form">
          <div class="two">
            <div><label>应用 ID（英文）</label><OsInput v-model="regId" placeholder="notice-board" mono /></div>
            <div><label>应用名称</label><OsInput v-model="regName" placeholder="公告板" /></div>
          </div>
          <label>应用 Schema
            <OsButton size="sm" @click="schemaText = EXAMPLE_SCHEMA">填入示例模板</OsButton>
          </label>
          <textarea v-model="schemaText" class="schema" spellcheck="false" placeholder="粘贴 JSON schema…" />
          <div>
            <OsButton variant="primary" @click="registerApp">注册应用</OsButton>
            <span class="msg">{{ regMsg }}</span>
          </div>
        </div>
        <p class="hint">dataSource 支持：static（items）/ rest（url、method+body、headers、auth、path 点路径、interval 轮询、topic 总线推送刷新）。相对路径 url 默认携带登录态。详见 README「第三方接入指南 · 方式一」。</p>
      </OsPanel>

      <OsPanel title="已注册的第三方应用" style="margin-top: 12px" v-if="registryApps.length">
        <div v-for="a in registryApps" :key="a.id" class="reg-item">
          <span class="an">{{ a.name }}</span><span class="aid">{{ a.id }}</span>
          <span class="grow" />
          <OsButton size="sm" @click="bus.emit('sys:open', { app: a.id }); ">在桌面打开</OsButton>
          <OsButton size="sm" variant="danger" @click="removeApp(a.id)">删除</OsButton>
        </div>
      </OsPanel>
    </template>
    <!-- AI 服务（goder） -->
    <template v-else-if="tab === 'ai'">
      <OsPanel title="接入真实大模型（任意 OpenAI 兼容接口）">
        <div class="form">
          <div class="ai-status" :class="{ on: aiConfigured }">
            {{ aiConfigured ? '✓ AI 服务已接入（goder 使用真实模型）' : '● 未配置——goder 运行在演示模式' }}
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
            <OsButton variant="primary" @click="saveAI">保存配置</OsButton>
            <OsButton :disabled="aiTesting" @click="testAI">{{ aiTesting ? '测试中…' : '测试连接' }}</OsButton>
            <span class="msg">{{ aiMsg }}</span>
          </div>
          <div v-if="aiTestResult" class="test-result" :class="{ ok: aiTestResult.ok }">
            <template v-if="aiTestResult.ok">
              ✓ 连接成功 · {{ aiTestResult.latencyMs }}ms · 模型 {{ aiTestResult.model }} 回复「{{ aiTestResult.reply }}」
            </template>
            <template v-else>✕ {{ aiTestResult.error }}</template>
          </div>
          <p class="hint">保存后即时生效，无需重启服务。密钥仅存于服务端 SQLite，不会下发到浏览器（界面上始终脱敏）。</p>
        </div>
      </OsPanel>
    </template>

    <!-- 系统信息 -->
    <template v-else-if="tab === 'system'">
      <OsPanel title="系统状态">
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
      <OsPanel title="审计日志（最近 200 条）" style="margin-top: 12px">
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
.wallpapers { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; }
.wall {
  height: 64px; border-radius: 10px; border: 2px solid transparent; cursor: pointer;
  color: rgba(255,255,255,.85); font-size: 12px; transition: transform .12s;
}
.wall:hover { transform: scale(1.03); }
.wall.active { border-color: var(--os-accent); box-shadow: 0 0 0 3px rgba(10,132,255,.25); }
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
.reg-item { display: flex; align-items: center; gap: 10px; padding: 8px 4px; border-bottom: 1px solid var(--os-border); }
.reg-item:last-child { border-bottom: none; }

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
