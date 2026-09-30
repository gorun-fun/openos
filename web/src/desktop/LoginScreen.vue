<script setup lang="ts">
import { ref } from 'vue'
import { useSession } from '../stores/session'
import { WALLPAPERS, useDesktop } from '../stores/desktop'

const session = useSession()
const desktop = useDesktop()
const username = ref('admin')
const password = ref('')
const error = ref('')
const loading = ref(false)

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
</script>

<template>
  <div class="login" :style="{ background: WALLPAPERS[desktop.wallpaper] }">
    <div class="login-card">
      <div class="avatar">🖥️</div>
      <h1>OpenOS</h1>
      <p class="sub">仿 macOS 网页桌面 · 多用户 · 事件总线</p>
      <form @submit.prevent="login">
        <input v-model="username" placeholder="用户名" autocomplete="username" />
        <input v-model="password" type="password" placeholder="密码" autocomplete="current-password" />
        <div v-if="error" class="error">{{ error }}</div>
        <button type="submit" :disabled="loading">{{ loading ? '登录中…' : '登 录' }}</button>
      </form>
      <div class="hint">
        <span @click="username = 'admin'; password = 'admin123'">admin / admin123（管理员）</span>
        <span @click="username = 'user'; password = 'user123'">user / user123（普通）</span>
        <span @click="username = 'guest'; password = 'guest123'">guest / guest123（访客）</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login {
  height: 100%; display: flex; align-items: center; justify-content: center;
  animation: os-fade-in .4s ease;
}
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
</style>
