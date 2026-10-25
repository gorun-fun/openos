<script setup lang="ts">
// 应用 — AI 转制上架（实验特色功能）：把公开项目转制为 openos 应用发布到货架。
// 发起（源仓库 URL → 队列）→ 任务列表（状态机 cloning/converting/building/
// publishing 跟进、Goder 会话深链、日志、取消）→ 已上架应用（货架 catalog 清单，
// 经应用商店安装）。前置：活动 GitLab 连接（目标项目与 CI）+ 货架目录
// GRF_OS_STEWARD_MARKET_DIR。
import { computed, ref } from 'vue'
import { OsButton, OsInput, OsModal, OsTable, OsEmpty, toast, osConfirm } from '../../../ui'
import type { TableColumn } from '../../../ui'
import { useWindows } from '../../../stores/windows'
import { grf } from '../core'
import { usePoll } from '../usePoll'

const windows = useWindows()

// ---- 前置状态 ----
const conns = ref<any[]>([])
const market = ref<any>(null)

async function loadPre() {
  const [c, m] = await Promise.all([
    grf.get<any>('/api/steward/connections').catch(() => null),
    grf.get<any>('/api/steward/apps').catch(() => null),
  ])
  conns.value = Array.isArray(c) ? c : []
  market.value = m || null
}
loadPre()

const activeGitLab = computed(() => conns.value.some((c: any) => c.platform === 'gitlab' && c.active))
const marketReady = computed(() => !!market.value?.market_configured)
const ready = computed(() => activeGitLab.value && marketReady.value)

// ---- 发起转制 ----
const srcURL = ref('')
const busy = ref(false)
const detail = ref<any>(null)
const detailLog = ref('')
const logOffset = ref(0)

async function start() {
  const u = srcURL.value.trim()
  if (!u) { toast('填入公开项目地址（https://…）', { type: 'error' }); return }
  busy.value = true
  try {
    const cv = await grf.post<any>('/api/steward/conversions', { source_url: u })
    toast(`已入队：${cv?.app_id}（克隆 → AI 转制 → CI 构建 → 货架发布）`, { type: 'success' })
    srcURL.value = ''
    await load()
  } catch (e: any) { toast(e.message || '发起失败', { type: 'error' }) } finally { busy.value = false }
}

// ---- 任务列表 ----
const convs = ref<any[]>([])

async function load() {
  try {
    convs.value = (await grf.get<any>('/api/steward/conversions')) || []
  } catch { /* 引擎未启用等场景 */ }
}
usePoll(load, 5_000)

const cvMeta: Record<string, { label: string; cls: string }> = {
  queued: { label: '排队中', cls: 'info' },
  cloning: { label: '克隆/建项目', cls: 'info' },
  converting: { label: 'AI 转制中', cls: 'info' },
  building: { label: 'CI 构建', cls: 'info' },
  publishing: { label: '发布货架', cls: 'info' },
  done: { label: '已上架', cls: 'ok' },
  failed: { label: '失败', cls: 'bad' },
  canceled: { label: '已取消', cls: '' },
}
const sm = (s: string) => cvMeta[s] || { label: s, cls: '' }
const terminal = (s: string) => ['done', 'failed', 'canceled'].includes(s)

const cols: TableColumn[] = [
  { key: 'app_id', label: '应用', width: '130px' },
  { key: 'source_url', label: '源项目' },
  { key: 'state', label: '状态', width: '110px' },
  { key: 'version', label: '版本', width: '80px' },
  { key: 'started_at', label: '时间', width: '150px', format: (v: string) => String(v || '').replace('T', ' ').slice(0, 19) },
]

async function openDetail(cv: any) {
  detail.value = cv
  logOffset.value = 0
  detailLog.value = ''
  await pullDetail(true)
}

async function pullDetail(reset = false) {
  const d = detail.value
  if (!d) return
  try {
    const res = await grf.get<any>(`/api/steward/conversions/${d.id}?log=${reset ? 0 : logOffset.value}`)
    detail.value = res?.conversion || d
    if (res?.log) {
      detailLog.value += res.log
      logOffset.value = res.log_offset ?? logOffset.value
    }
  } catch { /* 记录可能已清理 */ }
}
usePoll(() => { if (detail.value && !terminal(detail.value.state)) pullDetail() }, 3_000)

async function cancel() {
  const d = detail.value
  if (!await osConfirm({ title: '取消转制', body: `中止「${d.app_id}」的转制？（Goder 会话保留在「代码管家」分组，可人工跟进）`, danger: true })) return
  try {
    await grf.post<any>(`/api/steward/conversions/${d.id}/cancel`)
    toast('已发出取消信号', { type: 'success' })
    await pullDetail(true)
    await load()
  } catch (e: any) { toast(e.message || '取消失败', { type: 'error' }) }
}

function openGoder() {
  const sid = detail.value?.session_id
  if (!sid) return
  windows.open('goder', { session: sid })
  detail.value = null
}

// ---- 已上架应用 ----
const appsCols: TableColumn[] = [
  { key: 'name', label: '应用' },
  { key: 'id', label: 'appId', width: '130px' },
  { key: 'category', label: '分类', width: '80px' },
  { key: 'version', label: '版本', width: '90px' },
  { key: 'type', label: '形态', width: '90px' },
]

const marketApps = computed(() => (market.value?.apps || []).slice().sort((a: any, b: any) =>
  String(a.id).localeCompare(String(b.id))))
const hostCount = computed(() => marketApps.value.filter((a: any) => a.type === 'host').length)
</script>

<template>
  <div class="tabpage">
    <section class="zone">
      <div class="zonebar">
        <h4>AI 转制上架</h4>
        <span class="dim">公开项目 → openos 应用：克隆 → AI 会话包装（引擎脚手架 vite 单文件工程）→ GitLab CI 构建 → 货架发布</span>
      </div>
      <div v-if="ready" class="starter">
        <OsInput v-model="srcURL" placeholder="https://github.com/xxx/yyy（公开项目地址）" style="flex: 1; min-width: 260px" @keyup.enter="start" />
        <OsButton variant="primary" :disabled="busy" @click="start">{{ busy ? '提交中…' : '开始转制' }}</OsButton>
      </div>
      <div v-else class="prereq">
        <p v-if="!activeGitLab" class="warn">⚠ 需要「活动 GitLab 连接」——目标项目与 CI 构建都落在 GitLab（在「仓库」页连接并设为数据源）。</p>
        <p v-if="!marketReady" class="warn">⚠ 货架目录未配置（GRF_OS_STEWARD_MARKET_DIR，生产为中央货架挂载路径），发布步不可用。</p>
      </div>
    </section>

    <section class="zone">
      <div class="zonebar">
        <h4>转制任务</h4>
        <span class="grow" />
        <OsButton size="sm" @click="load">刷新</OsButton>
      </div>
      <OsTable :columns="cols" :rows="convs" @row-click="openDetail">
        <template #cell-app_id="{ row }"><span class="mono">{{ row.app_id }}</span></template>
        <template #cell-source_url="{ row }">
          <span class="mono dim2" :title="row.source_url">{{ row.source_url }}</span>
        </template>
        <template #cell-state="{ row }">
          <span class="badge" :class="sm(row.state).cls">{{ sm(row.state).label }}</span>
          <em v-if="row.state === 'failed' && row.error" class="hint" :title="row.error">！</em>
        </template>
        <template #cell-version="{ row }">
          <span v-if="row.version" class="badge ok">{{ row.version }}</span>
          <span v-else class="dim2">—</span>
        </template>
      </OsTable>
      <OsEmpty v-if="!convs.length" text="暂无转制任务——填入公开项目地址，AI 把它变成你的桌面应用" />
    </section>

    <section class="zone">
      <div class="zonebar">
        <h4>货架应用</h4>
        <span class="dim">{{ marketApps.length }} 个（含 {{ hostCount }} 个宿主预装）· 转制产物经应用商店安装到桌面</span>
        <span class="grow" />
        <OsButton size="sm" @click="loadPre">刷新</OsButton>
      </div>
      <OsTable :columns="appsCols" :rows="marketApps">
        <template #cell-name="{ row }">
          <span class="appicon">{{ row.icon && String(row.icon).length <= 4 ? row.icon : '🧩' }}</span>
          <b>{{ row.name || row.id }}</b>
          <em class="hint" :title="row.description">{{ row.description ? ' · ' + String(row.description).slice(0, 40) : '' }}</em>
        </template>
        <template #cell-id="{ row }"><span class="mono dim2">{{ row.id }}</span></template>
        <template #cell-type="{ row }">
          <span v-if="row.type === 'host'" class="badge">宿主预装</span>
          <span v-else class="badge info">商店可装</span>
        </template>
      </OsTable>
      <OsEmpty v-if="!marketApps.length && marketReady" text="货架暂无应用" />
    </section>

    <!-- 任务详情 -->
    <OsModal v-if="detail" class="dlg-lg" :title="`转制 · ${detail.app_id}`" okText="关 闭" cancelText="关 闭" @ok="detail = null" @cancel="detail = null">
      <div class="detail">
        <div class="meta">
          <span class="badge" :class="sm(detail.state).cls">{{ sm(detail.state).label }}</span>
          <a v-if="detail.target_web" :href="detail.target_web" target="_blank" class="srclink">目标项目 ↗</a>
          <span v-if="detail.version" class="badge ok">{{ detail.version }}</span>
          <span v-if="detail.operator" class="dim">操作者 {{ detail.operator }}</span>
          <span>{{ String(detail.started_at || '').replace('T', ' ').slice(0, 19) }}</span>
        </div>
        <p class="dim src">{{ detail.source_url }}</p>
        <p v-if="detail.error" class="err">⚠ {{ detail.error }}</p>
        <div v-if="detail.summary" class="summary"><pre>{{ detail.summary }}</pre></div>
        <div class="log"><pre>{{ detailLog || '（暂无日志）' }}</pre></div>
        <div class="actions">
          <OsButton v-if="!terminal(detail.state)" size="sm" variant="danger" @click="cancel">取消转制</OsButton>
          <OsButton v-if="detail.state === 'failed'" size="sm" variant="primary" @click="srcURL = detail.source_url; detail = null">重新发起（填回地址）</OsButton>
          <span class="grow" />
          <OsButton v-if="detail.session_id" size="sm" variant="primary" @click="openGoder">在 Goder 中查看会话</OsButton>
        </div>
      </div>
    </OsModal>
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 16px; }
.zone { display: flex; flex-direction: column; gap: 8px; }
.zonebar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.zonebar h4 { margin: 0; font-size: 13px; font-weight: 600; color: var(--os-text-secondary); }
.grow { flex: 1; }
.starter { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.prereq p { margin: 0; font-size: 12.5px; }
.prereq .warn { color: var(--grf-warn); }
.badge { display: inline-block; padding: 1px 8px; border-radius: 8px; font-size: 11px; background: var(--os-active); }
.badge.ok { background: rgba(48, 209, 88, .16); color: var(--grf-ok); }
.badge.bad { background: rgba(255, 69, 58, .14); color: var(--grf-bad); }
.badge.info { background: rgba(10, 132, 255, .18); color: #6db2ff; }
.hint { font-size: 11px; color: var(--os-text-tertiary); font-style: normal; }
.dim { font-size: 11.5px; color: var(--os-text-tertiary); }
.dim2 { color: var(--os-text-tertiary); font-size: 11.5px; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.srclink { color: var(--os-accent, #4da3ff); text-decoration: none; }
.appicon { margin-right: 6px; }
.detail { display: flex; flex-direction: column; gap: 8px; }
.meta { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12px; color: var(--os-text-secondary); }
.src { margin: 0; }
p.err { margin: 0; font-size: 12px; color: var(--grf-bad); }
.summary { max-height: 18vh; overflow-y: auto; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); }
.summary pre { margin: 0; padding: 10px 12px; font-size: 12px; line-height: 1.6; white-space: pre-wrap; word-break: break-word; }
.log { max-height: 30vh; overflow-y: auto; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); }
.log pre { margin: 0; padding: 10px 12px; font-size: 11.5px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.actions { display: flex; align-items: center; gap: 8px; }
</style>
