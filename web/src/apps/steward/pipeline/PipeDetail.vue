<script setup lang="ts">
// PipeDetail — 流水线详情弹窗（v0.0.19）：阶段步条 + job 清单 + 内嵌日志 +
// 失败转修 + job 运行控制。版本/项目页点任何流水线都在这里原地展开
// 「哪一步错、错在哪」，不再跳 GitLab（仅角落留外链兜底）。数据：
// GET /pipe/pipelines/{p}/{pl}（5s 轮询至终态）+ GET /pipe/jobs/{p}/{j}/trace
//（运行中 job 2s 跟随，ANSI 已在服务端剥离）；失败可一键 POST /pipe/fix 转
// 代码管家（steward 引擎）；job 可原地运行（manual 播放 = 发应用、重放历史发布
// pipeline 的 job = 按原版本回滚上架、终态重试、运行中取消——
// POST /pipe/jobs/{p}/{j}/run|cancel）。
import { computed, inject, ref, watch } from 'vue'
import { OsButton, OsModal, toast } from '../../../ui'
import { usePoll } from '../usePoll'
import { fmtTime } from '../core'
import {
  loadPipeDetail, loadPipeTrace, sendPipeFix, runPipeJob,
  reasonOf, fixStateOf, fmtDur, pipeTerminal,
} from './data'
import type { PipeDetail, PipeFixResp, PipeJob } from './data'
import StatusChip from './StatusChip.vue'

const props = defineProps<{ projectId: number; pipelineId: number }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const detail = ref<PipeDetail | null>(null)
const err = ref('')
const stage = ref('') // 选中阶段过滤（空 = 全部）

const pl = computed(() => detail.value?.pipeline)
const active = computed(() => !pl.value || !pipeTerminal(pl.value.status))
const shownJobs = computed(() =>
  stage.value ? (detail.value?.jobs || []).filter((j) => j.stage === stage.value) : (detail.value?.jobs || []))
const failJob = computed(() => (detail.value?.jobs || []).find((j) => j.status === 'failed' && !j.allow_failure) || null)

// ---- 详情加载（运行中 5s 轮询；终态停。force 用于 job 操作后绕过终态守卫）----
async function load(force = false) {
  if (!force && detail.value && !active.value) return
  try {
    const d = await loadPipeDetail(props.projectId, props.pipelineId)
    detail.value = d
    err.value = ''
    syncJob(d)
    if (!job.value && d.pipeline.status === 'failed' && failJob.value) openJob(failJob.value)
  } catch (e: any) {
    err.value = e.message || '流水线详情加载失败'
  }
}
usePoll(load, 5_000)

// ---- 选中 job 与日志跟随（照 steward Runs 的增量四件套）----
const job = ref<PipeJob | null>(null)
const logText = ref('')
const logOffset = ref(0)
const follow = ref(false)
const logBox = ref<HTMLElement | null>(null)

// 详情刷新时同步选中 job 的最新状态；到终态自动停跟随
function syncJob(d: PipeDetail) {
  if (!job.value) return
  const cur = d.jobs.find((j) => j.id === job.value!.id)
  if (cur) job.value = cur
  if (job.value && pipeTerminal(job.value.status)) follow.value = false
}

function openJob(j: PipeJob) {
  if (job.value?.id === j.id) return
  job.value = j
  logText.value = ''
  logOffset.value = 0
  follow.value = !pipeTerminal(j.status)
  pullLog()
}

async function pullLog(reset = false) {
  if (!job.value) return
  if (reset) { logText.value = ''; logOffset.value = 0 }
  try {
    const res = await loadPipeTrace(props.projectId, job.value.id, logOffset.value)
    // 服务端 offset 回退 = job 重跑日志重置，整体替换
    if (res.offset < logOffset.value) logText.value = res.log
    else logText.value += res.log
    logOffset.value = res.offset
    if (follow.value && logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  } catch { /* 日志拉取失败静默，下轮重试 */ }
}
usePoll(() => { if (job.value && follow.value) pullLog() }, 2_000)

// ---- job 运行控制（manual 播放 / 终态重试 / 取消）：发应用 = 播放 app-release，
// 回滚 = 重放历史发布 pipeline 的 job（沿用其原变量），重新部署某版本 = 重试
// tag 流水线的 deploy job。动作后强制刷新详情（终态流水线轮询已停，需 force
// 重新拉起；流水线转 running 后轮询自然恢复）----
type JobAct = { label: string; action: 'run' | 'cancel'; tip: string }
function actOf(j: PipeJob): JobAct | null {
  switch (j.status) {
    case 'manual': return { label: '运行', action: 'run', tip: '按本流水线的变量运行（重放历史发布 pipeline 的此 job = 按原版本回滚上架）' }
    case 'running': case 'pending': case 'created': case 'preparing': case 'waiting_for_resource': case 'scheduled':
      return { label: '取消', action: 'cancel', tip: '取消运行/排队中的 job' }
    case 'failed': case 'canceled': case 'success':
      return { label: '重试', action: 'run', tip: '按原 commit 与变量重跑（deploy 重试 = 重新部署该版本）' }
    default: return null
  }
}
const actBusy = ref<Set<number>>(new Set())
async function doAct(j: PipeJob, a: JobAct) {
  actBusy.value.add(j.id)
  try {
    await runPipeJob(props.projectId, j.id, a.action)
    toast(`已${a.label}：${j.name}`, { type: 'success' })
    if (job.value?.id === j.id) { logText.value = ''; logOffset.value = 0 } // 重跑日志从头
    await load(true)
    if (job.value?.id === j.id && !pipeTerminal(job.value.status)) {
      follow.value = true
      pullLog()
    }
  } catch (e: any) {
    toast(e.message || `${a.label}失败`, { type: 'error' })
  } finally {
    actBusy.value.delete(j.id)
  }
}

// ---- 失败转修（inline 确认面板，复述修复对象 + 发版勾选后一键入队）----
const fixDlg = ref(false)
const fixRelease = ref(false)
const fixBusy = ref(false)
const fixErr = ref('')
const fixDone = ref<PipeFixResp | null>(null)

function askFix() {
  fixRelease.value = !!pl.value?.tag // tag 流水线默认随修发版（末位+1）
  fixErr.value = ''
  fixDone.value = null
  fixDlg.value = true
}

async function doFix() {
  fixBusy.value = true
  try {
    fixDone.value = await sendPipeFix({
      project_id: props.projectId, pipeline_id: props.pipelineId,
      confirm: true, release: fixRelease.value,
    })
  } catch (e: any) {
    fixErr.value = e.message || '转修失败'
  } finally {
    fixBusy.value = false
  }
}

// 转修成功后原地看队列：关弹窗切本 App「问题」页（steward.vue provide 的页签
// 切换；脱离 App 壳独立渲染时为 no-op）
const switchTab = inject<(key: string) => void>('stewardSwitchTab', () => {})

function openSteward() {
  emit('close')
  switchTab('problems')
}

watch(() => [props.projectId, props.pipelineId], () => {
  detail.value = null; err.value = ''; job.value = null
  logText.value = ''; logOffset.value = 0; stage.value = ''
})
</script>

<template>
  <OsModal class="dlg-lg" :title="`流水线 #${pipelineId}${detail ? ` · ${detail.project.path}` : ''}`"
    okText="关 闭" @ok="emit('close')" @cancel="emit('close')">
    <div class="pd">
      <div v-if="err" class="err">{{ err }}</div>
      <template v-if="detail && pl">
        <div class="meta">
          <StatusChip :status="pl.status" />
          <span class="refchip" :class="pl.tag ? 'tag' : 'br'">{{ pl.tag ? 'tag' : '分支' }}·{{ pl.ref }}</span>
          <span class="mono dim">{{ pl.sha.slice(0, 8) }}</span>
          <span class="dim">#{{ pl.id }} · {{ pl.source }} · {{ fmtTime(pl.updated_at, false) }}</span>
          <span class="dim">耗时 {{ fmtDur(pl.duration) }}</span>
          <a class="ext" :href="pl.web_url" target="_blank" rel="noopener">GitLab ↗</a>
        </div>
        <div v-if="pl.yaml_errors" class="yaml">.gitlab-ci.yml 错误：{{ pl.yaml_errors }}</div>

        <div v-if="detail.stages.length" class="stages">
          <button v-for="s in detail.stages" :key="s.name" class="stage" :class="{ on: stage === s.name }"
            @click="stage = stage === s.name ? '' : s.name">
            <StatusChip :status="s.status" />
            <span class="sname">{{ s.name }}</span>
          </button>
        </div>

        <table class="jtab">
          <thead>
            <tr><th>任务</th><th>状态</th><th>失败原因</th><th>耗时</th><th>Runner</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="j in shownJobs" :key="j.id" :class="{ sel: job?.id === j.id }" @click="openJob(j)">
              <td><span class="jname">{{ j.name }}</span><span v-if="j.allow_failure" class="af" title="此任务失败不阻断流水线">可失败</span></td>
              <td><StatusChip :status="j.status" /></td>
              <td class="dim" :class="{ bad: j.status === 'failed' }">{{ reasonOf(j.failure_reason) || '—' }}</td>
              <td class="mono dim">{{ fmtDur(j.duration) }}</td>
              <td class="dim runner">{{ j.runner || '—' }}</td>
              <td>
                <OsButton v-if="actOf(j)" size="sm" :variant="actOf(j)!.action === 'cancel' ? 'danger' : 'primary'"
                  :disabled="actBusy.has(j.id)" :title="actOf(j)!.tip" @click.stop="doAct(j, actOf(j)!)">
                  {{ actBusy.has(j.id) ? '…' : actOf(j)!.label }}
                </OsButton>
                <span v-else class="dim">—</span>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="!detail.jobs.length" class="dim empty">无 job 明细——流水线可能因 .gitlab-ci.yml 配置错误未产出任务</p>

        <div v-if="job" class="logwrap">
          <div class="loghead">
            <span class="mono">{{ job.stage }} / {{ job.name }}</span>
            <span class="dim">#{{ job.id }}</span>
            <label class="fol"><input type="checkbox" v-model="follow" :disabled="pipeTerminal(job.status)" /> 跟随</label>
            <OsButton size="sm" @click="pullLog(true)">从头刷新</OsButton>
            <a class="ext" :href="job.web_url" target="_blank" rel="noopener">GitLab ↗</a>
          </div>
          <div ref="logBox" class="log"><pre>{{ logText || '（暂无日志）' }}</pre></div>
        </div>

        <div v-if="pl.status === 'failed'" class="acts">
          <template v-if="!fixDlg">
            <OsButton variant="danger" size="sm" @click="askFix">交给 AI 修复</OsButton>
              <span class="dim flow">自动注册仓库 → AI 定位修复（编译错误 / .gitlab-ci.yml）→ 提交推送{{ pl.tag ? ' → 打 tag 发版 → CI 重跑' : ' → CI 重跑' }}</span>
          </template>
          <div v-else class="fixbox">
            <p class="dim">
              将创建修复任务：<b>{{ detail.project.path }}</b> · {{ pl.tag ? 'tag ' : '分支 ' }}{{ pl.ref }} ·
              失败 {{ failJob ? `${failJob.stage}/${failJob.name}` : '（无 job 明细，疑为 .gitlab-ci.yml 错误）' }}
              <template v-if="failJob">，失败日志尾部已附</template>
            </p>
            <label class="fol"><input type="checkbox" v-model="fixRelease" /> 修复后自动发版（tag 末位+1，由 CI 重跑验证）</label>
            <div class="fixrow">
              <OsButton size="sm" @click="fixDlg = false">取 消</OsButton>
              <OsButton size="sm" variant="primary" :disabled="fixBusy" @click="doFix">{{ fixBusy ? '提交中…' : '确认开修' }}</OsButton>
            </div>
            <p v-if="fixErr" class="err">{{ fixErr }}</p>
            <p v-if="fixDone" class="fixdone">
              {{ fixDone.created ? '已创建修复问题' : '已复用现有修复问题' }}（{{ fixStateOf(fixDone.state) }}）
              <OsButton size="sm" @click="openSteward">去问题页查看</OsButton>
            </p>
          </div>
        </div>
      </template>
      <p v-else-if="!err" class="dim">加载中…</p>
    </div>
  </OsModal>
</template>

<style scoped>
.pd { display: flex; flex-direction: column; gap: 10px; }
.meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; text-align: left; }
.dim { font-size: 12px; color: var(--os-text-tertiary); }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.ext { margin-left: auto; font-size: 12px; color: var(--os-accent, #4da3ff); text-decoration: none; }
.loghead .ext { margin-left: 0; }
.refchip { padding: 1px 8px; border-radius: 8px; font-size: 11px; background: var(--os-active); color: var(--os-text-secondary); }
.refchip.tag { color: var(--grf-violet); background: color-mix(in srgb, var(--grf-violet) 14%, transparent); }
.yaml { padding: 8px 10px; border-radius: 8px; font-size: 12.5px; text-align: left; color: var(--grf-bad); background: color-mix(in srgb, var(--grf-bad) 10%, transparent); word-break: break-all; }
.stages { display: flex; gap: 6px; flex-wrap: wrap; }
.stage { display: inline-flex; align-items: center; gap: 6px; padding: 4px 9px; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); cursor: pointer; }
.stage.on { border-color: var(--os-accent, #4da3ff); background: color-mix(in srgb, var(--os-accent, #4da3ff) 12%, transparent); }
.sname { font-size: 12px; }
.jtab { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.jtab th { text-align: left; color: var(--os-text-tertiary); font-weight: 500; padding: 4px 8px; border-bottom: 1px solid var(--os-border); }
.jtab td { padding: 5px 8px; border-bottom: 1px solid var(--os-border); white-space: nowrap; }
.jtab tbody tr { cursor: pointer; }
.jtab tbody tr:hover { background: var(--os-hover, rgba(128, 128, 128, 0.08)); }
.jtab tbody tr.sel { background: var(--os-active); }
.jname { font-weight: 500; }
.af { margin-left: 6px; padding: 1px 6px; border-radius: 6px; font-size: 10.5px; background: var(--os-active); color: var(--os-text-tertiary); }
.bad { color: var(--grf-bad); }
.runner { max-width: 180px; overflow: hidden; text-overflow: ellipsis; }
.empty { text-align: left; }
.logwrap { display: flex; flex-direction: column; gap: 6px; }
.loghead { display: flex; align-items: center; gap: 8px; }
.fol { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--os-text-secondary); cursor: pointer; }
.log { max-height: 42vh; overflow-y: auto; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); text-align: left; }
.log pre { margin: 0; padding: 10px 12px; font-size: 11.5px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
.acts { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding-top: 4px; border-top: 1px solid var(--os-border); }
.flow { text-align: left; }
.fixbox { display: flex; flex-direction: column; gap: 8px; align-items: flex-start; text-align: left; }
.fixrow { display: flex; gap: 8px; }
.fixdone { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--grf-ok); }
.err { padding: 8px 10px; border-radius: 8px; font-size: 12.5px; text-align: left; color: var(--grf-bad); background: color-mix(in srgb, var(--grf-bad) 10%, transparent); }
</style>
