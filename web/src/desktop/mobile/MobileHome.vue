<script setup lang="ts">
// 移动形态桌面（Home）：单层滚动应用网格（Android launcher 形态），
// 顶部状态行 = 时钟（点击开通知中心）+ 搜索入口。无 Dock/菜单栏/快捷方式/小组件，
// 壁纸更换与外观等入口收进系统设置。仅在手机形态渲染（见 Desktop.vue）。
import { computed, onBeforeUnmount, onMounted, ref, type Component } from 'vue'
import { useApps } from '../../stores/apps'
import { useDesktop } from '../../stores/desktop'
import { useWindows } from '../../stores/windows'
import { useNotifications } from '../../stores/notifications'
import AppIcon from '../../ui/AppIcon.vue'
import { shell } from '../../shell'

const emit = defineEmits<{ spotlight: [] }>()

const apps = useApps()
const desktop = useDesktop()
const windows = useWindows()
const nc = useNotifications()

// 宿主菜单栏扩展（shell.menubarExtras）在移动形态的落点之一：主屏头部（时钟与
// 搜索之间）；导航胶囊「更多」面板是另一落点（见 MobilePill）——手机形态无
// 菜单栏，扩展组件自身按形态决定渲染形态。
const extras = computed(() => (shell.menubarExtras ? ([] as Component[]).concat(shell.menubarExtras) : []))

const now = ref(new Date())
let timer = 0
onMounted(() => {
  timer = window.setInterval(() => { now.value = new Date() }, 15_000)
})
onBeforeUnmount(() => clearInterval(timer))

const hh = () => String(now.value.getHours()).padStart(2, '0')
const mm = () => String(now.value.getMinutes()).padStart(2, '0')
const dateText = () => {
  const d = now.value
  const wd = ['日', '一', '二', '三', '四', '五', '六'][d.getDay()]
  return `${d.getMonth() + 1}月${d.getDate()}日 星期${wd}`
}
</script>

<template>
  <div class="mhome" :style="{ background: desktop.wallpapers[desktop.wallpaper] }">
    <!-- 状态行：时钟点击唤出通知中心；右侧 = 宿主菜单栏扩展（移动落点）+ 搜索唤出 Spotlight -->
    <header class="mhead">
      <button class="clock" data-os-nc-clock @click="nc.openPanel()">
        <b class="time">{{ hh() }}:{{ mm() }}</b>
        <span class="date">{{ dateText() }}</span>
      </button>
      <div class="mhead-side">
        <component :is="c" v-for="(c, i) in extras" :key="i" />
        <button class="search" title="搜索" @click="emit('spotlight')">
          <svg viewBox="0 0 20 20" width="19" height="19"><circle cx="8.5" cy="8.5" r="5.6" fill="none" stroke="currentColor" stroke-width="1.9" /><line x1="13" y1="13" x2="17.4" y2="17.4" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" /></svg>
        </button>
      </div>
    </header>

    <!-- 单层应用网格：滚动不分页，可见应用全量平铺（hidden 应用不出现） -->
    <nav class="mgrid" @contextmenu.prevent>
      <button v-for="a in apps.visible" :key="a.id" class="mitem" @click="windows.open(a.id)">
        <AppIcon :app="a.id" :size="58" />
        <span class="label">{{ a.name }}</span>
      </button>
    </nav>
  </div>
</template>

<style scoped>
.mhome {
  position: absolute; inset: 0; z-index: 10;
  display: flex; flex-direction: column; overflow: hidden;
}
.mhead {
  flex: none; display: flex; align-items: flex-end; justify-content: space-between;
  padding: calc(env(safe-area-inset-top, 0px) + 18px) 20px 4px;
}
/* 右侧组：宿主菜单栏扩展（门店切换器等）与搜索按钮同行 */
.mhead-side { display: flex; align-items: center; gap: 8px; min-width: 0; }
.clock {
  border: none; background: none; cursor: pointer;
  display: flex; flex-direction: column; align-items: flex-start; gap: 2px;
  padding: 4px 8px; margin: -4px 0 0 -8px; font-family: inherit;
}
.clock .time {
  font-size: 34px; font-weight: 600; line-height: 1.1; letter-spacing: .5px;
  color: #fff; text-shadow: 0 1px 12px rgba(0, 0, 0, .4);
}
.clock .date { font-size: 13px; color: rgba(255, 255, 255, .88); text-shadow: 0 1px 6px rgba(0, 0, 0, .4); }
.search {
  width: 40px; height: 40px; border-radius: 50%; border: none; cursor: pointer;
  background: var(--os-panel); color: var(--os-text);
  backdrop-filter: blur(16px); -webkit-backdrop-filter: blur(16px);
  border: 1px solid var(--os-border);
  display: flex; align-items: center; justify-content: center;
}
.mgrid {
  flex: 1; min-height: 0; overflow-y: auto; -webkit-overflow-scrolling: touch;
  display: grid; grid-template-columns: repeat(4, 1fr); align-content: start;
  gap: 18px 6px; padding: 14px 14px calc(env(safe-area-inset-bottom, 0px) + 28px);
}
.mitem {
  border: none; background: none; cursor: pointer; font-family: inherit;
  display: flex; flex-direction: column; align-items: center; gap: 7px;
  padding: 6px 2px;
}
.mitem:active .app-icon { transform: scale(.9); }
.mitem .app-icon { transition: transform .14s var(--os-ease-out); }
.label {
  max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 12px;
  /* 固定白字不随主题：文字叠在壁纸上，深色主题变量在浅壁纸上不可读（用户拍板） */
  color: #fff; text-shadow: 0 1px 4px rgba(0, 0, 0, .55), 0 0 8px rgba(0, 0, 0, .25);
}
</style>
