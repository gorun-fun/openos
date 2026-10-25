<script setup lang="ts">
// steward — 代码管家工作面（admin 专属，面向一人公司：版本 / 流水线 / 运行 /
// 问题修复一站式）：总览（发版健康 + 问题队列 + 修复动态）、版本（版本 × CI
// 健康，段选项目视图）、问题确认（手动/自动两种模式）、修复记录（Goder 会话
// 执行 → 提交推送 → 可选发版）、仓库（双登录模式：平台账号连接浏览接管 +
// 项目级单仓注册）、应用（实验：公开项目 AI 转制上架）、设置。引擎与 API 都
// 在 openos-server（/api/steward/*、/api/steward/pipe/*，admin 专属）。
// 页组件在 ./tabs/，流水线在 ./pipeline/，KeepAlive 保状态
// 与轮询。初始 tab 经 windows.open('steward', { tab: 'overview' }) 指定，
// 默认「总览」。壳骨架共用 TabAppShell（switchTab 经 ref 转发进 provide）。
import { provide, ref } from 'vue'
import type { Component } from 'vue'
import TabAppShell, { type TabDef } from './TabAppShell.vue'
import { tabMetas } from './tabs'
import Overview from './tabs/Overview.vue'
import Versions from './tabs/Versions.vue'
import Problems from './tabs/Problems.vue'
import Runs from './tabs/Runs.vue'
import Repos from './tabs/Repos.vue'
import Apps from './tabs/Apps.vue'
import Settings from './tabs/Settings.vue'

defineProps<{ win?: any }>()

const comps: Record<string, Component> = {
  overview: Overview, versions: Versions, problems: Problems, runs: Runs,
  repos: Repos, apps: Apps, settings: Settings,

}
const tabsDef: TabDef[] = tabMetas().map((t) => ({
  ...t,
  comp: comps[t.key]!,
  adminOnly: false,
}))

// App 内页签切换（转修成功后流水线弹窗原地跳「问题」页、总览卡片直达，
// 不再另开窗口）——转发给共用壳的 switchTab（未知 key 由壳忽略）
const shell = ref<InstanceType<typeof TabAppShell>>()
provide('stewardSwitchTab', (key: string) => shell.value?.switchTab(key))
</script>

<template>
  <TabAppShell ref="shell" :win="win" :tabs="tabsDef" />
</template>

<style scoped>
/* 表格观感：所有单元格单行省略——长问题标题 / clone URL 不再折行把行撑成三四行
   （列宽由各表声明约束，未声明宽度的列自动吸收剩余空间并在超长时省略） */
:deep(.os-table th),
:deep(.os-table td) { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
