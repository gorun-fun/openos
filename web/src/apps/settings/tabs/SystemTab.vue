<script setup lang="ts">
// 系统信息分区（admin）：运行统计（版本/用户/应用/文件/存储）+ 审计日志。
// 原由 watch(tab) 首次进入时加载，随分区挂载迁移（每次进入刷新）。
import { onMounted, ref } from 'vue'
import { api } from '../../../api/client'
import { t } from '../../../i18n'
import { OsPanel } from '../../../ui'

// GET /api/stats（服务端 SystemStats 的最小投影）
interface SysStats {
  version: string
  userCount: number
  appCount: number
  fileCount: number
  storageUsed: number
  quotaPerUser: number
  startedAt: string
}
// GET /api/audit 行（服务端 AuditEntry；ip 字段本页不展示）
interface AuditRow {
  id: number
  username: string
  action: string
  detail: string
  createdAt: string
}

const stats = ref<SysStats | null>(null)
const audit = ref<AuditRow[]>([])
async function loadSystem() {
  try {
    const [s, a] = await Promise.all([
      api.get<SysStats>('/api/stats'),
      api.get<AuditRow[]>('/api/audit'),
    ])
    stats.value = s
    audit.value = a
  } catch { /* ignore */ }
}

onMounted(() => { void loadSystem() })
</script>

<template>
  <h2 class="sec">{{ t('settings.sys.status') }}</h2>
  <OsPanel>
    <div class="stats-grid" v-if="stats">
      <div class="stat"><b>{{ stats.version }}</b><span>{{ t('settings.sys.version') }}</span></div>
      <div class="stat"><b>{{ stats.userCount }}</b><span>{{ t('settings.sys.users') }}</span></div>
      <div class="stat"><b>{{ stats.appCount }}</b><span>{{ t('settings.sys.apps') }}</span></div>
      <div class="stat"><b>{{ stats.fileCount }}</b><span>{{ t('settings.sys.files') }}</span></div>
      <div class="stat"><b>{{ (stats.storageUsed / 1048576).toFixed(1) }} MB</b><span>{{ t('settings.sys.storage') }}</span></div>
      <div class="stat"><b>{{ (stats.quotaPerUser / 1048576) }} MB</b><span>{{ t('settings.sys.quota') }}</span></div>
    </div>
    <p class="hint">{{ t('settings.sys.startedAt', { time: stats?.startedAt || '—' }) }}</p>
  </OsPanel>
  <h2 class="sec">{{ t('settings.sys.audit') }}</h2>
  <OsPanel>
    <div class="audit-list">
      <div v-for="a in audit" :key="a.id" class="audit-row">
        <span class="au-time">{{ a.createdAt }}</span>
        <span class="au-user">{{ a.username }}</span>
        <span class="au-act">{{ a.action }}</span>
        <span class="au-detail">{{ a.detail }}</span>
      </div>
      <div v-if="!audit.length" class="hint">{{ t('settings.sys.noAudit') }}</div>
    </div>
  </OsPanel>
</template>

<style scoped>
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.sec:not(:first-child) { margin-top: 18px; }
.hint { font-size: 12px; color: var(--os-text-secondary); margin-top: 10px; }
.stats-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(130px, 1fr)); gap: 10px; }
.stat { background: var(--os-sidebar); border-radius: 10px; padding: 12px; text-align: center; }
.stat b { display: block; font-size: 20px; }
.stat span { font-size: 11.5px; color: var(--os-text-secondary); }
.audit-list { max-height: 300px; overflow-y: auto; font-size: 12px; }
.audit-row { display: flex; gap: 10px; padding: 5px 4px; border-bottom: 1px solid var(--os-border); align-items: baseline; }
.audit-row:last-child { border-bottom: none; }
.au-time { color: var(--os-text-secondary); flex: none; width: 130px; font-variant-numeric: tabular-nums; }
.au-user { flex: none; width: 80px; font-weight: 600; }
.au-act { flex: none; width: 110px; color: var(--os-accent); font-family: var(--os-mono); font-size: 11px; }
.au-detail { color: var(--os-text-secondary); word-break: break-all; }
</style>
