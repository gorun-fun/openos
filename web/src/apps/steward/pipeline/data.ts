// data.ts — pipeline App 两 tab 共享的数据源与状态呈现（同一次后端聚合，
// 服务端 30s 缓存；模块级单例如 @grf/core 的会话/客户端惯例，随 App chunk
// 打包一份）。类型与服务端 pipeline.go 的投影一一对应。
import { ref } from 'vue'
import { grf } from '../core'

export interface PipePipeline {
  id: number; sha: string; ref: string; status: string; source: string
  created_at: string; updated_at: string; web_url: string
}
export interface PipeTag {
  name: string; short_id: string; title: string; created_at: string
  pipeline: PipePipeline | null
}
export interface PipeProject {
  id: number; name: string; path: string; web_url: string
  default_branch: string; archived: boolean; empty_repo?: boolean
  last_activity_at: string; latest_tag: PipeTag | null
  tags: PipeTag[]; pipelines: PipePipeline[]
  branch_pipeline?: PipePipeline | null; error?: string
}

export const pipeProjects = ref<PipeProject[]>([])
export const pipeErr = ref('')
export const pipeLoading = ref(false)
export const pipeFetchedAt = ref('')

export async function loadPipe() {
  pipeLoading.value = true
  try {
    const data = await grf.get<{ fetched_at: string; projects: PipeProject[] }>('/api/steward/pipe/projects')
    pipeProjects.value = Array.isArray(data?.projects) ? data.projects : []
    pipeFetchedAt.value = data?.fetched_at || ''
    pipeErr.value = ''
  } catch (e: any) {
    // 失败保留上一轮数据（pipeProjects 不动），仅记错误供各 tab 顶部提示
    pipeErr.value = e.message || 'GitLab 数据加载失败'
  } finally {
    pipeLoading.value = false
  }
}

// ---- GitLab 流水线状态呈现（色语义走 shell-theme 的状态体系）----
const PIPE_STATUS: Record<string, { label: string; cls: string }> = {
  success: { label: '成功', cls: 'ok' },
  failed: { label: '失败', cls: 'bad' },
  running: { label: '运行中', cls: 'run' },
  pending: { label: '等待', cls: 'wait' },
  preparing: { label: '准备中', cls: 'wait' },
  created: { label: '已创建', cls: 'wait' },
  waiting_for_resource: { label: '排队', cls: 'wait' },
  scheduled: { label: '定时', cls: 'wait' },
  manual: { label: '手动', cls: 'man' },
  canceled: { label: '已取消', cls: 'off' },
  skipped: { label: '已跳过', cls: 'off' },
}

export function statusOf(s?: string | null): { label: string; cls: string } | null {
  return s ? PIPE_STATUS[s] || { label: s, cls: 'off' } : null
}

/** tag 在 GitLab 的页面地址（结构类型：兼容 OsTable 行槽的宽松 row） */
export function tagURL(p: { web_url: string }, t: { name: string }): string {
  return `${p.web_url}/-/tags/${encodeURIComponent(t.name)}`
}

// ---- 流水线详情 / job 日志 / 失败转修（v0.0.19；服务端 pipeline.go 三端点）----

export interface PipeJob {
  id: number; name: string; stage: string; status: string
  failure_reason?: string; allow_failure: boolean
  duration?: number; queued_duration?: number
  created_at?: string; started_at?: string; finished_at?: string
  web_url: string; runner?: string
}
export interface PipeStage { name: string; status: string }
export interface PipeProjRef {
  id: number; name: string; path: string; web_url: string
  default_branch: string; git_url?: string
}
export interface PipeDetail {
  project: PipeProjRef
  pipeline: PipePipeline & {
    tag?: boolean; duration?: number; queued_duration?: number
    started_at?: string; finished_at?: string; yaml_errors?: string
  }
  stages: PipeStage[]; jobs: PipeJob[]
}
export interface PipeTrace { log: string; offset: number; size: number }
export interface PipeFixResp {
  problem_id: string; repo_id: string
  state: string; release: boolean; created: boolean
}

/** 未到终态的流水线/job 状态（轮询与跟随的条件） */
export const PIPE_ACTIVE = ['running', 'pending', 'preparing', 'created', 'waiting_for_resource', 'scheduled']
export function pipeTerminal(s?: string | null): boolean {
  return !!s && !PIPE_ACTIVE.includes(s)
}

export function loadPipeDetail(projectID: number, pipelineID: number): Promise<PipeDetail> {
  return grf.get(`/api/steward/pipe/pipelines/${projectID}/${pipelineID}`)
}
export function loadPipeTrace(projectID: number, jobID: number, offset: number): Promise<PipeTrace> {
  return grf.get(`/api/steward/pipe/jobs/${projectID}/${jobID}/trace?offset=${offset}`)
}
/** job 运行控制：run = manual 播放（沿用流水线变量，重放历史发布 pipeline 即回滚）/ 终态重试；cancel = 取消 */
export function runPipeJob(projectID: number, jobID: number, action: 'run' | 'cancel'): Promise<{ job: PipeJob }> {
  return grf.post(`/api/steward/pipe/jobs/${projectID}/${jobID}/${action}`)
}
export function sendPipeFix(body: { project_id: number; pipeline_id: number; confirm: boolean; release: boolean }): Promise<PipeFixResp> {
  return grf.post('/api/steward/pipe/fix', body)
}

/** job failure_reason（GitLab 枚举）→ 中文 */
export function reasonOf(r?: string): string {
  if (!r) return ''
  const m: Record<string, string> = {
    unknown_failure: '未知失败',
    script_failure: '脚本失败（编译/命令报错）',
    api_failure: 'GitLab API 故障',
    system_failure: 'Runner 系统故障',
    runner_system_failure: 'Runner 系统故障',
    build_failure: '构建失败',
    stuck_or_timeout_failure: '卡住/超时',
    scheduler_failure: '调度失败',
    job_execution_timeout: '执行超时',
    runner_unsupported: 'Runner 不支持',
    data_integrity_failure: '数据完整性故障',
    unmet_prerequisites: '前置条件未满足',
    archived_failure: '已归档',
  }
  return m[r] || r
}

/** steward 问题状态 → 中文（转修结果回显用） */
export function fixStateOf(s: string): string {
  const m: Record<string, string> = {
    new: '待确认', confirmed: '已入队', running: '修复中',
    fixed: '已修复', failed: '修复失败', rejected: '已驳回', canceled: '已取消',
  }
  return m[s] || s
}

/** CI 耗时（秒）→ 分秒；无值 → — */
export function fmtDur(sec?: number): string {
  if (!sec || sec <= 0) return '—'
  if (sec < 60) return `${Math.round(sec)}s`
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  return s ? `${m}分${s}秒` : `${m}分`
}
