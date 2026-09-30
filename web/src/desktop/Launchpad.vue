<script setup lang="ts">
import { computed, ref } from 'vue'
import { useApps } from '../stores/apps'
import { useWindows } from '../stores/windows'
import AppIcon from '../ui/AppIcon.vue'

const emit = defineEmits<{ close: [] }>()
const apps = useApps()
const windows = useWindows()
const q = ref('')

const filtered = computed(() => {
  const s = q.value.trim().toLowerCase()
  const list = apps.visible
  if (!s) return list
  return list.filter(
    (a) => a.name.toLowerCase().includes(s) || a.id.toLowerCase().includes(s) || a.category.toLowerCase().includes(s),
  )
})

function open(id: string) {
  windows.open(id)
  emit('close')
}

function onDragStart(e: DragEvent, appId: string) {
  // 拖出启动台 → 桌面层 drop 处理创建快捷方式
  e.dataTransfer?.setData('application/x-openos-app', appId)
  e.dataTransfer!.effectAllowed = 'copy'
}
</script>

<template>
  <div class="launchpad" @pointerdown.self="emit('close')">
    <div class="search-wrap">
      <svg viewBox="0 0 16 16" class="mg"><circle cx="6.8" cy="6.8" r="4.6" fill="none" stroke="currentColor" stroke-width="1.5" /><line x1="10.4" y1="10.4" x2="14.2" y2="14.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" /></svg>
      <input v-model="q" placeholder="搜索" autofocus @keydown.esc="emit('close')" />
    </div>
    <div class="grid">
      <button
        v-for="(a, i) in filtered" :key="a.id"
        class="lp-item" draggable="true"
        :style="{ '--i': i }"
        :title="`点击打开 · 拖拽到桌面创建快捷方式`"
        @click="open(a.id)"
        @dragstart="onDragStart($event, a.id)"
      >
        <AppIcon :app="a.id" :size="68" />
        <span class="name">{{ a.name }}</span>
      </button>
      <div v-if="!filtered.length" class="empty">没有匹配的应用</div>
    </div>
    <p class="hint">点击打开 · 拖拽到桌面创建快捷方式 · Esc 关闭</p>
  </div>
</template>

<style scoped>
.launchpad {
  position: fixed; inset: 0; z-index: 9600;
  background: rgba(0, 0, 0, 0.3);
  backdrop-filter: blur(46px) saturate(1.5) brightness(1.08);
  -webkit-backdrop-filter: blur(46px) saturate(1.5) brightness(1.08);
  display: flex; flex-direction: column; align-items: center;
  padding-top: 8vh; animation: os-fade-in .18s ease;
}
.search-wrap { margin-bottom: 4vh; animation: lp-item-in .3s var(--os-ease-out) both; position: relative; }
.mg { position: absolute; left: 12px; top: 50%; transform: translateY(-50%); width: 14px; height: 14px; color: rgba(255,255,255,.6); pointer-events: none; }
input {
  width: 280px; padding: 8px 16px 8px 34px; border-radius: 999px;
  border: 1px solid rgba(255,255,255,.28);
  background: rgba(255,255,255,.2); color: #fff; font-size: 14px;
  backdrop-filter: blur(10px); transition: background .15s, border-color .15s;
}
input:focus { background: rgba(255,255,255,.3); border-color: rgba(255,255,255,.5); }
input::placeholder { color: rgba(255,255,255,.6); }
.grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 22px 12px; width: min(880px, 88vw); overflow-y: auto; padding-bottom: 20px;
}
.lp-item {
  display: flex; flex-direction: column; align-items: center; gap: 9px;
  background: transparent; border: none; cursor: pointer; padding: 10px 4px; border-radius: 16px;
  transition: transform .16s var(--os-ease-spring), background .15s;
  /* 图标依次弹入（基于网格内序号的级联延迟） */
  animation: lp-item-in .34s var(--os-ease-out) both;
  animation-delay: calc(var(--i, 0) * 22ms);
}
@keyframes lp-item-in {
  from { opacity: 0; transform: translateY(14px) scale(.92); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.lp-item:hover { transform: scale(1.08); background: rgba(255,255,255,.08); }
.lp-item:active { transform: scale(1.02); }
.name { color: #fff; font-size: 12.5px; text-shadow: 0 1px 4px rgba(0,0,0,.45); }
.empty { color: rgba(255,255,255,.7); grid-column: 1 / -1; text-align: center; padding: 40px; }
.hint { position: absolute; bottom: 100px; color: rgba(255,255,255,.55); font-size: 12px; }
</style>
