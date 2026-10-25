<script setup lang="ts">
// 代码管家桌面小组件：待确认 / 修复中 / 已修复 / 失败 四个数（仅 admin 有数据，
// user 显示无权限占位）。10s 轮询；点标题直达 steward App（有待确认进问题页，
// 否则看修复记录）。
// widgetRegistry 协议：收 { widget, update } props；当前无自定义持久化字段。
import { computed, ref, watch } from 'vue'
import { useWindows } from '../../stores/windows'
import { grf, session, isAdmin } from './core'
import { usePoll } from './usePoll'

defineProps<{ widget: Record<string, any>; update: (patch: Record<string, any>) => void }>()

const windows = useWindows()
const ov = ref<any>(null)

const run = usePoll(async () => {
  if (!isAdmin()) return
  ov.value = await grf.get<any>('/api/steward/overview').catch(() => null)
}, 10_000)

// 会话晚就绪（isAdmin 由 false 转 true）→ 补一轮
watch(() => session.loaded, (v) => { if (v) run() })

const health = computed(() => {
  if (!ov.value) return 'unk'
  if (ov.value.running > 0) return 'busy'
  const s = ov.value.last_run?.state
  if (s === 'done') return 'ok'
  if (s === 'failed' || s === 'no_changes') return 'bad'
  return 'unk'
})

function open() {
  windows.open('steward', { tab: ov.value?.pending ? 'problems' : 'runs' })
}
const sideText = () => (ov.value?.running ? '修复中' : '')
</script>

<template>
  <div class="wgt">
    <button class="head" title="打开代码管家" @click="open">
      <span class="dot" :class="health"></span>
      <b>代码管家</b>
      <span v-if="ov?.pending" class="pend">{{ ov.pending }}</span>
      <span class="side">{{ sideText() }}</span>
    </button>

    <template v-if="isAdmin()">
      <div class="row"><span>待确认 / 排队</span><b>{{ ov ? ov.pending : '—' }}</b></div>
      <div class="row"><span>修复中</span><b>{{ ov ? ov.running : '—' }}</b></div>
      <div class="row"><span>已修复</span><b>{{ ov ? ov.fixed : '—' }}</b></div>
      <div class="row"><span>失败</span><b>{{ ov ? ov.failed : '—' }}</b></div>
    </template>
    <div v-else class="row"><span>需要管理员权限</span></div>
  </div>
</template>

<style scoped>
.wgt { width: 232px; padding-bottom: 10px; user-select: none; }

.head {
  display: flex; align-items: center; gap: 8px;
  width: 100%; padding: 16px 14px 8px;
  border: none; background: transparent; cursor: pointer; font: inherit; color: #f2f3f7;
}
.head b { font-size: 13px; letter-spacing: .5px; }
.head:hover b { opacity: .82; }
.pend {
  margin-left: auto; font-size: 10.5px; padding: 2px 8px; border-radius: 99px;
  background: rgba(255, 159, 10, .32); color: #ffb340; white-space: nowrap;
}
.pend + .side { margin-left: 0; }
.side {
  margin-left: auto; font-size: 10.5px; color: rgba(255, 255, 255, .68);
  font-variant-numeric: tabular-nums; white-space: nowrap;
}
.dot { width: 9px; height: 9px; border-radius: 50%; flex: none; }
.dot.ok { background: #30d158; box-shadow: 0 0 6px rgba(48, 209, 88, .6); }
.dot.bad { background: #ff453a; box-shadow: 0 0 6px rgba(255, 69, 58, .6); }
.dot.busy { background: #0a84ff; box-shadow: 0 0 6px rgba(10, 132, 255, .6); }
.dot.unk { background: rgba(255, 255, 255, .3); }

.row {
  display: flex; align-items: center; justify-content: space-between; gap: 8px;
  width: 100%; padding: 6px 14px; font: inherit; color: #f2f3f7;
}
.row span { font-size: 12px; color: rgba(255, 255, 255, .68); white-space: nowrap; }
.row b { font-size: 12.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
</style>
