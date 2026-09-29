<script setup lang="ts">
import { onMounted } from 'vue'
import { setUnauthorizedHandler } from './api/client'
import { useSession } from './stores/session'
import LoginScreen from './desktop/LoginScreen.vue'
import DesktopShell from './desktop/Desktop.vue'

const session = useSession()

onMounted(() => {
  // 任何接口返回 401（过期 / 管理员改角色 / 改密 / 删除用户）→ 全局回登录页
  setUnauthorizedHandler(() => {
    session.logout()
    location.reload()
  })
  session.restore()
})
</script>

<template>
  <div v-if="!session.ready" class="splash">
    <div class="logo">🖥️</div>
    <div class="bar"><span /></div>
  </div>
  <LoginScreen v-else-if="!session.user" />
  <DesktopShell v-else />
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
