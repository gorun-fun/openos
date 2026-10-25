<script setup lang="ts">
// 修复记录 — 状态跟踪：阶段、模型、操作者、commit/tag、diff、修复摘要。
// Goder 执行（默认）只做状态跟踪，会话内容在操作者的 Goder「代码管家」分组里
// （「在 Goder 中查看会话」深链直达）；旧 gh 执行/兜底模式保留日志跟随。
import { computed, ref } from 'vue'
import { OsButton, OsModal, OsSelect, OsTable, OsEmpty, toast, osConfirm } from '../../../ui'
import type { TableColumn } from '../../../ui'
import { useWindows } from '../../../stores/windows'
import { grf } from '../core'
import { usePoll } from '../usePoll'

const windows = useWindows()
const runs = ref<any[]>([])
const repos = ref<any[]>([])
const fRepo = ref('')

const runMeta: Record<string, { label: string; cls: string }> = {
  prepare: { label: '准备', cls: 'info' },
  fixing: { label: '修复中', cls: 'info' },
  committing: { label: '提交', cls: 'info' },
  pushing: { label: '推送', cls: 'info' },
  releasing: { label: '发版', cls: 'info' },
  done: { label: '完成', cls: 'ok' },
  no_changes: { label: '无改动', cls: 'warn' },
  failed: { label: '失败', cls: 'bad' },
  canceled: { label: '已取消', cls: '' },
}
const sm = (s: string) => runMeta[s] || { label: s, cls: '' }
const terminal = (s: string) => ['done', 'no_changes', 'failed', 'canceled'].includes(s)

async function load() {
  try {
    const qs = fRepo.value ? `?repo=${fRepo.value}` : ''
    runs.value = (await grf.get<any>(`/api/steward/runs${qs}`)) || []
  } catch (e: any) { toast(e.message || '记录加载失败', { type: 'error' }) }
}
// 仓库清单只供筛选下拉，不随记录轮询
async function loadRepos() {
  try { repos.value = (await grf.get<any>('/api/steward/repos')) || [] } catch { /* 列表失败不阻断记录页 */ }
}
usePoll(load, 5_000)
loadRepos()

function dur(r: any) {
  if (!r?.started_at) return '—'
  const start = new Date(r.started_at).getTime()
  const end = r.ended_at ? new Date(r.ended_at).getTime() : Date.now()
  const s = Math.max(0, Math.round((end - start) / 1000))
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${s % 60}s`
}

const cols: TableColumn[] = [
  { key: 'problem_title', label: '问题' },
  { key: 'repo_name', label: '仓库', width: '100px' },
  { key: 'state', label: '状态', width: '86px' },
  { key: 'model', label: '模型', width: '150px' },
  { key: 'executor', label: '执行', width: '70px' },
  { key: 'commit', label: '提交 / 发版', width: '170px' },
  { key: 'files', label: '改动', width: '110px', align: 'right' },
  { key: 'dur', label: '耗时', width: '70px', align: 'right' },
]

// ---- 详情（goder：状态卡 + 摘要 + Goder 深链；gh：日志跟随）----
const detail = ref<any>(null)
const logText = ref('')
const logOffset = ref(0)
const logBox = ref<HTMLElement | null>(null)
const follow = ref(true)

const isGoder = (r: any) => r?.executor === 'goder' || !!r?.session_id

async function openDetail(r: any) {
  detail.value = r
  logText.value = ''
  logOffset.value = 0
  follow.value = true
  await pullDetail(true)
}

async function pullDetail(reset = false) {
  const d = detail.value
  if (!d) return
  try {
    const wantLog = !isGoder(d)
    const res = await grf.get<any>(`/api/steward/runs/${d.id}?log=${reset || !wantLog ? 0 : logOffset.value}`)
    detail.value = res?.run || d
    if (!isGoder(detail.value) && res?.log) {
      logText.value += res.log
      logOffset.value = res.log_offset ?? logOffset.value
      if (follow.value && logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    }
    // 终态停止跟随轮询（列表 5s 轮询仍会刷新外层）
    if (terminal(detail.value.state)) follow.value = false
  } catch { /* 记录可能已被清理 */ }
}
usePoll(() => { if (detail.value && follow.value) pullDetail() }, 2_000)

function openGoder() {
  const d = detail.value
  if (!d?.session_id) return
  windows.open('goder', { session: d.session_id })
  detail.value = null
}

async function cancel() {
  const d = detail.value
  if (!await osConfirm({ title: '取消修复', body: '中止当前修复？Goder 会话保留在「代码管家」分组，可人工跟进。', danger: true })) return
  try {
    await grf.post<any>(`/api/steward/runs/${d.id}/cancel`)
    toast('已发出取消信号', { type: 'success' })
    await pullDetail(true)
  } catch (e: any) { toast(e.message || '取消失败', { type: 'error' }) }
}
async function retry() {
  try {
    await grf.post<any>(`/api/steward/runs/${detail.value.id}/retry`)
    toast('已重新入队', { type: 'success' })
    detail.value = null
    await load()
  } catch (e: any) { toast(e.message || '重试失败', { type: 'error' }) }
}

const canCancel = computed(() => detail.value && !terminal(detail.value.state))
const canRetry = computed(() => detail.value && ['failed', 'no_changes', 'canceled'].includes(detail.value.state))
</script>

<template>
  <div class="tabpage">
    <div class="pagebar">
      <OsSelect v-model="fRepo" :options="[{ value: '', label: '全部仓库' }, ...repos.map((r) => ({ value: r.id, label: r.name }))]" style="width: 160px" @update:model-value="load" />
      <OsButton @click="load">刷新</OsButton>
    </div>

    <OsTable :columns="cols" :rows="runs" @row-click="openDetail">
      <template #cell-problem_title="{ row }">
        <span :title="row.problem_title">{{ row.problem_title }}</span>
      </template>
      <template #cell-state="{ row }">
        <span class="badge" :class="sm(row.state).cls">{{ sm(row.state).label }}</span>
      </template>
      <template #cell-executor="{ row }">
        <span class="mono dim2">{{ row.executor === 'gh' || (!row.executor && !row.session_id) ? 'gh' : 'Goder' }}</span>
      </template>
      <template #cell-commit="{ row }">
        <span v-if="row.tag" class="badge ok">{{ row.tag }}</span>
        <span class="mono dim2">{{ String(row.commit || '').slice(0, 8) }}</span>
        <em v-if="row.error" class="hint" :title="row.error"> ⚠</em>
      </template>
      <template #cell-files="{ row }">
        <span v-if="row.files">{{ row.files }} 文件 <em class="ins">+{{ row.ins }}</em>/<em class="dels">-{{ row.dels }}</em></span>
        <span v-else class="dim">—</span>
      </template>
      <template #cell-dur="{ row }">{{ dur(row) }}</template>
    </OsTable>
    <OsEmpty v-if="!runs.length" text="暂无修复记录（确认问题后自动开始）" />

    <OsModal v-if="detail" class="dlg-lg" :title="detail.problem_title || '修复记录'" okText="关 闭" cancelText="关 闭" @ok="detail = null" @cancel="detail = null">
      <div class="detail">
        <div class="meta">
          <span class="badge" :class="sm(detail.state).cls">{{ sm(detail.state).label }}</span>
          <span>{{ detail.repo_name }}</span>
          <span v-if="detail.model" class="mono">{{ detail.model }}</span>
          <span v-if="detail.operator" class="dim">操作者 {{ detail.operator }}</span>
          <span v-if="detail.release" class="badge warn">发版</span>
          <span>{{ String(detail.started_at || '').replace('T', ' ').slice(0, 19) }} · {{ dur(detail) }}</span>
        </div>
        <div class="meta">
          <span v-if="detail.commit" class="mono">commit {{ detail.commit.slice(0, 10) }}</span>
          <span v-if="detail.tag" class="badge ok">{{ detail.tag }}</span>
          <span v-if="detail.files">{{ detail.files }} 文件（+{{ detail.ins }} / -{{ detail.dels }}）</span>
        </div>
        <p v-if="detail.error" class="err">⚠ {{ detail.error }}</p>

        <!-- Goder 执行：状态跟踪 + 摘要；完整会话在操作者 Goder 里 -->
        <template v-if="isGoder(detail)">
          <p v-if="!terminal(detail.state)" class="hint-line">修复会话正在 Goder 中执行，此处只跟进度；完整过程与续聊在 Goder「代码管家」分组。</p>
          <div v-if="detail.summary" class="summary"><pre>{{ detail.summary }}</pre></div>
          <div class="actions">
            <OsButton v-if="canCancel" size="sm" variant="danger" @click="cancel">取消修复</OsButton>
            <OsButton v-if="canRetry" size="sm" variant="primary" @click="retry">重新入队</OsButton>
            <span class="grow" />
            <OsButton size="sm" variant="primary" @click="openGoder">在 Goder 中查看会话</OsButton>
          </div>
        </template>

        <!-- gh 执行（兜底/旧记录）：原始日志跟随 -->
        <template v-else>
          <div ref="logBox" class="log"><pre>{{ logText || '（日志等待输出…）' }}</pre></div>
          <div class="actions">
            <OsButton v-if="canCancel" size="sm" variant="danger" @click="cancel">取消修复</OsButton>
            <OsButton v-if="canRetry" size="sm" variant="primary" @click="retry">重新入队</OsButton>
            <span class="grow" />
            <OsButton size="sm" @click="pullDetail(true)">日志从头刷新</OsButton>
          </div>
        </template>
      </div>
    </OsModal>
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 10px; }
.pagebar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.grow { flex: 1; }
.badge { display: inline-block; padding: 1px 8px; border-radius: 8px; font-size: 11px; background: var(--os-active); }
.badge.ok { background: rgba(48, 209, 88, .16); color: var(--grf-ok); }
.badge.bad { background: rgba(255, 69, 58, .14); color: var(--grf-bad); }
.badge.warn { background: rgba(255, 159, 10, .16); color: #ffb340; }
.badge.info { background: rgba(10, 132, 255, .18); color: #6db2ff; }
.hint { font-size: 11px; color: var(--os-text-tertiary); font-style: normal; }
.dim { color: var(--os-text-tertiary); }
.dim2 { color: var(--os-text-secondary); font-size: 11.5px; }
.mono { font-family: ui-monospace, monospace; font-size: 11.5px; }
em.ins { color: var(--grf-ok); font-style: normal; }
em.dels { color: var(--grf-bad); font-style: normal; }
.detail { display: flex; flex-direction: column; gap: 8px; }
.meta { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12px; color: var(--os-text-secondary); }
p.err { margin: 0; font-size: 12px; color: var(--grf-bad); }
p.hint-line { margin: 0; font-size: 12px; color: var(--os-text-tertiary); }
.summary { max-height: 30vh; overflow-y: auto; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); }
.summary pre { margin: 0; padding: 10px 12px; font-size: 12px; line-height: 1.6; white-space: pre-wrap; word-break: break-word; }
.log { max-height: 44vh; overflow-y: auto; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); }
.log pre { margin: 0; padding: 10px 12px; font-size: 11.5px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.actions { display: flex; align-items: center; gap: 8px; }
</style>
