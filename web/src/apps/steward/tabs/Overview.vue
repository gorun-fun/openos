<script setup lang="ts">
// 总览 — 默认落点：用户最关心的四件事一屏（10s 轮询）。发版健康（失败流水线
// 待处理）、问题队列（待确认/修复中）、修复动态（最近记录）、账号连接状态
//（未连接引导去「仓库」页）。卡片点击直达对应页签（stewardSwitchTab）。
import { computed, inject, ref } from 'vue'
import { grf } from '../core'
import { usePoll } from '../usePoll'
import { loadPipe, pipeProjects, pipeErr } from '../pipeline/data'

const switchTab = inject<(key: string) => void>('stewardSwitchTab', () => {})

const ov = ref<any>(null)
const problems = ref<any[]>([])
const runs = ref<any[]>([])
const conns = ref<any[] | null>([])

async function load() {
  const [o, p, r, c] = await Promise.all([
    grf.get<any>('/api/steward/overview').catch(() => null),
    grf.get<any[]>('/api/steward/problems').catch(() => []),
    grf.get<any[]>('/api/steward/runs').catch(() => []),
    grf.get<any[]>('/api/steward/connections').catch(() => null),
  ])
  ov.value = o
  problems.value = (Array.isArray(p) ? p : []).filter((x: any) => ['new', 'confirmed', 'running', 'failed'].includes(x.state)).slice(0, 4)
  runs.value = (Array.isArray(r) ? r : []).slice(0, 4)
  conns.value = Array.isArray(c) ? c : null // 非数组（空态整包/异常）一律视为未知
  loadPipe() // 发版健康共享流水线聚合（服务端 30s 缓存，多页签轮询无压力）
}
usePoll(load, 10_000)

const connList = computed(() => conns.value || [])

// 发版健康：最新版本流水线或默认分支流水线失败的项目（与版本页「失败」口径一致）
const failedProjects = computed(() =>
  pipeProjects.value.filter((p) => p.latest_tag?.pipeline?.status === 'failed' || p.branch_pipeline?.status === 'failed').slice(0, 4))

function go(key: string) { switchTab(key) }

const pMeta: Record<string, { label: string; cls: string }> = {
  new: { label: '待确认', cls: 'warn' }, confirmed: { label: '排队中', cls: 'info' },
  running: { label: '修复中', cls: 'info' }, failed: { label: '失败', cls: 'bad' },
  fixed: { label: '已修复', cls: 'ok' }, rejected: { label: '已驳回', cls: '' }, canceled: { label: '已取消', cls: '' },
}
const rMeta: Record<string, { label: string; cls: string }> = {
  prepare: { label: '准备', cls: 'info' }, fixing: { label: '修复中', cls: 'info' },
  committing: { label: '提交', cls: 'info' }, pushing: { label: '推送', cls: 'info' },
  releasing: { label: '发版', cls: 'info' }, done: { label: '完成', cls: 'ok' },
  no_changes: { label: '无改动', cls: 'warn' }, failed: { label: '失败', cls: 'bad' },
  canceled: { label: '已取消', cls: '' },
}
const platIcon = (p: string) => (p === 'gitlab' ? '🦊' : '🐙')
const hostOf = (c: any) => String(c.base_url || '').replace(/^https?:\/\//, '').replace(/\/$/, '')
</script>

<template>
  <div class="tabpage">
    <div class="grid">
      <!-- 发版健康 -->
      <section class="card" :class="{ bad: failedProjects.length }" @click="go('versions')">
        <h3>发版健康</h3>
        <template v-if="failedProjects.length">
          <p class="num bad">{{ failedProjects.length }} 个项目失败待处理</p>
          <ul class="list">
            <li v-for="p in failedProjects" :key="p.id">
              <span class="mono" :title="p.path">{{ p.path }}</span>
              <span class="dim">{{ p.latest_tag?.pipeline?.status === 'failed' ? `${p.latest_tag.name} 发版失败` : `默认分支失败` }}</span>
            </li>
          </ul>
        </template>
        <template v-else>
          <p class="num ok">{{ pipeErr ? '数据待同步' : '近况良好' }}</p>
          <p class="dim">{{ pipeErr ? pipeErr : `全部项目版本与默认分支流水线无失败（${pipeProjects.length} 项目）` }}</p>
        </template>
        <p class="go">去版本页查看 →</p>
      </section>

      <!-- 问题队列 -->
      <section class="card" :class="{ warn: ov?.pending }" @click="go('problems')">
        <h3>问题队列</h3>
        <p class="num">
          <span :class="ov?.pending ? 'warn' : ''">{{ ov ? ov.pending : '—' }}</span>
          <span class="dim unit">待确认 / 排队</span>
          <span class="sep" />
          <span :class="ov?.running ? 'info' : ''">{{ ov ? ov.running : '—' }}</span>
          <span class="dim unit">修复中</span>
        </p>
        <ul class="list">
          <li v-for="p in problems" :key="p.id">
            <span class="badge" :class="pMeta[p.state]?.cls">{{ pMeta[p.state]?.label || p.state }}</span>
            <span class="t" :title="p.title">{{ p.title }}</span>
          </li>
          <li v-if="!problems.length" class="dim">队列空闲——无待处理问题</li>
        </ul>
        <p class="go">去问题页处理 →</p>
      </section>

      <!-- 修复动态 -->
      <section class="card" @click="go('runs')">
        <h3>修复动态</h3>
        <ul class="list">
          <li v-for="r in runs" :key="r.id">
            <span class="badge" :class="rMeta[r.state]?.cls">{{ rMeta[r.state]?.label || r.state }}</span>
            <span class="t" :title="r.problem_title">{{ r.problem_title }}</span>
            <span class="dim">{{ r.repo_name }}</span>
          </li>
          <li v-if="!runs.length" class="dim">尚无修复记录（确认问题后自动开始）</li>
        </ul>
        <p v-if="ov?.last_run" class="dim last">
          最近一次：{{ rMeta[ov.last_run.state]?.label || ov.last_run.state }}
          <template v-if="ov.last_run.tag"> · {{ ov.last_run.tag }}</template>
          <template v-if="ov.last_run.commit"> · {{ String(ov.last_run.commit).slice(0, 7) }}</template>
        </p>
        <p class="go">去修复页跟进 →</p>
      </section>

      <!-- 账号连接 -->
      <section class="card" @click="go('repos')">
        <h3>账号连接</h3>
        <ul class="list">
          <li v-for="c in connList" :key="c.id">
            <span class="plat">{{ platIcon(c.platform) }}</span>
            <span class="mono">{{ c.login }}</span>
            <span class="dim">@{{ hostOf(c) }}</span>
            <span v-if="c.active" class="badge info">数据源</span>
          </li>
          <li v-if="!connList.length" class="dim">
            {{ conns === null ? '引擎未启用' : '尚未连接平台账号——连接后可浏览接管账号下全部仓库' }}
          </li>
        </ul>
        <p class="go">{{ connList.length ? '去仓库页管理 →' : '去连接账号 →' }}</p>
      </section>
    </div>
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 10px; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 12px; }
.card {
  display: flex; flex-direction: column; gap: 8px; padding: 14px 16px;
  border-radius: 12px; border: 1px solid var(--os-border); background: var(--os-input-bg);
  cursor: pointer; transition: border-color .15s;
}
.card:hover { border-color: var(--os-accent, #4da3ff); }
.card.bad { border-color: color-mix(in srgb, var(--grf-bad) 45%, transparent); }
.card h3 { margin: 0; font-size: 13px; font-weight: 600; color: var(--os-text-secondary); }
.num { margin: 0; font-size: 22px; font-weight: 600; font-variant-numeric: tabular-nums; }
.num.ok { color: var(--grf-ok); }
.num.bad { color: var(--grf-bad); }
.num .warn { color: #ffb340; }
.num .info { color: #6db2ff; }
.unit { margin-left: 6px; font-size: 11.5px; font-weight: 400; }
.sep { flex: 1; }
.num { display: flex; align-items: baseline; }
.list { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 6px; }
.list li { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 12.5px; }
.list .t, .list .mono { flex: 0 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.list li > .dim { margin-left: auto; flex: none; font-size: 11.5px; }
.dim { color: var(--os-text-tertiary); font-size: 12px; }
.last { margin: 0; }
.go { margin: auto 0 0; font-size: 11.5px; color: var(--os-accent, #4da3ff); }
.badge { flex: none; display: inline-block; padding: 1px 8px; border-radius: 8px; font-size: 11px; background: var(--os-active); }
.badge.ok { background: rgba(48, 209, 88, .16); color: var(--grf-ok); }
.badge.bad { background: rgba(255, 69, 58, .14); color: var(--grf-bad); }
.badge.warn { background: rgba(255, 159, 10, .16); color: #ffb340; }
.badge.info { background: rgba(10, 132, 255, .18); color: #6db2ff; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.plat { flex: none; }
</style>
