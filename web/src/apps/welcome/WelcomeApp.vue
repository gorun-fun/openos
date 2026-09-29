<script setup lang="ts">
import { useWindows } from '../../stores/windows'
import { OsButton } from '../../ui'
import type { WinState } from '../../stores/windows'

defineProps<{ win: WinState }>()
const windows = useWindows()

const features = [
  { icon: '🪟', title: '真实窗体管理', desc: '拖拽移动、八向缩放、红绿灯控制、焦点层级、Dock 最小化，一应俱全。' },
  { icon: '🚌', title: '事件总线', desc: '前后端与跨窗口统一通过事件总线通信（WebSocket），本地即时分发 + 跨端广播。' },
  { icon: '🧩', title: '开放能力', desc: '第三方用统一基础 UI 组合新应用：组件型（OpenOS.registerApp）或声明式（JSON Schema）。' },
  { icon: '✦', title: 'goder AI 助手', desc: '完整 AI 编程工作台（Goder - AI助手）：项目、会话、聊天、审查与终端一体，还能操控桌面。' },
  { icon: '🗒️', title: '便签', desc: '随手记录的便签应用，桌面小组件联动展示，可拖动可缩放并对齐网格。' },
  { icon: '👥', title: '多用户多角色', desc: 'admin / user / guest 三级角色，管理员在系统设置中配置各角色可访问的应用。' },
  { icon: '💾', title: 'SQLite 配置', desc: '用户、角色授权、应用注册、虚拟文件系统全部由轻量 SQLite 持久化。' },
]

const quick = [
  { id: 'terminal', label: '终端' }, { id: 'files', label: '文件' },
  { id: 'docs', label: '文档' }, { id: 'sheets', label: '表格' },
  { id: 'slides', label: '幻灯片' }, { id: 'pdf', label: 'PDF' },
  { id: 'calculator', label: '计算器' }, { id: 'goder', label: 'Goder' },
  { id: 'notes', label: '便签' }, { id: 'settings', label: '设置' },
]
</script>

<template>
  <div class="welcome">
    <header>
      <div class="logo">🖥️</div>
      <h1>欢迎使用 OpenOS</h1>
      <p>前后端分离的仿 macOS 网页桌面 · Go + Vite · 事件总线驱动</p>
    </header>

    <section class="quick">
      <OsButton v-for="q in quick" :key="q.id" size="sm" @click="windows.open(q.id)">{{ q.label }}</OsButton>
    </section>

    <section class="grid">
      <article v-for="f in features" :key="f.title">
        <div class="ic">{{ f.icon }}</div>
        <h3>{{ f.title }}</h3>
        <p>{{ f.desc }}</p>
      </article>
    </section>

    <footer>
      提示：桌面右键可添加便签等小组件；或在终端输入 <code>open sheets</code>；按 <code>Alt+Space</code> 打开启动台。
    </footer>
  </div>
</template>

<style scoped>
.welcome { width: 100%; overflow-y: auto; padding: 26px 30px; background: var(--os-window); }
header { text-align: center; margin-bottom: 18px; }
.logo { font-size: 44px; }
h1 { font-size: 20px; margin-top: 6px; }
header p { font-size: 12.5px; color: var(--os-text-secondary); margin-top: 4px; }
.quick { display: flex; gap: 8px; flex-wrap: wrap; justify-content: center; margin-bottom: 20px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }
article {
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 12px; padding: 14px;
}
.ic { font-size: 22px; }
h3 { font-size: 13.5px; margin: 6px 0 4px; }
p { font-size: 12px; color: var(--os-text-secondary); line-height: 1.6; }
footer { margin-top: 18px; font-size: 12px; color: var(--os-text-secondary); text-align: center; }
code { background: var(--os-active); padding: 1px 6px; border-radius: 5px; font-family: var(--os-mono); }
</style>
