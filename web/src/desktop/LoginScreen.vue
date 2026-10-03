<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useSession } from '../stores/session'
import { useDesktop } from '../stores/desktop'
import { shell } from '../shell'

const session = useSession()
const desktop = useDesktop()
const username = ref('admin')
const password = ref('')
const error = ref('')
const loading = ref(false)

const sso = computed(() => (shell.login.mode === 'sso' ? shell.login : null))
// 宿主 SSO 回跳失败时带 sso_error 参数，展示错误并保留手动重试（不再自动跳转）
const ssoError = ref(new URLSearchParams(location.search).get('sso_error') || '')

async function login() {
  if (!username.value || !password.value || loading.value) return
  loading.value = true
  error.value = ''
  try {
    await session.login(username.value, password.value)
  } catch (e: any) {
    error.value = e.message || '登录失败'
  } finally {
    loading.value = false
  }
}

function ssoLogin() {
  if (sso.value) location.href = sso.value.url
}

// SSO 模式：无错误时短暂展示后自动跳转（宿主会话仍有效时全程无感回来）
let ssoTimer = 0
onMounted(() => {
  if (sso.value && !ssoError.value) ssoTimer = window.setTimeout(ssoLogin, 600)
})
onBeforeUnmount(() => clearTimeout(ssoTimer))

// 桌面客户端专属：登录前切换服务器（OpenOS 兼容端）——整窗转客户端本地设置页，
// 从那里连接新地址（连回原地址则重新进登录页）
const serverSettingsUrl = (window as any).__OPENOS_DESKTOP__?.settingsUrl as string | undefined
function switchServer() {
  if (serverSettingsUrl) location.href = serverSettingsUrl
}
</script>

<template>
  <div class="login" :style="{ background: desktop.wallpapers[desktop.wallpaper] }">
    <div class="login-card">
      <div class="avatar">{{ shell.brand.logo }}</div>
      <h1>{{ shell.brand.name }}</h1>
      <p class="sub">{{ shell.brand.sub }}</p>

      <template v-if="sso">
        <div v-if="ssoError" class="error">{{ ssoError }}</div>
        <button :disabled="loading" @click="ssoLogin">
          {{ loading ? '跳转中…' : (sso.label || '使用统一身份登录') }}
        </button>
        <p class="sso-hint">将跳转至统一登录页面，完成后自动返回</p>
      </template>

      <form v-else @submit.prevent="login">
        <input v-model="username" placeholder="用户名" autocomplete="username" />
        <input v-model="password" type="password" placeholder="密码" autocomplete="current-password" />
        <div v-if="error" class="error">{{ error }}</div>
        <button type="submit" :disabled="loading">{{ loading ? '登录中…' : '登 录' }}</button>
      </form>

      <div v-if="!sso" class="hint">
        <span @click="username = 'admin'; password = 'admin123'">admin / admin123（管理员）</span>
        <span @click="username = 'user'; password = 'user123'">user / user123（普通）</span>
        <span @click="username = 'guest'; password = 'guest123'">guest / guest123（访客）</span>
      </div>
    </div>

    <!-- 桌面客户端专属：登录前切换 OpenOS 兼容端 -->
    <button v-if="serverSettingsUrl" class="switch-server" @click="switchServer">⚙ 切换服务器</button>
  </div>
</template>

<style scoped>
.login {
  height: 100%; display: flex; align-items: center; justify-content: center;
  animation: os-fade-in .4s ease;
}
/* 桌面客户端的登录前切换服务器入口（桌面模式专属，浮于页面底部）；
   平铺 button 规则的 width/active 变换须显式覆盖 */
.switch-server {
  position: fixed; bottom: 26px; left: 50%; transform: translateX(-50%);
  width: auto; padding: 7px 18px; border: none; border-radius: 999px; margin: 0;
  background: rgba(0, 0, 0, .24); color: rgba(255, 255, 255, .78); font-size: 12.5px;
  font-weight: 400; letter-spacing: .5px; cursor: pointer;
  backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px);
  transition: background .15s, color .15s;
}
.switch-server:hover:not(:disabled) { background: rgba(0, 0, 0, .34); color: #fff; filter: none; }
.switch-server:active:not(:disabled) { transform: translateX(-50%); }
.login-card {
  width: 348px; padding: 44px 38px 26px; text-align: center; border-radius: 24px;
  background: rgba(255,255,255,.16);
  backdrop-filter: blur(36px) saturate(1.9);
  -webkit-backdrop-filter: blur(36px) saturate(1.9);
  border: 1px solid rgba(255,255,255,.32);
  box-shadow: 0 30px 80px rgba(0,0,0,.42), inset 0 1px 0 rgba(255,255,255,.35);
  color: #fff;
  animation: card-in .5s var(--os-ease-spring);
}
@keyframes card-in {
  from { opacity: 0; transform: translateY(18px) scale(.96); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
html:not(.dark) .login-card { color: #fff; border-color: rgba(255,255,255,.32); }
.avatar {
  width: 78px; height: 78px; margin: 0 auto 16px; border-radius: 50%;
  background: linear-gradient(145deg, #6a8dff, #a26bff);
  display: flex; align-items: center; justify-content: center; font-size: 38px;
  box-shadow: 0 10px 28px rgba(122,130,255,.5), 0 0 0 4px rgba(255,255,255,.18);
  animation: avatar-float 4.5s ease-in-out infinite;
}
@keyframes avatar-float {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-5px); }
}
h1 { font-size: 23px; font-weight: 700; letter-spacing: .5px; text-shadow: 0 1px 6px rgba(0,0,0,.25); }
.sub { font-size: 12px; opacity: .8; margin: 6px 0 24px; text-shadow: 0 1px 4px rgba(0,0,0,.2); }
input {
  width: 100%; padding: 11px 15px; margin-bottom: 10px; border-radius: 11px;
  border: 1px solid rgba(255,255,255,.34); background: rgba(255,255,255,.14); color: #fff;
  font-size: 14px; transition: border-color .15s, box-shadow .15s, background .15s;
}
input::placeholder { color: rgba(255,255,255,.65); }
input:focus {
  border-color: rgba(255,255,255,.85); background: rgba(255,255,255,.22);
  box-shadow: 0 0 0 3.5px rgba(255,255,255,.18);
}
button {
  width: 100%; padding: 11px; border: none; border-radius: 11px; margin-top: 8px;
  background: linear-gradient(180deg, #4da3ff, #0a84ff); color: #fff; font-size: 15px; font-weight: 600;
  letter-spacing: 2px; cursor: pointer; transition: filter .15s, transform .1s;
  box-shadow: 0 6px 18px rgba(10,132,255,.45), inset 0 1px 0 rgba(255,255,255,.3);
}
button:hover:not(:disabled) { filter: brightness(1.08); }
button:active:not(:disabled) { transform: scale(.98); }
button:disabled { opacity: .6; cursor: default; }
.error {
  color: #ffd7d7; font-size: 12px; margin-bottom: 8px;
  background: rgba(255,80,80,.22); border: 1px solid rgba(255,120,120,.4);
  border-radius: 8px; padding: 6px 10px;
}
.hint { margin-top: 20px; display: flex; flex-direction: column; gap: 5px; font-size: 11px; opacity: .78; }
.hint span {
  cursor: pointer; width: fit-content; margin: 0 auto; padding: 2px 10px; border-radius: 999px;
  transition: background .12s;
}
.hint span:hover { background: rgba(255,255,255,.16); text-decoration: none; }
.sso-hint { margin-top: 12px; font-size: 11px; opacity: .7; }

/* —— 手机形态：卡片自适应屏宽（348px 固定宽在 320/375px 屏会顶满溢出），键盘弹起可滚动 —— */
@media (max-width: 767px) {
  .login {
    height: 100dvh; overflow-y: auto; align-items: safe center;
    padding: calc(env(safe-area-inset-top, 0px) + 20px) 0 calc(env(safe-area-inset-bottom, 0px) + 20px);
  }
  .login-card { width: auto; max-width: 348px; margin: 0 16px; padding: 34px 24px 22px; }
}
</style>
