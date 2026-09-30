<script setup lang="ts">
import { onMounted } from 'vue'
import { setUnauthorizedHandler } from './api/client'
import { useSession } from './stores/session'
import { shell } from './shell'
import LoginScreen from './desktop/LoginScreen.vue'
import DesktopShell from './desktop/Desktop.vue'
import { OsToasts } from './ui'

const session = useSession()

onMounted(() => {
  // 任何接口返回 401（过期 / 管理员改角色 / 改密 / 删除用户）→ 回登录：
  // SSO 模式跳宿主登录入口（宿主会话仍有效时无感返回），本地模式刷新回登录页
  setUnauthorizedHandler(() => {
    session.logout()
    if (shell.login.mode === 'sso') {
      location.href = shell.login.url
      return
    }
    location.reload()
  })
  session.restore()
})
</script>

<template>
  <div v-if="!session.ready" class="splash">
    <div class="logo">{{ shell.brand.logo }}</div>
    <div class="bar"><span /></div>
  </div>
  <LoginScreen v-else-if="!session.user" />
  <DesktopShell v-else />
  <!-- 全局轻提示（登录页与桌面均可触发） -->
  <OsToasts />
</template>

<style scoped>
.splash {
  height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 18px; background: linear-gradient(160deg, #0f2027 0%, #203a43 50%, #2c5364 100%);
}
.logo { font-size: 56px; animation: os-fade-in .5s ease; }
.bar {
  width: 160px; height: 4px; border-radius: 2px; background: rgba(255,255,255,.15); overflow: hidden;
}
.bar span {
  display: block; height: 100%; width: 40%; border-radius: 2px; background: #fff;
  animation: splash-slide 1.1s ease-in-out infinite;
}
@keyframes splash-slide {
  0% { transform: translateX(-160px); }
  100% { transform: translateX(160px); }
}
</style>
