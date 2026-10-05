<script setup lang="ts">
// 应用商店：浏览内置目录、安装/更新/卸载与按角色授权。
// 目录收录形态与安全边界见服务端 internal/market：网页应用（iframe 直嵌，WebApp
// 容器渲染）与声明式应用（SchemaApp 渲染，无代码执行），安装 = 写 apps 表 + 授权，
// 不引入远程代码加载；安装/更新仅管理员可操作，普通用户可浏览与打开已装应用。
import { computed, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { api } from '../../api/client'
import { useSession } from '../../stores/session'
import { useApps } from '../../stores/apps'
import { useWindows } from '../../stores/windows'
import { AppIcon, OsEmpty, osConfirm, toast, icons } from '../../ui'

defineProps<{ win: WinState }>()

// 目录条目（含本机安装状态，字段与服务端 market.State 对应）
interface MarketState {
  id: string
  name: string
  description: string
  category: string
  icon: string
  type: 'web' | 'proxy' | 'schema' | 'bundle' | 'host'
  version: string
  changelog?: string
  perms?: string[]
  installed: boolean
  installedVersion?: string
  upgradable: boolean
  managed: boolean
  conflict?: boolean
  protected?: boolean
  versions?: string[] // 可切换版本（bundle 专属）：目录历史版本 + 本地已落盘版本
}

const session = useSession()
const apps = useApps()
const windows = useWindows()
const isAdmin = computed(() => session.user?.role === 'admin')

const states = ref<MarketState[]>([])
const loading = ref(false)
const keyword = ref('')
const category = ref('全部')
const detail = ref<MarketState | null>(null) // 非空 = 详情视图
const busy = ref(false)

// 角色授权选择（仅 admin 加载；安装时整组覆盖该应用的角色授权，admin 恒在）
const roles = ref<string[]>([])
const roleLabels = ref<Record<string, string>>({})
const accessMap = ref<Record<string, string[]>>({})
const selectedRoles = ref<string[]>(['admin', 'user'])

async function load() {
  loading.value = true
  try {
    states.value = await api.get<MarketState[]>('/api/market/apps')
  } catch (e: any) {
    toast(e.message, { type: 'error' })
  } finally {
    loading.value = false
  }
}

// 「刷新」按钮：先让服务端立即拉取远程目录源（不等 10 分钟周期），再重读货架。
// 全部源拉取失败时服务端沿用现行目录，按钮仍算成功（货架可正常重读）。
const refreshing = ref(false)
async function refresh() {
  if (refreshing.value) return
  refreshing.value = true
  try {
    await api.post('/api/market/refresh')
    await load()
  } catch (e: any) {
    toast(e.message, { type: 'error' })
  } finally {
    refreshing.value = false
  }
}
async function loadRoles() {
  if (!isAdmin.value) return
  try {
    const d = await api.get<{ roles: string[]; access: Record<string, string[]>; labels?: Record<string, string> }>('/api/apps/access')
    roles.value = d.roles
    accessMap.value = d.access || {}
    roleLabels.value = d.labels || {}
  } catch { /* 忽略：安装时无角色信息则只授 admin */ }
}
onMounted(() => { load(); loadRoles(); loadAutoUpgrade() })

const categories = computed(() => {
  const out: string[] = []
  for (const s of states.value) if (!out.includes(s.category)) out.push(s.category)
  return ['全部', ...out]
})
const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return states.value.filter((s) =>
    (category.value === '全部' || s.category === category.value) &&
    (!kw || s.name.toLowerCase().includes(kw) || s.description.toLowerCase().includes(kw) || s.id.toLowerCase().includes(kw)))
})

function typeLabel(t: string) {
  return t === 'web' ? '网页应用' : t === 'proxy' ? '代理网页' : t === 'schema' ? '声明式' : t === 'bundle' ? '组件应用' : t === 'host' ? '宿主应用' : t
}

function openDetail(s: MarketState) {
  settingsOpen.value = false
  detail.value = s
  // 已装应用预选当前已授权角色；未安装默认 admin + user
  const granted = Object.keys(accessMap.value).filter((r) => accessMap.value[r]?.includes(s.id))
  selectedRoles.value = granted.length ? granted : ['admin', 'user']
  if (!selectedRoles.value.includes('admin')) selectedRoles.value.push('admin')
  selVer.value = s.installedVersion || s.version
}
function toggleRole(r: string) {
  const i = selectedRoles.value.indexOf(r)
  if (i >= 0) selectedRoles.value.splice(i, 1)
  else selectedRoles.value.push(r)
}
// 卡片按钮：未安装 → 进详情选角色安装（admin）或提示；已安装 → 打开
function cardAction(s: MarketState) {
  if (!s.installed || s.conflict || (s.upgradable && isAdmin.value)) { openDetail(s); return }
  openApp(s)
}
async function install(s: MarketState) {
  busy.value = true
  try {
    await api.post('/api/market/install', { id: s.id, roles: selectedRoles.value })
    toast(`${s.name} 已安装（v${s.version}）`, { type: 'success' })
    await Promise.all([load(), apps.refresh()])
    detail.value = states.value.find((x) => x.id === s.id) || null
  } catch (e: any) {
    toast(e.message, { type: 'error' })
  } finally {
    busy.value = false
  }
}
async function uninstall(s: MarketState) {
  if (!(await osConfirm({ title: '卸载应用', body: `确定卸载「${s.name}」？其授权将一并回收。`, danger: true, confirmText: '卸载' }))) return
  busy.value = true
  try {
    await api.del(`/api/apps/${s.id}`)
    toast(`${s.name} 已卸载`)
    await Promise.all([load(), apps.refresh()])
    detail.value = null
  } catch (e: any) {
    toast(e.message, { type: 'error' })
  } finally {
    busy.value = false
  }
}
function openApp(s: MarketState) {
  if (apps.byId(s.id)) windows.open(s.id)
  else toast('当前角色未获授权，请联系管理员', { type: 'error' })
}

// ---- 版本切换（回滚，仅 bundle 且已安装）：POST install 带 version，服务端保留授权、
//      本地已有该版本组件文件则直接复用（离线可回滚）。返回是否切换成功（取消/失败
//      时调用方把下拉选值弹回当前版本） ----
const selVer = ref('')
async function switchVersion(s: MarketState, v: string): Promise<boolean> {
  const note = autoUp.value && v !== s.version
    ? `\n自动升级会跳过 v${s.version}（视为问题版本），目录发布更新的版本时才会自动升级。`
    : ''
  if (!(await osConfirm({
    title: '切换版本',
    body: `将「${s.name}」从 v${s.installedVersion || '—'} 切换到 v${v}，授权保持不变。${note}`,
    confirmText: '切换',
  }))) return false
  busy.value = true
  try {
    await api.post('/api/market/install', { id: s.id, version: v })
    toast(`${s.name} 已切换到 v${v}`, { type: 'success' })
    await Promise.all([load(), apps.refresh()])
    detail.value = states.value.find((x) => x.id === s.id) || null
    selVer.value = v
    return true
  } catch (e: any) {
    toast(e.message, { type: 'error' })
    return false
  } finally {
    busy.value = false
  }
}
// 下拉选中即触发：未切走是空操作；取消/失败把选值弹回当前版本
async function onVerPick(ev: Event) {
  const v = (ev.target as HTMLSelectElement).value
  const s = detail.value
  if (!s || v === s.installedVersion) return
  if (!(await switchVersion(s, v))) selVer.value = s.installedVersion || s.version
}

// ---- 商店设置：目录源管理（admin）——默认官方源不可删，自定义源可添加多个，
//      排在前面可遮蔽官方同 id 条目；保存后服务端立即拉取全部源合并生效 ----
const settingsOpen = ref(false)
const srcDefaults = ref<string[]>([])
const srcCustom = ref<string[]>([])
const srcInput = ref('')
const srcMsg = ref('')

// 自动升级开关（admin，全局）：目录每次刷新（10 分钟周期/手动/保存源）后，
// 商店安装的应用自动对齐目录版本并保留授权；关闭则仅手动更新
const autoUp = ref(false)
const autoUpBusy = ref(false)

async function loadAutoUpgrade() {
  try {
    const d = await api.get<{ on: boolean }>('/api/market/autoupgrade')
    autoUp.value = d.on
  } catch { /* 忽略：开关状态拉取失败按默认关展示 */ }
}
async function saveAutoUpgrade() {
  autoUpBusy.value = true
  try {
    await api.put('/api/market/autoupgrade', { on: autoUp.value })
    toast(autoUp.value ? '自动升级已开启' : '自动升级已关闭', { type: 'success' })
    if (autoUp.value) await load() // 开启即触发一轮刷新+升级，重读货架反映新版本
  } catch (e: any) {
    toast(e.message, { type: 'error' })
    await loadAutoUpgrade()
  } finally {
    autoUpBusy.value = false
  }
}

async function openSettings() {
  detail.value = null
  settingsOpen.value = true
  srcMsg.value = ''
  try {
    const d = await api.get<{ defaults: string[]; custom: string[] }>('/api/market/sources')
    srcDefaults.value = d.defaults || []
    srcCustom.value = d.custom || []
  } catch (e: any) {
    srcMsg.value = e.message
  }
}
function addSource() {
  const u = srcInput.value.trim()
  if (!/^https?:\/\//i.test(u)) { srcMsg.value = '地址需以 http(s):// 开头'; return }
  if (u.length > 500) { srcMsg.value = '地址过长'; return }
  if (srcCustom.value.includes(u)) { srcMsg.value = '该地址已在列表中'; return }
  if (srcCustom.value.length >= 10) { srcMsg.value = '最多添加 10 个自定义源'; return }
  srcCustom.value.push(u)
  srcInput.value = ''
  srcMsg.value = ''
}
async function saveSources() {
  busy.value = true
  srcMsg.value = ''
  try {
    const d = await api.put<{ custom: string[]; sourcesOk: number; entries: number }>('/api/market/sources', { urls: srcCustom.value })
    srcCustom.value = d.custom || []
    toast(`目录源已保存（${d.sourcesOk} 个源拉取成功，共 ${d.entries} 个应用）`, { type: 'success' })
    settingsOpen.value = false
    await load()
  } catch (e: any) {
    srcMsg.value = e.message
  } finally {
    busy.value = false
  }
}

const icoSearch = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><circle cx="7" cy="7" r="4.6"/><path d="M10.6 10.6 14 14"/></svg>'
const icoBack = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M9.5 3.5 5 8l4.5 4.5"/></svg>'
const icoGear = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="8" cy="8" r="1.9"/><circle cx="8" cy="8" r="4.2"/><path d="M8 1.6v2.4M8 12v2.4M1.6 8h2.4M12 8h2.4M3.47 3.47l1.66 1.66M12.53 3.47l-1.66 1.66M3.47 12.53l1.66-1.66M12.53 12.53l-1.66-1.66"/></svg>'
</script>

<template>
  <div class="store-app">
    <!-- 融合工具栏：整条可拖动窗口 -->
    <div class="os-tb" data-os-drag>
      <button v-if="detail || settingsOpen" class="os-tb-btn" title="返回" v-html="icoBack" @click="settingsOpen ? (settingsOpen = false) : (detail = null)" />
      <div class="os-tb-title">{{ settingsOpen ? '商店设置' : detail ? detail.name : '应用商店' }}</div>
      <span v-if="!detail && !settingsOpen" class="count">{{ filtered.length }} 个应用</span>
      <span class="grow" />
      <div v-if="!detail && !settingsOpen" class="search">
        <span class="s-ico" v-html="icoSearch" />
        <input v-model="keyword" placeholder="搜索应用" spellcheck="false" />
      </div>
      <button v-if="isAdmin" class="os-tb-btn" title="商店设置（目录源）" v-html="icoGear" @click="openSettings" />
      <button class="os-tb-btn" :class="{ spin: refreshing }" :disabled="refreshing" title="刷新" v-html="icons.refresh" @click="refresh" />
    </div>

    <!-- 目录列表 -->
    <div v-if="!detail && !settingsOpen" class="body">
      <div class="cats">
        <button v-for="c in categories" :key="c" class="cat" :class="{ on: c === category }" @click="category = c">{{ c }}</button>
      </div>
      <div v-if="loading && !states.length" class="hint">正在加载目录…</div>
      <div v-else-if="!filtered.length" class="hint-empty"><OsEmpty text="没有匹配的应用" /></div>
      <div v-else class="grid">
        <div v-for="s in filtered" :key="s.id" class="card" @click="openDetail(s)">
          <AppIcon class="c-icon" :app="s.id" :icon="s.icon" :size="48" />
          <div class="c-main">
            <div class="c-name">{{ s.name }} <span class="c-type">{{ typeLabel(s.type) }}</span></div>
            <div class="c-desc">{{ s.description }}</div>
            <div class="c-cat">{{ s.category }}<template v-if="s.installed"> · 已安装<template v-if="s.upgradable">（可更新）</template></template></div>
          </div>
          <button
            class="c-btn" :class="{ accent: !s.installed || s.upgradable }"
            :disabled="(s.protected && !apps.byId(s.id)) || (!isAdmin && (!s.installed || s.upgradable))"
            @click.stop="cardAction(s)"
          >
            {{ (s.protected && !apps.byId(s.id)) ? '不可用' : !s.installed ? (isAdmin ? '安装' : '未安装') : s.upgradable ? (isAdmin ? '更新' : '可更新') : (apps.byId(s.id) ? '打开' : '已安装') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 商店设置：自动升级 + 目录源管理（admin） -->
    <div v-else-if="settingsOpen" class="body detail">
      <div class="d-sec">
        <h4>自动升级</h4>
        <p class="s-desc">
          开启后，每次目录刷新（每 10 分钟自动 + 手动刷新/保存目录源）会把商店安装的应用自动升级到目录版本并保留原授权；
          目录最新版被货架回退时也会跟随回退。手动切换到旧版本的应用会自动跳过被避开的版本（视为问题版本），
          目录发布更新的版本后才恢复升级。关闭后仅在手动点击「更新」时升级。
        </p>
        <label class="au-row">
          <input type="checkbox" v-model="autoUp" :disabled="autoUpBusy" @change="saveAutoUpgrade" />
          <span>目录刷新时自动升级已安装应用</span>
        </label>
      </div>
      <div class="d-sec">
        <h4>目录源</h4>
        <p class="s-desc">
          应用目录从以下 JSON 地址拉取（每 10 分钟自动刷新，保存后立即拉取）。
          自定义源排在官方源之前，与官方目录同 id 的条目以自定义源为准；多个官方源按列出顺序前者为准。
        </p>
        <div v-for="u in srcDefaults" :key="u" class="src-row">
          <span class="src-badge">官方</span>
          <span class="src-url" :title="u">{{ u }}</span>
        </div>
        <div v-if="!srcDefaults.length" class="src-row">
          <span class="src-badge">官方</span>
          <span class="src-url">（官方源已关闭：OPENOS_MARKET_URL=off）</span>
        </div>
        <div v-for="(u, i) in srcCustom" :key="u" class="src-row">
          <span class="src-badge custom">自定义</span>
          <span class="src-url" :title="u">{{ u }}</span>
          <button class="ghost danger s-del" :disabled="busy" @click="srcCustom.splice(i, 1)">移除</button>
        </div>
        <div class="src-add">
          <input v-model="srcInput" placeholder="https://example.com/catalog.json" spellcheck="false" @keydown.enter="addSource" />
          <button class="ghost" @click="addSource">添加</button>
        </div>
        <div v-if="srcMsg" class="s-msg">{{ srcMsg }}</div>
        <div class="d-actions">
          <button class="primary" :disabled="busy" @click="saveSources">{{ busy ? '正在拉取…' : '保存并拉取' }}</button>
        </div>
      </div>
    </div>

    <!-- 应用详情 -->
    <div v-else-if="detail" class="body detail">
      <div class="d-head">
        <AppIcon class="d-icon" :app="detail.id" :icon="detail.icon" :size="76" />
        <div class="d-info">
          <div class="d-name">{{ detail.name }}</div>
          <div class="d-meta">
            {{ detail.category }} · {{ typeLabel(detail.type) }} · 目录 v{{ detail.version }}
            <template v-if="detail.installed"> · 本机 v{{ detail.installedVersion || '—' }}</template>
          </div>
          <div class="d-actions">
            <button v-if="!detail.installed && isAdmin && detail.type !== 'host'" class="primary" :disabled="busy || detail.protected" @click="install(detail)">安装</button>
            <button v-else-if="detail.upgradable && isAdmin" class="primary" :disabled="busy" @click="install(detail)">更新到 v{{ detail.version }}</button>
            <span v-if="detail.installed && detail.managed && !detail.upgradable" class="ok">✓ 已是最新版本</span>
            <button v-if="detail.installed && apps.byId(detail.id)" class="ghost" @click="openApp(detail)">打开</button>
            <button v-if="detail.installed && isAdmin && !detail.protected" class="ghost danger" :disabled="busy" @click="uninstall(detail)">卸载</button>
          </div>
        </div>
      </div>
      <p class="d-desc">{{ detail.description }}</p>
      <div v-if="isAdmin && detail.installed && detail.managed && detail.versions?.length" class="d-sec">
        <h4>版本切换（回滚）</h4>
        <p class="s-desc">切换到指定历史版本：授权保持不变，本地已有该版本文件时离线可切。当前 v{{ detail.installedVersion }}。</p>
        <div class="ver-row">
          <select v-model="selVer" class="ver-select" :disabled="busy" @change="onVerPick">
            <option v-for="v in detail.versions" :key="v" :value="v">
              v{{ v }}{{ v === detail.version ? '（最新）' : '' }}{{ v === detail.installedVersion ? ' · 当前' : '' }}
            </option>
          </select>
        </div>
      </div>
      <div v-if="detail.perms?.length" class="d-sec">
        <h4>权限说明</h4>
        <ul><li v-for="(p, i) in detail.perms" :key="i">{{ p }}</li></ul>
      </div>
      <div v-if="detail.changelog" class="d-sec">
        <h4>版本 {{ detail.version }}</h4>
        <p class="d-log">{{ detail.changelog }}</p>
      </div>
      <div v-if="detail.conflict" class="d-warn">本机已存在 ID 为「{{ detail.id }}」的手工注册应用，安装将覆盖其配置。</div>
      <div v-if="detail.protected" class="d-warn">应用 ID 与内置或宿主锁定应用冲突，不可安装。</div>
      <div v-if="!isAdmin" class="d-note">安装与更新需要管理员权限。</div>
      <div v-if="isAdmin && !detail.protected" class="d-sec">
        <h4>授权角色（安装时应用）</h4>
        <div class="d-roles">
          <label v-for="r in roles" :key="r" class="role" :class="{ fixed: r === 'admin' }">
            <input type="checkbox" :checked="selectedRoles.includes(r)" :disabled="r === 'admin'" @change="toggleRole(r)" />
            <span>{{ roleLabels[r] || r }}</span>
            <span v-if="r === 'admin'" class="r-note">恒有</span>
          </label>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.store-app { width: 100%; height: 100%; display: flex; flex-direction: column; background: var(--os-bg, #f5f5f7); }
.os-tb .grow { flex: 1; }
.count { font-size: 12px; color: var(--os-text-secondary); white-space: nowrap; }
.search {
  display: flex; align-items: center; gap: 5px; height: 26px; padding: 0 9px;
  background: rgba(125, 125, 125, .14); border-radius: 13px; flex: none;
}
.search:focus-within { outline: 2px solid var(--os-focus-ring); }
.s-ico { display: flex; color: var(--os-text-secondary); }
.s-ico :deep(svg) { width: 13px; height: 13px; }
.search input { border: none; background: none; outline: none; width: 130px; font-size: 12px; color: var(--os-text, #222); }

.body { flex: 1; min-height: 0; overflow-y: auto; padding: 14px 18px 20px; }
.cats { display: flex; gap: 6px; flex-wrap: wrap; margin-bottom: 14px; }
.cat {
  border: none; cursor: pointer; padding: 5px 13px; border-radius: 13px; font-size: 12px;
  background: rgba(125, 125, 125, .13); color: var(--os-text-secondary);
}
.cat:hover { background: rgba(125, 125, 125, .22); }
.cat.on { background: var(--os-accent); color: #fff; }
/* 深色主题：灰底 chips 在暗背景上对比不足，提亮底色与文字 */
html.dark .cat { background: rgba(125, 125, 125, .22); color: #c7c7cc; }
html.dark .cat:hover { background: rgba(125, 125, 125, .32); }

.hint { padding: 60px 0; text-align: center; color: var(--os-text-secondary); font-size: 13px; }
.hint-empty { padding: 60px 0; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 12px; }
.card {
  display: flex; gap: 12px; align-items: center; padding: 14px; border-radius: 12px;
  background: var(--os-panel-solid); box-shadow: 0 1px 2px rgba(0, 0, 0, .06); cursor: default;
  border: 1px solid transparent;
}
.card:hover { border-color: var(--os-accent-soft, rgba(0, 122, 255, .3)); box-shadow: 0 2px 8px rgba(0, 0, 0, .08); }
html.dark .card { box-shadow: 0 1px 3px rgba(0, 0, 0, .4); }
html.dark .card:hover { box-shadow: 0 2px 10px rgba(0, 0, 0, .5); }
.c-main { flex: 1; min-width: 0; }
.c-name { font-size: 14px; font-weight: 600; color: var(--os-text, #222); display: flex; align-items: center; gap: 6px; }
.c-type { font-size: 10.5px; font-weight: 400; color: var(--os-text-secondary); background: rgba(125, 125, 125, .14); padding: 1px 7px; border-radius: 8px; flex: none; }
.c-desc {
  font-size: 12px; color: var(--os-text-secondary); margin-top: 3px;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.c-cat { font-size: 11px; color: var(--os-text-secondary); margin-top: 5px; }
.c-btn {
  flex: none; border: none; cursor: pointer; font-size: 12px; padding: 6px 14px; border-radius: 13px;
  background: rgba(125, 125, 125, .15); color: var(--os-text, #222);
}
.c-btn.accent { background: var(--os-accent); color: #fff; }
.c-btn.accent:hover:not(:disabled) { background: var(--os-accent-hover, var(--os-accent)); }
.c-btn:disabled { opacity: .45; cursor: default; }

/* 详情 */
.detail { max-width: 720px; }
.d-head { display: flex; gap: 18px; align-items: center; }
.d-info { flex: 1; min-width: 0; }
.d-name { font-size: 20px; font-weight: 700; color: var(--os-text, #222); }
.d-meta { font-size: 12px; color: var(--os-text-secondary); margin-top: 4px; }
.d-actions { display: flex; gap: 8px; align-items: center; margin-top: 12px; flex-wrap: wrap; }
.d-actions .ok { font-size: 12px; color: #34a853; }
html.dark .d-actions .ok { color: #30d158; }
.primary, .ghost {
  border: none; cursor: pointer; font-size: 13px; padding: 7px 18px; border-radius: 8px;
}
.primary { background: var(--os-accent); color: #fff; }
.primary:hover:not(:disabled) { background: var(--os-accent-hover, var(--os-accent)); }
.ghost { background: rgba(125, 125, 125, .13); color: var(--os-text, #222); }
.ghost:hover:not(:disabled) { background: rgba(125, 125, 125, .24); }
.ghost.danger { color: #e0343c; }
.primary:disabled, .ghost:disabled { opacity: .5; cursor: default; }
.d-desc { font-size: 13px; line-height: 1.7; color: var(--os-text, #222); margin: 16px 2px 0; }
.d-sec { margin-top: 18px; }
.d-sec h4 { font-size: 12px; color: var(--os-text-secondary); font-weight: 600; margin-bottom: 7px; }
.d-sec ul { margin: 0; padding-left: 18px; font-size: 12.5px; line-height: 1.8; color: var(--os-text, #222); }
.d-log { margin: 0; font-size: 12.5px; color: var(--os-text, #222); }
.d-warn {
  margin-top: 16px; padding: 9px 12px; border-radius: 8px; font-size: 12.5px;
  background: #fff3e0; color: #b2600a;
}
html.dark .d-warn { background: rgba(255, 159, 10, .16); color: #ffb340; }
.d-note { margin-top: 16px; padding: 9px 12px; border-radius: 8px; font-size: 12.5px; background: rgba(125, 125, 125, .12); color: var(--os-text-secondary); }
.d-roles { display: flex; gap: 8px; flex-wrap: wrap; }
.role {
  display: flex; align-items: center; gap: 6px; font-size: 12.5px; cursor: pointer;
  padding: 6px 12px; border-radius: 8px; background: rgba(125, 125, 125, .12); color: var(--os-text, #222);
}
.role input { accent-color: var(--os-accent); }
.role.fixed { cursor: default; opacity: .75; }
.r-note { font-size: 11px; color: var(--os-text-secondary); }

/* 商店设置：目录源 */
.s-desc { margin: 0 0 12px; font-size: 12.5px; line-height: 1.7; color: var(--os-text-secondary); }
.au-row {
  display: flex; align-items: center; gap: 8px; font-size: 12.5px; cursor: pointer;
  padding: 8px 12px; border-radius: 8px; background: rgba(125, 125, 125, .1); color: var(--os-text, #222);
}
.au-row:hover { background: rgba(125, 125, 125, .18); }
.au-row input { accent-color: var(--os-accent); }
.src-row { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border-radius: 8px; background: rgba(125, 125, 125, .1); margin-bottom: 6px; }
.src-badge {
  flex: none; font-size: 10.5px; padding: 2px 8px; border-radius: 8px;
  background: var(--os-accent); color: #fff;
}
.src-badge.custom { background: rgba(125, 125, 125, .35); color: var(--os-text, #222); }
.src-url { flex: 1; min-width: 0; font-size: 12.5px; color: var(--os-text, #222); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; direction: rtl; text-align: left; }
.s-del { padding: 4px 10px; font-size: 11.5px; }
.src-add { display: flex; gap: 8px; margin-top: 10px; }
.src-add input {
  flex: 1; min-width: 0; height: 30px; padding: 0 10px; border-radius: 8px; font-size: 12.5px;
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text, #222); outline: none;
}
.src-add input:focus { border-color: var(--os-accent); }
.src-add .ghost { padding: 5px 14px; }
.s-msg { margin-top: 8px; font-size: 12px; color: #e0343c; }

/* 版本切换（回滚） */
.ver-row { display: flex; gap: 8px; align-items: center; }
.ver-select {
  height: 30px; max-width: 280px; padding: 0 8px; border-radius: 8px; font-size: 12.5px;
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text, #222); outline: none;
}
.ver-select:focus { border-color: var(--os-accent); }
</style>
