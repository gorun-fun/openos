<script setup lang="ts">
// 项目 — git.gorun.fun 项目清单与版本/CI 概况：最新版本及其 CI 是否成功、
// 默认分支流水线、最近流水线；行内「详情」展开该项目的版本 × CI 与最近
// 流水线。所有流水线交互原地打开 PipeDetail（阶段 + job + 日志 + 失败转修），
// 不再跳 GitLab（仅项目详情内留一个外链兜底）。30s 轮询。
import { computed, ref } from 'vue'
import { OsButton, OsEmpty, OsModal, OsTable } from '../../../../ui'
import type { TableColumn } from '../../../../ui'
import { usePoll } from '../../usePoll'
import { fmtTime } from '../../core'
import { loadPipe, pipeProjects, pipeErr, pipeLoading } from '../data'
import type { PipeProject } from '../data'
import StatusChip from '../StatusChip.vue'
import PipeDetail from '../PipeDetail.vue'

const run = usePoll(loadPipe, 30_000)

const kw = ref('')
const rows = computed(() => {
  const k = kw.value.trim().toLowerCase()
  if (!k) return pipeProjects.value
  return pipeProjects.value.filter((p) =>
    p.path.toLowerCase().includes(k) || p.name.toLowerCase().includes(k))
})

const cols: TableColumn[] = [
  { key: 'name', label: '项目' },
  { key: 'latest', label: '最新版本', width: '110px' },
  { key: 'tagci', label: '版本 CI', width: '90px' },
  { key: 'branch', label: '默认分支', width: '150px' },
  { key: 'tags', label: '版本数', width: '70px' },
  { key: 'last', label: '最近流水线', width: '180px' },
  { key: 'activity', label: '最近活动', width: '150px' },
]

// 最新版本 CI 失败 / 默认分支失败的项目数（顶部摘要）
const broken = computed(() => pipeProjects.value.filter((p) =>
  p.latest_tag?.pipeline?.status === 'failed' || p.branch_pipeline?.status === 'failed').length)

// ---- 项目详情（版本 × CI + 最近流水线）----
const detail = ref<PipeProject | null>(null)
function openDetail(p: PipeProject) { detail.value = p }

// ---- 流水线详情（任何 CI chip / 流水线行点击；打开时收起项目详情避免弹窗叠层）----
const sel = ref<{ projectId: number; pipelineId: number } | null>(null)
function openPipe(p: PipeProject, pl: { id: number }) {
  detail.value = null
  sel.value = { projectId: p.id, pipelineId: pl.id }
}
</script>

<template>
  <div class="tabpage">
    <div class="pagebar">
      <input v-model="kw" class="in search" placeholder="搜索项目" />
      <OsButton @click="run">刷新</OsButton>
      <span class="dim">
        {{ rows.length }} 项目<template v-if="broken"> · <span class="bad">{{ broken }} 个失败待处理</span></template>
      </span>
    </div>
    <div v-if="pipeErr" class="err">GitLab：{{ pipeErr }}<template v-if="pipeProjects.length">（以下为上一轮数据）</template></div>

    <OsTable :columns="cols" :rows="rows">
      <template #cell-name="{ row }">
        <a class="plink" @click="openDetail(row as PipeProject)">{{ row.name }}</a>
        <span class="sub">{{ row.path }}</span>
        <span v-if="row.archived" class="arch">已归档</span>
        <em v-if="row.error" class="err-inline" :title="row.error">明细拉取失败</em>
      </template>
      <template #cell-latest="{ row }">
        <span v-if="row.latest_tag" class="mono tagv">{{ row.latest_tag.name }}</span>
        <span v-else class="dim">—</span>
      </template>
      <template #cell-tagci="{ row }">
        <button v-if="row.latest_tag?.pipeline" class="chipbtn" :title="`流水线 #${row.latest_tag.pipeline.id}`"
          @click.stop="openPipe(row as PipeProject, row.latest_tag.pipeline)">
          <StatusChip :status="row.latest_tag.pipeline.status" />
        </button>
        <StatusChip v-else status="" />
      </template>
      <template #cell-branch="{ row }">
        <span class="mono dim-b">{{ row.default_branch || '—' }}</span>
        <button v-if="row.branch_pipeline" class="chipbtn ml" :title="`流水线 #${row.branch_pipeline.id}`"
          @click.stop="openPipe(row as PipeProject, row.branch_pipeline)">
          <StatusChip :status="row.branch_pipeline.status" />
        </button>
      </template>
      <template #cell-tags="{ row }"><span class="mono">{{ row.tags.length || '—' }}</span></template>
      <template #cell-last="{ row }">
        <template v-if="row.pipelines[0]">
          <button class="chipbtn" :title="`流水线 #${row.pipelines[0].id}`"
            @click.stop="openPipe(row as PipeProject, row.pipelines[0])">
            <StatusChip :status="row.pipelines[0].status" />
          </button>
          <span class="sub ml">{{ row.pipelines[0].ref }} · {{ fmtTime(row.pipelines[0].updated_at, false) }}</span>
        </template>
        <span v-else class="dim">—</span>
      </template>
      <template #cell-activity="{ row }">{{ fmtTime(row.last_activity_at, false) }}</template>
      <template #ops="{ row }">
        <OsButton size="sm" @click.stop="openDetail(row as PipeProject)">详情</OsButton>
      </template>
    </OsTable>
    <OsEmpty v-if="!rows.length && !pipeLoading" text="GitLab 上没有可见项目——检查 GRF_OS_GITLAB_* 凭证配置" />

    <OsModal v-if="detail" class="dlg-lg" :title="`${detail.name}（${detail.path}）`" okText="关 闭" @ok="detail = null" @cancel="detail = null">
      <div class="detail">
        <div v-if="detail.error" class="err">{{ detail.error }}</div>
        <a class="ext" :href="detail.web_url" target="_blank" rel="noopener">项目主页 GitLab ↗</a>
        <p class="sec">版本 × CI/CD（点击 CI 状态看阶段与日志）</p>
        <table class="mtab" v-if="detail.tags.length">
          <thead><tr><th>版本</th><th>提交</th><th>CI/CD</th><th>时间</th></tr></thead>
          <tbody>
            <tr v-for="t in detail.tags" :key="t.name">
              <td class="mono tagv">{{ t.name }}</td>
              <td><span class="mono sha">{{ t.short_id }}</span><span class="sub" :title="t.title">{{ t.title }}</span></td>
              <td>
                <button v-if="t.pipeline" class="chipbtn" :title="`流水线 #${t.pipeline.id}`"
                  @click="openPipe(detail, t.pipeline)">
                  <StatusChip :status="t.pipeline.status" />
                </button>
                <StatusChip v-else status="" />
              </td>
              <td>{{ fmtTime(t.created_at, false) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="dim">尚无任何 tag</p>

        <p class="sec">最近流水线（点击行看阶段与日志）</p>
        <table class="mtab clickable" v-if="detail.pipelines.length">
          <thead><tr><th>#</th><th>ref</th><th>提交</th><th>状态</th><th>来源</th><th>更新时间</th></tr></thead>
          <tbody>
            <tr v-for="pl in detail.pipelines" :key="pl.id" @click="openPipe(detail, pl)">
              <td class="mono">{{ pl.id }}</td>
              <td class="mono">{{ pl.ref }}</td>
              <td class="mono dim-b">{{ pl.sha.slice(0, 8) }}</td>
              <td><StatusChip :status="pl.status" /></td>
              <td class="dim-b">{{ pl.source }}</td>
              <td>{{ fmtTime(pl.updated_at, false) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="dim">尚无流水线</p>
      </div>
    </OsModal>

    <PipeDetail v-if="sel" :key="`${sel.projectId}-${sel.pipelineId}`"
      :project-id="sel.projectId" :pipeline-id="sel.pipelineId" @close="sel = null" />
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 10px; }
.pagebar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.dim { font-size: 12px; color: var(--os-text-tertiary); }
.dim-b { color: var(--os-text-secondary); }
.bad { color: var(--grf-bad); }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.sub { margin-left: 7px; font-size: 11.5px; color: var(--os-text-tertiary); }
.ml { margin-left: 7px; }
.sha { margin-right: 6px; color: var(--os-text-tertiary); }
.arch { margin-left: 7px; padding: 1px 6px; border-radius: 6px; font-size: 10.5px; background: var(--os-active); color: var(--os-text-tertiary); }
.err-inline { margin-left: 7px; font-size: 11px; font-style: normal; color: var(--grf-bad); cursor: help; }
.plink { color: var(--os-accent, #4da3ff); cursor: pointer; }
.tagv { color: var(--os-text); font-weight: 500; }
.chipbtn { padding: 0; border: none; background: none; cursor: pointer; }
.ext { font-size: 12px; color: var(--os-accent, #4da3ff); text-decoration: none; align-self: flex-start; }
.err { padding: 8px 10px; border-radius: 8px; font-size: 12.5px; color: var(--grf-bad); background: color-mix(in srgb, var(--grf-bad) 10%, transparent); }
.in { padding: 6px 9px; border-radius: 7px; font-size: 12.5px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); }
.search { min-width: 200px; }
.detail { display: flex; flex-direction: column; gap: 8px; }
.sec { font-size: 12.5px; font-weight: 600; margin-top: 6px; }
.mtab { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.mtab th { text-align: left; color: var(--os-text-tertiary); font-weight: 500; padding: 4px 8px; border-bottom: 1px solid var(--os-border); }
.mtab td { padding: 5px 8px; border-bottom: 1px solid var(--os-border); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 320px; }
.mtab.clickable tbody tr { cursor: pointer; }
.mtab.clickable tbody tr:hover { background: var(--os-hover, rgba(128, 128, 128, 0.08)); }
</style>
