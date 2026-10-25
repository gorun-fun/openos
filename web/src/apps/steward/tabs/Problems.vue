<script setup lang="ts">
// 问题 — 待修复问题列表：来源三路（外部系统上报 / GitHub issue 同步 / 手动录入），
// 手动模式仓库的新问题停在此待确认（确认时可勾选本次发版），自动模式仓库的新
// 问题直接入队（列表可见状态流转）。5s 轮询跟进状态。
import { computed, ref } from 'vue'
import { OsButton, OsInput, OsModal, OsForm, OsField, OsSelect, OsTable, OsEmpty, OsToggle, toast, osConfirm } from '../../../ui'
import type { TableColumn } from '../../../ui'
import { useWindows } from '../../../stores/windows'
import { grf } from '../core'
import { usePoll } from '../usePoll'
import { loadPipe, pipeErr, pipeProjects, type PipeProject } from '../pipeline/data'

const windows = useWindows()

const problems = ref<any[]>([])
const repos = ref<any[]>([])
const fState = ref('')
const fRepo = ref('')
const fQ = ref('')

async function load() {
  try {
    const qs = new URLSearchParams()
    if (fState.value) qs.set('state', fState.value)
    if (fRepo.value) qs.set('repo', fRepo.value)
    if (fQ.value.trim()) qs.set('q', fQ.value.trim())
    problems.value = (await grf.get<any>(`/api/steward/problems?${qs}`)) || []
  } catch (e: any) { toast(e.message || '问题列表加载失败', { type: 'error' }) }
}
// 仓库清单只供筛选下拉与录入选择，不随问题轮询（录入选 GitLab 项目会自动接管，
// 录入后刷新一次）
async function loadRepos() {
  try { repos.value = (await grf.get<any>('/api/steward/repos')) || [] } catch { /* 列表失败不阻断问题页 */ }
}
usePoll(load, 5_000)
loadRepos()

const stateMeta: Record<string, { label: string; cls: string }> = {
  new: { label: '待确认', cls: 'warn' },
  confirmed: { label: '排队中', cls: 'info' },
  running: { label: '修复中', cls: 'info' },
  fixed: { label: '已修复', cls: 'ok' },
  failed: { label: '失败', cls: 'bad' },
  rejected: { label: '已驳回', cls: '' },
  canceled: { label: '已取消', cls: '' },
}
const srcMeta: Record<string, string> = { report: '上报', github: 'GitHub', gitlab: 'GitLab', pipeline: '流水线', manual: '手动' }
function sm(s: string) { return stateMeta[s] || { label: s, cls: '' } }

// 全部仓库手动同步一轮 issue（GitLab/GitLab 双平台，问题页即时入口）
const syncBusy = ref(false)
async function syncAllIssues() {
  syncBusy.value = true
  try {
    const r = await grf.post<any>('/api/steward/sync-issues')
    const errs: string[] = Array.isArray(r?.errors) ? r.errors : []
    if (errs.length) toast(`同步完成：新增 ${r?.added || 0} 个问题，${errs.length} 个仓库失败——${errs.join('；')}`, { type: 'error' })
    else toast(r?.added ? `同步完成：新增 ${r.added} 个问题` : '同步完成：无新问题', { type: 'success' })
    await load()
  } catch (e: any) { toast(e.message || '同步失败', { type: 'error' }) } finally { syncBusy.value = false }
}

const cols: TableColumn[] = [
  { key: 'title', label: '标题' },
  { key: 'repo_name', label: '仓库', width: '110px' },
  { key: 'source', label: '来源', width: '80px' },
  { key: 'state', label: '状态', width: '90px' },
  { key: 'created_at', label: '时间', width: '168px', format: (v: string) => String(v || '').replace('T', ' ').slice(0, 19) },
]

// ---- 详情/确认 ----
const detail = ref<any>(null)
const detailRun = ref<any>(null)
const confirmRelease = ref(false)
const busy = ref(false)

const canConfirm = computed(() => ['new', 'failed', 'canceled'].includes(detail.value?.state))
const canCancel = computed(() => ['confirmed', 'running'].includes(detail.value?.state))

async function openDetail(p: any) {
  detail.value = p
  confirmRelease.value = !!p.release
  detailRun.value = null
  if (p.run_id) {
    try {
      const r = await grf.get<any>(`/api/steward/runs/${p.run_id}?log=0`)
      detailRun.value = r?.run || null
    } catch { /* 记录可能已被清理 */ }
  }
}

function openGoder() {
  const sid = detailRun.value?.session_id
  if (!sid) return
  windows.open('goder', { session: sid })
  detail.value = null
}

async function act(path: string, body?: any, okMsg?: string) {
  busy.value = true
  try {
    await grf.post<any>(`/api/steward/problems/${detail.value.id}/${path}`, body)
    toast(okMsg || '操作成功', { type: 'success' })
    detail.value = null
    await load()
  } catch (e: any) { toast(e.message || '操作失败', { type: 'error' }) } finally { busy.value = false }
}

async function confirmFix() {
  await act('confirm', { release: confirmRelease.value }, '已确认，进入修复队列')
}
async function reject() {
  if (!await osConfirm({ title: '驳回问题', body: `驳回「${detail.value.title}」？不再代码管家。`, danger: true })) return
  await act('reject', undefined, '已驳回')
}
async function cancel() {
  if (!await osConfirm({ title: '取消修复', body: '取消该问题的修复（运行中将中止当前修复进程）？', danger: true })) return
  await act('cancel', undefined, '已取消')
}
async function retryFromFail() {
  // 失败重试：经最近一次记录走 retry（问题回队列）
  busy.value = true
  try {
    if (detail.value.run_id) await grf.post<any>(`/api/steward/runs/${detail.value.run_id}/retry`)
    else await grf.post<any>(`/api/steward/problems/${detail.value.id}/confirm`, {})
    toast('已重新入队', { type: 'success' })
    detail.value = null
    await load()
  } catch (e: any) { toast(e.message || '重试失败', { type: 'error' }) } finally { busy.value = false }
}

// ---- 手动录入 ----
// 仓库可选已接管仓库，也可直接选 GitLab 项目（未接管的录入时服务端自动注册，
// 同流水线转修；与已接管仓库同名视为已接管不再重复列出，后端按克隆地址去重）
const addDlg = ref(false)
const addForm = ref<any>({})
const glPickable = computed<PipeProject[]>(() => {
  const taken = new Set(repos.value.map((r: any) => r.name))
  return pipeProjects.value.filter((p) => !p.archived && !p.empty_repo && !taken.has(p.path))
})
const isGlRepo = (v: string) => String(v || '').startsWith('gl:')

function openAdd() {
  addForm.value = { repo_id: repos.value[0]?.id || '', title: '', body: '' }
  addDlg.value = true
  if (!pipeProjects.value.length) {
    // GitLab 项目按需拉取（服务端 30s 缓存；失败仅在弹窗内提示）
    loadPipe().finally(() => {
      if (addDlg.value && !addForm.value.repo_id && glPickable.value.length) {
        addForm.value.repo_id = 'gl:' + glPickable.value[0].id
      }
    })
  }
}
async function saveAdd() {
  const { repo_id, title, body } = addForm.value
  if (!title || !repo_id) { toast('仓库与标题必填', { type: 'error' }); return }
  busy.value = true
  try {
    const payload: Record<string, unknown> = { title, body }
    if (isGlRepo(repo_id)) payload.gitlab_project_id = Number(repo_id.slice(3))
    else payload.repo_id = repo_id
    await grf.post<any>('/api/steward/problems', payload)
    toast('已录入', { type: 'success' })
    addDlg.value = false
    await Promise.all([load(), loadRepos()]) // GitLab 项目录入即自动接管，仓库下拉同步刷新
  } catch (e: any) { toast(e.message || '录入失败', { type: 'error' }) } finally { busy.value = false }
}

// ---- 修复记录展示辅助 ----
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
const runLabel = (s: string) => runMeta[s]?.label || s
const runCls = (s: string) => runMeta[s]?.cls || ''
</script>

<template>
  <div class="tabpage">
    <div class="pagebar">
      <OsSelect v-model="fState" :options="[{ value: '', label: '全部状态' }, ...Object.entries(stateMeta).map(([value, m]) => ({ value, label: m.label }))]" style="width: 130px" @update:model-value="load" />
      <OsSelect v-model="fRepo" :options="[{ value: '', label: '全部仓库' }, ...repos.map((r) => ({ value: r.id, label: r.name }))]" style="width: 150px" @update:model-value="load" />
      <OsInput v-model="fQ" placeholder="搜索标题 / 上报人" style="width: 180px" @keyup.enter="load" />
      <OsButton :disabled="syncBusy" @click="syncAllIssues">同步 issue</OsButton>
      <OsButton @click="load">刷新</OsButton>
      <span class="grow" />
      <OsButton variant="primary" @click="openAdd">手动录入</OsButton>
    </div>

    <OsTable :columns="cols" :rows="problems" @row-click="openDetail">
      <template #cell-title="{ row }">
        <span :title="row.title">{{ row.title }}</span>
      </template>
      <template #cell-source="{ row }">
        <a v-if="row.ext_url" :href="row.ext_url" target="_blank" class="srclink" @click.stop>{{ srcMeta[row.source] || row.source }} ↗</a>
        <span v-else>{{ srcMeta[row.source] || row.source }}</span>
      </template>
      <template #cell-state="{ row }">
        <span class="badge" :class="sm(row.state).cls">{{ sm(row.state).label }}</span>
        <em v-if="row.state === 'failed' && row.error" class="hint" :title="row.error">！</em>
      </template>
    </OsTable>
    <OsEmpty v-if="!problems.length" text="暂无问题（GitLab/GitHub issue 同步 / 外部系统上报 / 流水线转修 / 手动录入）" />

    <!-- 详情 / 确认 -->
    <OsModal v-if="detail" class="dlg-lg" :title="detail.title" okText="关 闭" cancelText="关 闭" @ok="detail = null" @cancel="detail = null">
      <div class="detail">
        <div class="meta">
          <span class="badge" :class="sm(detail.state).cls">{{ sm(detail.state).label }}</span>
          <span>{{ detail.repo_name }}</span>
          <span>来源：{{ srcMeta[detail.source] || detail.source }}<template v-if="detail.reporter">（{{ detail.reporter }}）</template></span>
          <span>{{ String(detail.created_at || '').replace('T', ' ').slice(0, 19) }}</span>
          <a v-if="detail.ext_url" :href="detail.ext_url" target="_blank">原文 ↗</a>
        </div>
        <div v-if="detail.labels?.length" class="labels">
          <span v-for="l in detail.labels" :key="l" class="badge">{{ l }}</span>
        </div>
        <pre class="body">{{ detail.body || '（无描述）' }}</pre>
        <p v-if="detail.error" class="err">⚠ {{ detail.error }}</p>

        <!-- 最近一次修复：状态跟踪（会话内容在操作者 Goder「代码管家」分组） -->
        <div v-if="detailRun" class="lastrun">
          <div class="meta">
            <b>最近修复</b>
            <span class="badge" :class="runCls(detailRun.state)">{{ runLabel(detailRun.state) }}</span>
            <span v-if="detailRun.model">{{ detailRun.model }}</span>
            <span v-if="detailRun.operator">操作者 {{ detailRun.operator }}</span>
            <span v-if="detailRun.commit">commit {{ String(detailRun.commit).slice(0, 8) }}</span>
            <span v-if="detailRun.tag">tag {{ detailRun.tag }}</span>
            <span class="grow" />
            <OsButton v-if="detailRun.session_id" size="sm" @click="openGoder">在 Goder 中查看会话</OsButton>
          </div>
          <pre v-if="detailRun.summary" class="body sum">{{ detailRun.summary }}</pre>
        </div>

        <div class="actions">
          <template v-if="canConfirm">
            <label class="rel"><OsToggle v-model="confirmRelease" /> 修复完成后发版（tag 末位 +1）</label>
            <OsButton variant="primary" size="sm" :disabled="busy" @click="confirmFix">确认修复</OsButton>
            <OsButton v-if="detail.state === 'new'" size="sm" :disabled="busy" @click="reject">驳回</OsButton>
          </template>
          <OsButton v-if="detail.state === 'failed'" size="sm" variant="primary" :disabled="busy" @click="retryFromFail">重新入队</OsButton>
          <OsButton v-if="canCancel" size="sm" variant="danger" :disabled="busy" @click="cancel">取消修复</OsButton>
        </div>
      </div>
    </OsModal>

    <!-- 手动录入 -->
    <OsModal v-if="addDlg" title="手动录入问题" okText="录 入" cancelText="取 消" @ok="saveAdd" @cancel="addDlg = false">
      <OsForm :model="addForm">
        <OsField label="仓库" required>
          <select v-model="addForm.repo_id" class="sel">
            <optgroup label="已接管仓库">
              <option v-for="r in repos" :key="r.id" :value="r.id">{{ r.name }}（{{ r.mode === 'auto' ? '自动模式，录入即修复' : '手动模式' }}）</option>
            </optgroup>
            <optgroup v-if="glPickable.length" label="GitLab 项目（未接管的录入时自动接管）">
              <option v-for="p in glPickable" :key="'gl:' + p.id" :value="'gl:' + p.id">{{ p.path }}</option>
            </optgroup>
          </select>
          <p v-if="isGlRepo(addForm.repo_id)" class="hintline">所选 GitLab 项目尚未接管：录入时将自动注册（手动模式，推送用服务端 GitLab 凭证），稍后可在「仓库」页调整。</p>
          <p v-else-if="pipeErr && !glPickable.length" class="hintline">GitLab 项目加载失败：{{ pipeErr }}</p>
        </OsField>
        <OsField label="标题" required><OsInput v-model="addForm.title" placeholder="一句话说清问题" /></OsField>
        <OsField label="描述">
          <textarea v-model="addForm.body" class="ta" rows="6" placeholder="现象 / 复现步骤 / 期望行为 / 相关日志……越具体修复越准" />
        </OsField>
      </OsForm>
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
.srclink { color: var(--os-accent); text-decoration: none; }
.detail { display: flex; flex-direction: column; gap: 10px; }
.meta { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12px; color: var(--os-text-secondary); }
.labels { display: flex; gap: 6px; flex-wrap: wrap; }
pre.body { margin: 0; padding: 10px 12px; border-radius: 8px; background: var(--os-input-bg); border: 1px solid var(--os-border); font-size: 12.5px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; max-height: 34vh; overflow-y: auto; }
p.err { margin: 0; font-size: 12px; color: var(--grf-bad); }
.actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.rel { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--os-text-secondary); }
.lastrun { display: flex; flex-direction: column; gap: 6px; border-top: 1px dashed var(--os-border); padding-top: 8px; }
pre.body.sum { max-height: 20vh; font-size: 12px; }
.sel { box-sizing: border-box; width: 100%; padding: 6px 9px; border-radius: 8px; font-size: 13px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); }
.ta { box-sizing: border-box; width: 100%; padding: 7px 10px; border-radius: 8px; font-size: 12.5px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); font-family: inherit; resize: vertical; }
.hintline { margin: 0; font-size: 11.5px; line-height: 1.5; color: var(--os-text-tertiary); }
</style>
