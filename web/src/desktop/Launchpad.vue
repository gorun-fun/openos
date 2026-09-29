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
  if (!s) return apps.apps
  return apps.apps.filter(
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
      <input v-model="q" placeholder="搜索应用" autofocus @keydown.esc="emit('close')" />
    </div>
    <div class="grid">
      <button
        v-for="a in filtered" :key="a.id"
        class="lp-item" draggable="true"
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
  background: rgba(0, 0, 0, 0.28);
  backdrop-filter: blur(40px) saturate(1.4) brightness(1.06);
  -webkit-backdrop-filter: blur(40px) saturate(1.4) brightness(1.06);
  display: flex; flex-direction: column; align-items: center;
  padding-top: 8vh; animation: os-fade-in .16s ease;
}
.search-wrap { margin-bottom: 4vh; }
input {
  width: 280px; padding: 8px 16px; border-radius: 20px; border: none;
  background: rgba(255,255,255,.25); color: #fff; font-size: 14px; text-align: center;
  backdrop-filter: blur(10px);
}
input::placeholder { color: rgba(255,255,255,.6); }
.grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr));
  gap: 22px 12px; width: min(880px, 88vw); overflow-y: auto; padding-bottom: 20px;
}
.lp-item {
  display: flex; flex-direction: column; align-items: center; gap: 8px;
  background: transparent; border: none; cursor: pointer; padding: 8px 4px; border-radius: 14px;
  transition: transform .12s;
}
.lp-item:hover { transform: scale(1.06); }
.name { color: #fff; font-size: 12.5px; text-shadow: 0 1px 3px rgba(0,0,0,.4); }
.empty { color: rgba(255,255,255,.7); grid-column: 1 / -1; text-align: center; padding: 40px; }
.hint { position: absolute; bottom: 100px; color: rgba(255,255,255,.55); font-size: 12px; }
</style>
