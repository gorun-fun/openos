<script setup lang="ts">
// 版本 — git.gorun.fun 全部项目的版本（tag）× 对应 CI/CD 是否成功：
// 跨项目摊平按打 tag 时间降序，可按项目 / CI 状态过滤、按项目或版本号搜索。
// 点版本行 / CI 状态原地打开流水线详情（PipeDetail：阶段 + job + 日志 +
// 失败转修），不再跳 GitLab。数据来自 /api/v1/steward/pipe/projects（30s 缓存），30s 轮询。
import { computed, ref } from 'vue'
import { OsButton, OsEmpty, OsTable } from '../../../../ui'
import type { TableColumn } from '../../../../ui'
import { usePoll } from '../../usePoll'
import { fmtTime } from '../../core'
import { loadPipe, pipeProjects, pipeErr, pipeLoading, pipeFetchedAt } from '../data'
import type { PipeProject, PipeTag } from '../data'
import StatusChip from '../StatusChip.vue'
import PipeDetail from '../PipeDetail.vue'

const run = usePoll(loadPipe, 30_000)

const project = ref('') // 空 = 全部
const status = ref('')  // 空 = 全部
const kw = ref('')
const onlyFailed = ref(false) // 「只看失败」快捷过滤（与状态下拉叠加）

const tagged = computed(() => pipeProjects.value.filter((p) => p.tags.length))
const statusOpts = [
  { v: '', label: '全部状态' },
  { v: 'success', label: '成功' },
  { v: 'failed', label: '失败' },
  { v: 'active', label: '进行中' },
  { v: 'none', label: '无 CI' },
  { v: 'canceled', label: '已取消' },
  { v: 'manual', label: '手动' },
]

const ACTIVE = ['running', 'pending', 'preparing', 'created', 'waiting_for_resource', 'scheduled']
function matchStatus(t: PipeTag): boolean {
  if (onlyFailed.value) return t.pipeline?.status === 'failed'
  const f = status.value
  if (!f) return true
  const s = t.pipeline?.status
  if (f === 'none') return !s
  if (f === 'active') return !!s && ACTIVE.includes(s)
  return s === f
}

interface Row { p: PipeProject; t: PipeTag }
const rows = computed<Row[]>(() => {
  const k = kw.value.trim().toLowerCase()
  const out: Row[] = []
  for (const p of pipeProjects.value) {
    if (project.value && p.id !== Number(project.value)) continue
    for (const t of p.tags) {
      if (!matchStatus(t)) continue
      if (k && !p.path.toLowerCase().includes(k) && !p.name.toLowerCase().includes(k)
        && !t.name.toLowerCase().includes(k) && !t.short_id.toLowerCase().includes(k)) continue
      out.push({ p, t })
    }
  }
  out.sort((a, b) => Date.parse(b.t.created_at || '') - Date.parse(a.t.created_at || ''))
  return out
})

const cols: TableColumn[] = [
  { key: 'path', label: '项目', width: '200px' },
  { key: 'tag', label: '版本', width: '110px' },
  { key: 'commit', label: '提交' },
  { key: 'ci', label: 'CI/CD', width: '90px' },
  { key: 'created', label: '时间', width: '150px' },
]

const failed = computed(() => rows.value.filter((r) => r.t.pipeline?.status === 'failed').length)

// ---- 流水线详情（行点击 / CI chip 点击）----
const sel = ref<{ projectId: number; pipelineId: number } | null>(null)
function openPipe(p: PipeProject, pl: { id: number }) {
  sel.value = { projectId: p.id, pipelineId: pl.id }
}
function rowPipe(row: Record<string, any>) {
  const r = row as Row
  if (r.t?.pipeline) openPipe(r.p, r.t.pipeline)
}
</script>

<template>
  <div class="tabpage">
    <div class="pagebar">
      <select v-model="project" class="in">
        <option value="">全部项目</option>
        <option v-for="p in tagged" :key="p.id" :value="String(p.id)">{{ p.path }}（{{ p.tags.length }}）</option>
      </select>
      <select v-model="status" class="in" :disabled="onlyFailed">
        <option v-for="o in statusOpts" :key="o.v" :value="o.v">{{ o.label }}</option>
      </select>
      <input v-model="kw" class="in search" placeholder="搜索项目 / 版本 / 提交" />
      <OsButton :variant="onlyFailed ? 'primary' : 'plain'" @click="onlyFailed = !onlyFailed">只看失败</OsButton>
      <OsButton @click="run">刷新</OsButton>
      <span class="dim">
        {{ pipeProjects.length }} 项目 · {{ rows.length }} 版本<template v-if="failed"> · <span class="bad">{{ failed }} 失败</span></template>
      </span>
    </div>
    <div v-if="pipeErr" class="err">GitLab：{{ pipeErr }}<template v-if="pipeProjects.length">（以下为上一轮数据）</template></div>

    <OsTable :columns="cols" :rows="rows" @row-click="rowPipe">
      <template #cell-path="{ row }">
        <span class="path" :title="row.p.path">{{ row.p.path }}</span>
      </template>
      <template #cell-tag="{ row }">
        <span class="mono tagv" :class="{ linked: !!row.t.pipeline }">{{ row.t.name }}</span>
      </template>
      <template #cell-commit="{ row }">
        <span class="mono sha">{{ row.t.short_id }}</span>
        <span class="title" :title="row.t.title">{{ row.t.title }}</span>
      </template>
      <template #cell-ci="{ row }">
        <button v-if="row.t.pipeline" class="chipbtn" :title="`流水线 #${row.t.pipeline.id} · ${row.t.pipeline.source} · ${fmtTime(row.t.pipeline.updated_at)}`"
          @click.stop="openPipe(row.p, row.t.pipeline)">
          <StatusChip :status="row.t.pipeline.status" />
        </button>
        <StatusChip v-else status="" />
      </template>
      <template #cell-created="{ row }">{{ fmtTime(row.t.created_at) }}</template>
    </OsTable>
    <OsEmpty v-if="!rows.length && !pipeLoading" text="没有匹配的版本——项目打 tag 后出现在这里（无任何 tag 的项目在「项目」页）" />

    <PipeDetail v-if="sel" :key="`${sel.projectId}-${sel.pipelineId}`"
      :project-id="sel.projectId" :pipeline-id="sel.pipelineId" @close="sel = null" />
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 10px; }
.pagebar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.dim { font-size: 12px; color: var(--os-text-tertiary); }
.bad { color: var(--grf-bad); }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.sha { margin-right: 6px; color: var(--os-text-tertiary); }
.title { color: var(--os-text-secondary); }
.path { cursor: default; }
.tagv.linked { color: var(--os-text); font-weight: 500; }
.chipbtn { padding: 0; border: none; background: none; cursor: pointer; }
.err { padding: 8px 10px; border-radius: 8px; font-size: 12.5px; color: var(--grf-bad); background: color-mix(in srgb, var(--grf-bad) 10%, transparent); }
.in { padding: 6px 9px; border-radius: 7px; font-size: 12.5px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); }
.search { min-width: 200px; }
</style>
