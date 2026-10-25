// capabilities.ts — 代码管家对 AI 的能力清单（自 grf-os 宿主上游化移植）：
// queries 只读取数 + 低风险单点动作（【动作】标记，执行前向用户复述确认）+
// views 页签入口。数据面即 /api/steward/*（admin 专属，服务端仍校验角色）；
// 脱敏与精简原则同宿主惯例（投影白名单、长文本截断）。
import type { AppCapabilities, CapDef, CapViewDef } from '../../desktop/capabilities'
import { capS, capN, capB, capObj } from '../../capkit'
import { grf, isAdmin } from './core'
import { STEWARD_TABS } from './tabs'

/** 截断预览：空值归一为 undefined，长文本只留前 n 字。 */
const cut = (v: any, n = 200) =>
  v == null || v === '' ? undefined : String(v).slice(0, n)

/** admin 专属能力前置校验（服务端仍会 403，此处给可读错误）。 */
const requireAdmin = () => {
  if (!isAdmin()) throw new Error('此能力仅 admin 可用（代码管家为管理员工作面）')
}

/** 页签 → views（os_app_ui 可视化入口：打开/聚焦窗口并路由到页签）。 */
const stewardViews = (): CapViewDef[] =>
  Object.entries(STEWARD_TABS).map(([key, label]) => ({
    name: key,
    title: `代码管家 · ${label}`,
    description: `打开（或聚焦）代码管家的「${label}」页签。`,
    props: { tab: key },
  }))

// ---- steward（代码管家 + 流水线发版健康） ----

// 流水线状态简报（/pipe 聚合行内嵌的 pipeline 对象 → 精简投影）
const pipeBrief = (p: any) =>
  p ? { id: p.id, ref: p.ref, status: p.status, source: p.source, updated_at: p.updated_at } : null

// steward 问题状态 → 中文（转修结果回显用）
const fixStateZh = (s: string): string => {
  const m: Record<string, string> = {
    new: '待确认', confirmed: '已入队', running: '修复中',
    fixed: '已修复', failed: '修复失败', rejected: '已驳回', canceled: '已取消',
  }
  return m[s] || s
}

export function stewardCaps(): AppCapabilities {
  return {
    appId: 'steward',
    label: '代码管家（steward：流水线发版健康 + 问题自动修复）',
  queries: [
    {
      name: 'overview',
      title: '修复总览',
      description:
        'steward（代码管家）总览：待处理（待确认+排队）/修复中/已修复/失败计数与最近一次修复记录（模型/提交/发版标签）。问「steward 现在忙不忙」「最近修了什么」用本查询。',
      result: '{pending, running, fixed, failed, last_run?}',
      run: () => grf.get<any>('/api/steward/overview'),
    },
    {
      name: 'problems',
      title: '问题队列',
      description:
        'steward 问题队列明细，可按条件过滤。state 取值：new 待确认 / confirmed 排队中 / running 修复中 / fixed 已修复 / failed 失败 / rejected 已驳回 / canceled 已取消。问「有几个待确认问题」「某某仓库的修复进展」用本查询。',
      params: capObj({
          state: capS('按状态过滤（可选）'),
          repo: capS('按仓库名过滤（可选）'),
          q: capS('标题关键词（可选）'),
        }),
      result: '{total, problems[]}',
      run: async (args: any) => {
        const qs = new URLSearchParams()
        if (args?.state) qs.set('state', String(args.state))
        if (args?.repo) qs.set('repo', String(args.repo))
        if (args?.q) qs.set('q', String(args.q))
        const s = qs.toString()
        const list: any[] = (await grf.get<any[]>(`/api/steward/problems${s ? '?' + s : ''}`)) || []
        return {
          total: list.length,
          problems: list.map((p) => ({
            id: p.id,
            title: p.title,
            repo: p.repo_name,
            source: p.source,
            state: p.state,
            error: p.error || undefined,
            created_at: p.created_at,
            body_preview: String(p.body || '').slice(0, 200),
          })),
        }
      },
    },
    {
      name: 'repos',
      title: '接管仓库',
      description:
        'steward 接管的 git 仓库清单：名称、clone 地址、推送分支、模式（manual 新问题待确认 / auto 直接入队）、issue 同步配置（GitHub owner/repo；GitLab 仓接管即同步，项目路径取 clone 地址）与最近同步时间。问「steward 管着哪些仓库」「某仓库是不是自动模式」用本查询。',
      result: '{total, repos[]}',
      run: async () => {
        const list: any[] = (await grf.get<any[]>('/api/steward/repos')) || []
        return {
          total: list.length,
          repos: list.map((r) => ({
            id: r.id,
            name: r.name,
            clone_url: r.clone_url,
            branch: r.branch || r.def_branch,
            mode: r.mode,
            platform: r.platform || null,
            github: r.github_owner ? `${r.github_owner}/${r.github_repo}` : null,
            issue_labels: r.issue_labels || [],
            git_token_set: !!r.git_token_set,
            auto_release: !!r.auto_release,
            last_issue_sync: r.last_issue_sync || null,
          })),
        }
      },
    },
    {
      name: 'runs',
      title: '修复记录',
      description:
        'steward 修复记录：每次执行的状态（prepare 准备 / fixing 修复中 / committing 提交 / pushing 推送 / releasing 发版 / done 完成 / no_changes 无改动 / failed 失败 / canceled 已取消）、模型、commit 与发版 tag、改动统计与执行日志。不带 id 返回记录列表（可按仓库过滤）；带 id 返回单次详情与日志尾部。问「最近修复跑得怎么样」「某次修复为什么失败」用本查询。',
      params: capObj({
          id: capS('修复记录 id（可选；提供则返回该次详情+日志尾部）'),
          repo: capS('按仓库名过滤（可选，列表模式）'),
        }),
      result: '列表 {total, runs[]} 或详情 {run, log_tail}',
      run: async (args: any) => {
        requireAdmin()
        if (args?.id) {
          const res = await grf.get<any>(`/api/steward/runs/${encodeURIComponent(String(args.id))}?log=0`)
          const run = res?.run || {}
          const log = String(res?.log || '')
          return {
            run: {
              id: run.id, problem_title: run.problem_title, repo_name: run.repo_name,
              state: run.state, model: run.model,
              commit: run.commit || undefined, tag: run.tag || undefined,
              files: run.files, insertions: run.ins, deletions: run.dels,
              error: cut(run.error, 300),
              started_at: run.started_at, ended_at: run.ended_at,
            },
            log_tail: log.length > 3000 ? '…' + log.slice(-3000) : log || undefined,
          }
        }
        const qs = args?.repo ? `?repo=${encodeURIComponent(String(args.repo))}` : ''
        const list: any[] = (await grf.get<any[]>(`/api/steward/runs${qs}`)) || []
        return {
          total: list.length,
          runs: list.map((r) => ({
            id: r.id, problem_title: r.problem_title, repo_name: r.repo_name,
            state: r.state, model: r.model,
            commit: r.commit || undefined, tag: r.tag || undefined,
            files: r.files, error: cut(r.error, 200),
            started_at: r.started_at, ended_at: r.ended_at,
          })),
        }
      },
    },
    // ---- 流水线发版健康（GitLab 版本 × CI；数据源与 steward「版本/项目」页同）----
    {
      name: 'versions',
      title: '版本×CI 发版健康',
      description:
        'GitLab（git.gorun.fun）全部项目的发版健康：每项目最新版本（tag）对应 CI 流水线成败、默认分支流水线状态与近期流水线，数据同代码管家 App「版本」页（服务端 30s 缓存）。问「今天发版都成功了吗」「哪个项目的 CI 挂了」「失败的项目列一下」用本查询；要看阶段/job 细节用 steward.pipeline_detail。',
      params: capObj({
          failed_only: capB('只看失败（最新版本流水线或默认分支流水线失败的项目，可选）'),
          project: capS('按项目名/路径过滤（可选）'),
        }),
      result: '{fetched_at, total, failed, projects[{id,name,latest_tag,branch_pipeline,recent[]}]}',
      run: async (args: any) => {
        requireAdmin()
        const data = await grf.get<any>('/api/steward/pipe/projects')
        const name = String(args?.project || '').trim().toLowerCase()
        let projects = (data?.projects || []).map((p: any) => ({
          id: p.id,
          name: p.name,
          path: p.path,
          default_branch: p.default_branch,
          latest_tag: p.latest_tag
            ? { name: p.latest_tag.name, created_at: p.latest_tag.created_at, pipeline: pipeBrief(p.latest_tag.pipeline) }
            : null,
          branch_pipeline: pipeBrief(p.branch_pipeline),
          recent: (p.pipelines || []).slice(0, 3).map(pipeBrief),
          error: p.error ? cut(p.error, 120) : undefined,
        }))
        if (name) {
          projects = projects.filter((p: any) =>
            String(p.name || '').toLowerCase().includes(name) || String(p.path || '').toLowerCase().includes(name))
        }
        const isFailed = (p: any) =>
          p.latest_tag?.pipeline?.status === 'failed' || p.branch_pipeline?.status === 'failed'
        if (args?.failed_only) projects = projects.filter(isFailed)
        return {
          fetched_at: data?.fetched_at || null,
          total: projects.length,
          failed: projects.filter(isFailed).length,
          projects,
        }
      },
    },
    {
      name: 'pipeline_detail',
      title: '流水线详情',
      description:
        '一条 GitLab 流水线的详情：阶段聚合、job 清单（状态/失败原因/耗时）、tag 标记、yaml_errors。项目与流水线 id 从 steward.versions 获取。问「这条流水线挂在哪个阶段」「哪个 job 失败了、什么原因」用本查询；失败 job 的日志用 steward.job_trace。',
      params: capObj({
          project_id: capN('GitLab 项目 id（steward.versions 的 projects[].id）'),
          pipeline_id: capN('流水线 id（pipeline.id）'),
        }, ['project_id', 'pipeline_id']),
      result: '{project, pipeline, stages[], jobs[]}',
      run: async (args: any) => {
        requireAdmin()
        const pid = Number(args?.project_id), pl = Number(args?.pipeline_id)
        if (!pid || !pl) throw new Error('缺少 project_id / pipeline_id（从 steward.versions 获取）')
        const d = await grf.get<any>(`/api/steward/pipe/pipelines/${pid}/${pl}`)
        return {
          project: d?.project ? { id: d.project.id, name: d.project.name, path: d.project.path, web_url: d.project.web_url } : null,
          pipeline: d?.pipeline ? {
            id: d.pipeline.id, ref: d.pipeline.ref, status: d.pipeline.status, source: d.pipeline.source,
            tag: !!d.pipeline.tag, duration_s: d.pipeline.duration ?? null,
            created_at: d.pipeline.created_at, updated_at: d.pipeline.updated_at,
            yaml_errors: cut(d.pipeline.yaml_errors, 400),
          } : null,
          stages: d?.stages || [],
          jobs: (d?.jobs || []).map((j: any) => ({
            id: j.id, name: j.name, stage: j.stage, status: j.status,
            failure_reason: j.failure_reason || undefined,
            duration_s: j.duration ?? null, allow_failure: !!j.allow_failure,
          })),
        }
      },
    },
    {
      name: 'job_trace',
      title: 'job 日志',
      description:
        '一条 GitLab CI job 的执行日志（服务端已剥 ANSI 色码），默认返回尾部 2000 字（可调 200–8000）。job id 从 steward.pipeline_detail 获取。问「这个 job 为什么失败」「把失败日志给我看」用本查询。',
      params: capObj({
          project_id: capN('GitLab 项目 id'),
          job_id: capN('job id（steward.pipeline_detail 的 jobs[].id）'),
          tail: capN('返回尾部字数（可选，默认 2000，最大 8000）'),
        }, ['project_id', 'job_id']),
      result: '{job_id, size, tail}',
      run: async (args: any) => {
        requireAdmin()
        const pid = Number(args?.project_id), jid = Number(args?.job_id)
        if (!pid || !jid) throw new Error('缺少 project_id / job_id')
        const t = await grf.get<any>(`/api/steward/pipe/jobs/${pid}/${jid}/trace?offset=0`)
        const log = String(t?.log || '')
        const tail = Math.min(Math.max(Number(args?.tail) || 2000, 200), 8000)
        return {
          job_id: jid,
          size: t?.size ?? log.length,
          tail: log.length > tail ? '…' + log.slice(-tail) : log || '(无日志)',
        }
      },
    },
    {
      name: 'conversions',
      title: 'AI 转制任务',
      description:
        '代码管家「应用」页的 AI 转制任务清单：把公开项目转制为 openos 应用（状态 queued 排队 / cloning 克隆建项目 / converting AI 转制中 / building CI 构建 / publishing 发布货架 / done 已上架 / failed 失败 / canceled 已取消）、源仓库、目标项目与已发布版本。问「转制进行到哪一步了」「哪些应用转制失败」用本查询。',
      result: '{total, conversions[]}',
      run: async () => {
        requireAdmin()
        const list: any[] = (await grf.get<any[]>('/api/steward/conversions')) || []
        return {
          total: list.length,
          conversions: list.map((c) => ({
            id: c.id, app_id: c.app_id, source_url: c.source_url, state: c.state,
            version: c.version || undefined, error: cut(c.error, 200),
            started_at: c.started_at, ended_at: c.ended_at || undefined,
          })),
        }
      },
    },
    {
      name: 'apps',
      title: '货架应用清单',
      description:
        '应用货架（catalog）清单：宿主预装应用与 AI 转制上架的应用（名称/分类/版本/形态）。转制产物经 openos 应用商店安装到桌面。问「货架上有哪些应用」「某个应用发布到什么版本」用本查询。',
      result: '{market_configured, total, apps[]}',
      run: async () => {
        requireAdmin()
        const r = await grf.get<any>('/api/steward/apps')
        const apps: any[] = r?.apps || []
        return {
          market_configured: !!r?.market_configured,
          total: apps.length,
          apps: apps.map((a) => ({
            id: a.id, name: a.name, category: a.category || undefined,
            version: a.version, kind: a.type === 'host' ? '宿主预装' : '商店可装',
            description: cut(a.description, 120),
          })),
        }
      },
    },
  ],
  actions: [
    {
      name: 'manage',
      title: '【动作】问题与修复管理',
      description:
        '【动作】steward 问题与修复记录的管理动作：kind=problem（问题）支持 confirm 确认入队 / reject 驳回 / cancel 取消；kind=run（修复记录）支持 cancel 取消执行 / retry 重试。执行前必须向用户复述目标（问题标题或记录 id）与动作并获确认。手动录入新问题、改仓库配置等请用 os_app_ui 打开代码管家 App 对应页面。',
      params: capObj({
          kind: capS('problem 或 run'),
          id: capS('问题 id 或修复记录 id'),
          action: capS('problem: confirm/reject/cancel；run: cancel/retry'),
        }, ['kind', 'id', 'action']),
      result: '{done, kind, id, action}',
      run: async (args: any) => {
        requireAdmin()
        const kind = String(args?.kind || '')
        const id = String(args?.id || '').trim()
        const action = String(args?.action || '')
        if (!id) throw new Error('缺少 id 参数')
        if (kind === 'problem') {
          if (!['confirm', 'reject', 'cancel'].includes(action)) {
            throw new Error('problem 的 action 须为 confirm / reject / cancel')
          }
          await grf.post<any>(`/api/steward/problems/${encodeURIComponent(id)}/${action}`)
          return { done: true, kind, id, action }
        }
        if (kind === 'run') {
          if (!['cancel', 'retry'].includes(action)) throw new Error('run 的 action 须为 cancel / retry')
          await grf.post<any>(`/api/steward/runs/${encodeURIComponent(id)}/${action}`)
          return { done: true, kind, id, action }
        }
        throw new Error('kind 须为 problem 或 run')
      },
    },
    {
      name: 'job_control',
      title: '【动作】流水线 job 运行控制',
      description:
        '【动作】控制一条 GitLab CI job：run = manual job 播放（沿用流水线既有变量——重放历史发布 pipeline 的 app-release 即按原版本重新上架=回滚）/ 终态 job（success/failed/canceled）重试（按原 commit 与变量重跑，deploy 重试即重新部署该版本）/ cancel = 取消运行或排队中的 job；运行/排队中的 job 不能 run。job id 从 steward.pipeline_detail 获取。执行前必须向用户复述：哪个项目、哪个 job、执行什么动作。',
      params: capObj({
          project_id: capN('GitLab 项目 id'),
          job_id: capN('job id（steward.pipeline_detail 的 jobs[].id）'),
          action: capS('run（播放/重试）或 cancel（取消）'),
        }, ['project_id', 'job_id', 'action']),
      result: '{done, job{最新状态}}',
      run: async (args: any) => {
        requireAdmin()
        const pid = Number(args?.project_id), jid = Number(args?.job_id)
        const action = String(args?.action || '')
        if (!pid || !jid) throw new Error('缺少 project_id / job_id')
        if (action !== 'run' && action !== 'cancel') throw new Error('action 须为 run 或 cancel')
        const r = await grf.post<any>(`/api/steward/pipe/jobs/${pid}/${jid}/${action}`)
        const j = r?.job || {}
        return {
          done: true,
          job: { id: j.id, name: j.name, stage: j.stage, status: j.status, failure_reason: j.failure_reason || undefined },
        }
      },
    },
    {
      name: 'fix_pipeline',
      title: '【动作】流水线失败转修',
      description:
        '【动作】把一条失败的 GitLab 流水线交给代码管家（steward）自动修复（平台核心工作流：自动注册/刷新仓库 → 组装失败上下文（首个硬失败 job + 日志尾部 + yaml_errors）→ 幂等建问题 → AI 在 Goder 会话里修复并直推修复分支）。两步语义：confirm=false（或缺省）只建/复用问题（待确认态，返回 problem_id 供用户过目）；用户同意后以 confirm:true 再次调用即确认入队开始修复。release=true 表示修复成功后自动发版（tag 末位+1；tag 流水线默认应发）。执行前必须向用户复述：哪个项目哪条流水线、是否发版。',
      params: capObj({
          project_id: capN('GitLab 项目 id（steward.versions 获取）'),
          pipeline_id: capN('失败的流水线 id'),
          release: capB('修复成功后自动发版（可选，默认否）'),
          confirm: capB('确认入队开始修复（可选，默认否=仅建问题）'),
        }, ['project_id', 'pipeline_id']),
      result: '{problem_id, repo_id, state, state_label, release, created, hint}',
      run: async (args: any) => {
        requireAdmin()
        const pid = Number(args?.project_id), pl = Number(args?.pipeline_id)
        if (!pid || !pl) throw new Error('缺少 project_id / pipeline_id')
        const r = await grf.post<any>('/api/steward/pipe/fix', {
          project_id: pid, pipeline_id: pl,
          confirm: !!args?.confirm, release: !!args?.release,
        })
        return {
          problem_id: r?.problem_id,
          repo_id: r?.repo_id,
          state: r?.state,
          state_label: fixStateZh(String(r?.state || '')),
          release: !!r?.release,
          created: !!r?.created,
          hint: args?.confirm
            ? '已确认入队，可在 steward「问题/修复记录」页或 Goder「代码管家」分组会话里跟进'
            : '已创建/复用问题（待确认），用户同意后以 confirm:true 再次调用开始修复',
        }
      },
    },
    {
      name: 'convert_app',
      title: '【动作】公开项目 AI 转制上架',
      description:
        '【动作】把一个公开项目转制为 openos 应用并发布到货架（实验特色功能：克隆源仓库 → AI 会话包装为 Vue 应用（引擎脚手架 vite 单文件工程）→ GitLab CI 构建 → 产物写货架 catalog，用户经应用商店安装）。前置：活动 GitLab 连接与货架目录已配置。执行前必须向用户复述：转制哪个仓库。任务异步执行，进度用 steward.conversions 查询。',
      params: capObj({
          source_url: capS('公开项目仓库地址（https://…，或源项目在 GitLab/GitHub 的克隆地址）'),
        }, ['source_url']),
      result: '{queued, id, app_id, state, hint}',
      run: async (args: any) => {
        requireAdmin()
        const u = String(args?.source_url || '').trim()
        if (!u) throw new Error('缺少 source_url 参数')
        const cv = await grf.post<any>('/api/steward/conversions', { source_url: u })
        return {
          queued: true, id: cv?.id, app_id: cv?.app_id, state: cv?.state,
          hint: '已入队（克隆 → AI 转制 → CI 构建 → 货架发布），进度用 steward.conversions 查询',
        }
      },
    },
  ],
    views: stewardViews(),
  }
}
