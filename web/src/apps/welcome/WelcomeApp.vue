<script setup lang="ts">
import { useWindows } from '../../stores/windows'
import { OsButton, AppIcon } from '../../ui'
import type { WinState } from '../../stores/windows'

defineProps<{ win: WinState }>()
const windows = useWindows()

const features = [
  { icon: '🪟', title: '真实窗体管理', desc: '拖拽移动、八向缩放、红绿灯控制、焦点层级、Dock 最小化，一应俱全。' },
  { icon: '🔎', title: '聚焦搜索', desc: '⌘/Alt+Space 呼出：搜应用、递归搜文件、即时计算器、直达系统操作。' },
  { icon: '🗂', title: '访达式文件管理', desc: '图标/列表/分栏/画廊四视图、右键菜单、拖拽移动、废纸篓与空格快速预览。' },
  { icon: '📝', title: '文稿 · 表格 · 幻灯片', desc: 'Markdown 文稿、CSV 表格、HTML 幻灯片，均直接存取服务端文件，双击即开。' },
  { icon: '🚌', title: '事件总线', desc: '前后端与跨窗口统一通过事件总线通信（WebSocket），本地即时分发 + 跨端广播。' },
  { icon: '🧩', title: '开放能力', desc: '第三方用统一基础 UI 组合新应用：组件型（OpenOS.registerApp）或声明式（JSON Schema）。' },
  { icon: '✦', title: 'goder AI 助手', desc: '完整 AI 编程工作台：项目、会话、聊天、审查与终端一体，还能操控桌面。' },
  { icon: '👥', title: '多用户多角色', desc: 'admin / user / guest 与自定义角色，管理员在系统设置中配置各角色可访问的应用。' },
]

const quick = [
  { id: 'files', label: '文件' }, { id: 'docs', label: '文档' },
  { id: 'sheets', label: '表格' }, { id: 'slides', label: '幻灯片' },
  { id: 'terminal', label: '终端' }, { id: 'goder', label: 'Goder' },
  { id: 'notes', label: '便签' }, { id: 'settings', label: '设置' },
]
</script>

<template>
  <div class="welcome">
    <header>
      <div class="logo">🖥️</div>
      <h1>欢迎使用 OpenOS</h1>
      <p>前后端分离的仿 macOS 网页桌面 · Go + Vite · 事件总线驱动</p>
      <div class="quick">
        <button v-for="q in quick" :key="q.id" class="qbtn" @click="windows.open(q.id)">
          <AppIcon :app="q.id" :size="34" />
          <span>{{ q.label }}</span>
        </button>
      </div>
    </header>

    <section class="features">
      <article v-for="f in features" :key="f.title">
        <div class="ic">{{ f.icon }}</div>
        <div class="ft">
          <h3>{{ f.title }}</h3>
          <p>{{ f.desc }}</p>
        </div>
      </article>
    </section>

    <footer>
      提示：<code>⌘/Alt+Space</code> 聚焦搜索 · <code>F4</code> 启动台 · 桌面右键添加小组件 · 终端输入 <code>open sheets</code>
    </footer>
  </div>
</template>

<style scoped>
.welcome { width: 100%; overflow-y: auto; padding: 34px 38px 24px; background: var(--os-window); }
header { text-align: center; margin-bottom: 26px; }
.logo { font-size: 54px; line-height: 1; }
h1 { font-size: 23px; margin-top: 12px; }
header p { font-size: 12.5px; color: var(--os-text-secondary); margin-top: 5px; }
.quick { display: flex; gap: 14px; flex-wrap: wrap; justify-content: center; margin-top: 18px; }
.qbtn {
  border: none; background: transparent; cursor: pointer; font-family: inherit;
  display: flex; flex-direction: column; align-items: center; gap: 5px;
  padding: 6px 8px; border-radius: 12px; color: var(--os-text); font-size: 12px;
  transition: background .14s;
}
.qbtn:hover { background: var(--os-hover); }
.features { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 4px 18px; }
article {
  display: flex; gap: 12px; align-items: flex-start; padding: 12px 8px;
  border-bottom: 1px solid var(--os-border); border-radius: 8px;
}
article:hover { background: var(--os-hover); }
.ic { flex: none; font-size: 22px; width: 34px; text-align: center; }
h3 { font-size: 13px; margin-bottom: 3px; }
p { font-size: 12px; color: var(--os-text-secondary); line-height: 1.6; }
footer { margin-top: 20px; font-size: 12px; color: var(--os-text-secondary); text-align: center; }
code { background: var(--os-active); padding: 1px 6px; border-radius: 5px; font-family: var(--os-mono); }
</style>
