<script setup lang="ts">
// 系统设置：分区壳——OsLayout 骨架 + 侧栏导航 + tab 分发。各分区内容拆至
// tabs/ 子组件（挂载即加载，原单组件 watch(tab) 的懒加载语义随迁）；跨分区
// 共享的应用/角色目录收敛在 useAccess.ts。桌面客户端专属的「服务器」分区
// （内嵌本地设置页 iframe）与 SSO 判断属壳层逻辑，留在这里。
import { computed, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { shell } from '../../shell'
import { t } from '../../i18n'
import { OsLayout } from '../../ui'
import AppearanceTab from './tabs/AppearanceTab.vue'
import AccountTab from './tabs/AccountTab.vue'
import UsersTab from './tabs/UsersTab.vue'
import AccessTab from './tabs/AccessTab.vue'
import WebappsTab from './tabs/WebappsTab.vue'
import RegistryTab from './tabs/RegistryTab.vue'
import AiTab from './tabs/AiTab.vue'
import SystemTab from './tabs/SystemTab.vue'

const props = defineProps<{ win: WinState }>()
const session = useSession()
const isAdmin = session.user?.role === 'admin'
// SSO 模式（宿主组壳注入 login.mode='sso'）：身份与密码托管于宿主统一身份系统。
// 账户分区（改密表单/跳转按钮）与用户管理分区（新建用户的密码字段）按它切换
const ssoLogin = computed(() => (shell.login.mode === 'sso' ? shell.login : null))

const tab = ref('appearance')
// 桌面客户端专属：服务器设置（OpenOS 兼容端连接）。标记对象带 settingsUrl =
// 客户端本地设置页地址（设置服务降级时无该字段，入口随之隐藏）
const serverSettingsUrl = (window as any).__OPENOS_DESKTOP__?.settingsUrl as string | undefined
const items = computed(() => {
  // AI 服务全员可见（只读状态 + goder 启用引导），其余管理页仅管理员
  const base = [
    { key: 'appearance', label: t('settings.tab.appearance'), icon: '🎨' },
    { key: 'account', label: t('settings.tab.account'), icon: '👤' },
  ]
  if (serverSettingsUrl) base.push({ key: 'server', label: t('settings.tab.server'), icon: '🌐' })
  base.push({ key: 'ai', label: t('settings.tab.ai'), icon: '✦' })
  if (isAdmin) {
    base.push(
      { key: 'users', label: t('settings.tab.users'), icon: '👥' },
      { key: 'access', label: t('settings.tab.roles'), icon: '🔐' },
      { key: 'webapps', label: t('settings.tab.webapps'), icon: '🌐' },
      { key: 'registry', label: t('settings.tab.registry'), icon: '🧩' },
      { key: 'system', label: t('settings.tab.system'), icon: 'ℹ️' },
    )
  }
  return base
})
</script>

<template>
  <OsLayout :win="props.win" :items="items" :active="tab" @select="tab = $event">
    <!-- 外观 -->
    <AppearanceTab v-if="tab === 'appearance'" />

    <!-- 账户 -->
    <AccountTab v-else-if="tab === 'account'" :sso-login="ssoLogin" />

    <!-- 用户管理 -->
    <UsersTab v-else-if="tab === 'users'" :sso-login="ssoLogin" />

    <!-- 角色与权限（应用授权升级版：权限点 + 应用授权双区） -->
    <AccessTab v-else-if="tab === 'access'" />

    <!-- 网页应用：统一代理的网页嵌入管理（kind=web 直嵌 / kind=proxy 服务端反代） -->
    <WebappsTab v-else-if="tab === 'webapps'" />

    <!-- 第三方应用：声明式应用（JSON Schema）注册，添加/编辑走弹窗 -->
    <RegistryTab v-else-if="tab === 'registry'" />

    <!-- 服务器（桌面客户端专属：内嵌客户端本地设置页，改连 OpenOS 兼容端） -->
    <template v-else-if="tab === 'server'">
      <iframe :src="serverSettingsUrl" class="server-frame" :title="t('settings.tab.server')" />
    </template>

    <!-- AI 服务（goder 联动） -->
    <AiTab v-else-if="tab === 'ai'" />

    <!-- 系统信息 -->
    <SystemTab v-else-if="tab === 'system'" />
  </OsLayout>
</template>

<style scoped>
/* 侧栏彩色图标块（SF Symbols 彩色方块语言） */
:deep(.os-sidebar button .ic) { color: #fff; }
:deep(.os-sidebar button[data-key='appearance'] .ic) { background: linear-gradient(180deg, #64d2ff, #0a84ff); }
:deep(.os-sidebar button[data-key='account'] .ic) { background: linear-gradient(180deg, #68d391, #30a14e); }
:deep(.os-sidebar button[data-key='users'] .ic) { background: linear-gradient(180deg, #8e8e93, #636366); }
:deep(.os-sidebar button[data-key='access'] .ic) { background: linear-gradient(180deg, #bf5af2, #8944ab); }
:deep(.os-sidebar button[data-key='registry'] .ic) { background: linear-gradient(180deg, #5ac8fa, #0a84ff); }
:deep(.os-sidebar button[data-key='ai'] .ic) { background: linear-gradient(180deg, #ff375f, #d70015); }
:deep(.os-sidebar button[data-key='server'] .ic) { background: linear-gradient(180deg, #5e5ce6, #3634a3); }
:deep(.os-sidebar button[data-key='system'] .ic) { background: linear-gradient(180deg, #a2a2a7, #77777c); }

/* 服务器设置：客户端本地设置页占满内容区（高度不够时保底 460px） */
.server-frame {
  display: block; width: 100%; height: 100%; min-height: 460px;
  border: none; border-radius: 8px;
}
</style>
