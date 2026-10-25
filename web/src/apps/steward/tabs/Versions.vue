<script setup lang="ts">
// 版本 — 发版健康工作面，段选两视图（v0.0.32 起合并原「版本/项目」两页签）：
// 「版本视图」跨项目摊平 tag × CI 成败；「项目视图」按项目看最新版本 CI、默认
// 分支流水线与最近流水线。两视图共享 pipeline/data.ts 数据源（服务端 30s
// 缓存），任何流水线交互都在 PipeDetail 原地展开（阶段 + job + 日志 + 失败
// 转修），不再跳 GitLab。
import { computed, ref } from 'vue'
import VersionRows from '../pipeline/tabs/Versions.vue'
import ProjectRows from '../pipeline/tabs/Projects.vue'

const view = ref<'versions' | 'projects'>('versions')
const comps = { versions: VersionRows, projects: ProjectRows }
const current = computed(() => comps[view.value])
</script>

<template>
  <div class="tabpage">
    <div class="segbar">
      <button class="seg" :class="{ on: view === 'versions' }" @click="view = 'versions'">按版本</button>
      <button class="seg" :class="{ on: view === 'projects' }" @click="view = 'projects'">按项目</button>
      <span class="dim segtip">{{ view === 'versions' ? '全部项目的版本（tag）× CI 成败，跨项目摊平' : '每项目一行：最新版本 CI、默认分支流水线与最近动态' }}</span>
    </div>
    <KeepAlive>
      <component :is="current" />
    </KeepAlive>
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 10px; }
.segbar { display: flex; align-items: center; gap: 8px; }
.seg {
  padding: 5px 14px; border-radius: 8px; font-size: 12.5px; cursor: pointer;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text-secondary);
}
.seg.on { border-color: var(--os-accent, #4da3ff); background: color-mix(in srgb, var(--os-accent, #4da3ff) 14%, transparent); color: var(--os-text); }
.segtip { font-size: 11.5px; }
.dim { color: var(--os-text-tertiary); }
</style>
