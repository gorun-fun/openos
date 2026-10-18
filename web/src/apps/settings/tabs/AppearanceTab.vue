<script setup lang="ts">
// 外观分区：深色/Dock/语言即时设置 + 壁纸选择 + 关于卡片。全部为桌面壳本地
// 状态（desktop store / shell 单例），无服务端加载。
import { ref } from 'vue'
import { api } from '../../../api/client'
import { useDesktop } from '../../../stores/desktop'
import { shell } from '../../../shell'
import { t, setLocale, locale, LOCALES, LOCALE_SETTINGS_KEY, type Locale } from '../../../i18n'
import { OsSelect, OsToggle } from '../../../ui'

const desktop = useDesktop()

// 语言：即时生效并写账号设置（多端一致；登录页切换仅本地缓存）
const curLocale = ref<Locale>(locale())
function onLocaleChange(v: string | undefined) {
  if (v !== 'zh-CN' && v !== 'en-US') return
  const l = v as Locale
  curLocale.value = l
  setLocale(l)
  void api.put('/api/settings', { key: LOCALE_SETTINGS_KEY, value: l }).catch(() => {})
}
</script>

<template>
  <h2 class="sec">{{ t('settings.appearance') }}</h2>
  <div class="card">
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ t('settings.ap.dark') }}</div>
        <div class="g-sub">{{ t('settings.ap.darkSub') }}</div>
      </div>
      <OsToggle :model-value="desktop.dark" @update:model-value="(v: any) => desktop.setDark(!!v)" />
    </div>
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ t('settings.ap.dockHide') }}</div>
        <div class="g-sub">{{ t('settings.ap.dockHideSub') }}</div>
      </div>
      <OsToggle :model-value="desktop.dockAutoHide" @update:model-value="(v: any) => desktop.setDockAutoHide(!!v)" />
    </div>
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ t('settings.ap.lang') }}</div>
        <div class="g-sub">{{ t('settings.ap.langSub') }}</div>
      </div>
      <OsSelect :model-value="curLocale" :options="LOCALES.map((l) => ({ value: l.value, label: l.label }))" @update:model-value="onLocaleChange" />
    </div>
  </div>
  <h2 class="sec">{{ t('settings.ap.wallpaper') }}</h2>
  <div class="wallpapers">
    <button
      v-for="(w, i) in desktop.wallpapers" :key="i"
      class="wall" :class="{ active: desktop.wallpaper === i }"
      :style="{ background: w }" :title="t('settings.ap.wallItem', { n: i + 1 })" @click="desktop.setWallpaper(i)"
    ><span v-if="desktop.wallpaper === i" class="wall-check">✓</span></button>
  </div>
  <h2 class="sec">{{ t('settings.ap.about') }}</h2>
  <div class="card">
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ shell.brand.logo }} {{ shell.brand.name }}</div>
        <div class="g-sub">{{ shell.brand.sub }}</div>
      </div>
      <!-- 宿主注入的产品版本（brand.version，独立部署时缺省不显示） -->
      <span v-if="shell.brand.version" class="ver">{{ shell.brand.version }}</span>
    </div>
  </div>
</template>

<style scoped>
/* macOS 系统设置式分组：卡片外的组标题 + 卡片内的“左说明右控件”行 */
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.sec:not(:first-child) { margin-top: 18px; }
.card {
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 10px; overflow: hidden;
}
.card .group { display: flex; align-items: center; gap: 14px; padding: 11px 14px; min-height: 44px; }
.card .group + .group { border-top: 1px solid var(--os-border); }
.g-text { flex: 1; min-width: 0; }
.g-title { font-size: 13px; }
.g-sub { font-size: 11.5px; color: var(--os-text-secondary); margin-top: 1px; }

.wallpapers { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; }
.wall {
  height: 72px; border-radius: 10px; border: 2px solid transparent; cursor: pointer;
  position: relative; transition: transform .12s; box-shadow: inset 0 0 0 1px rgba(0, 0, 0, .06);
}
.wall:hover { transform: scale(1.02); }
.wall.active { border-color: var(--os-accent); box-shadow: 0 0 0 3px rgba(10,132,255,.25); }
.wall-check {
  position: absolute; right: 6px; bottom: 5px; width: 20px; height: 20px; border-radius: 50%;
  background: var(--os-accent); color: #fff; font-size: 12px; line-height: 20px; text-align: center;
}
/* 关于卡片右侧的版本号（宿主 brand.version 注入） */
.ver { font-family: var(--os-mono); font-size: 12px; color: var(--os-text-secondary); white-space: nowrap; }
</style>
