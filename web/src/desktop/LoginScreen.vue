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
  width: 340px; padding: 40px 36px 28px; text-align: center; border-radius: 20px;
  background: var(--os-panel); backdrop-filter: blur(30px) saturate(1.6);
  border: 1px solid rgba(255,255,255,.25); box-shadow: 0 24px 70px rgba(0,0,0,.4);
  color: #fff;
}
html:not(.dark) .login-card { color: var(--os-text); border-color: rgba(0,0,0,.08); }
.avatar {
  width: 76px; height: 76px; margin: 0 auto 14px; border-radius: 50%;
  background: linear-gradient(145deg, #6a8dff, #a26bff);
  display: flex; align-items: center; justify-content: center; font-size: 38px;
  box-shadow: 0 8px 24px rgba(106,141,255,.45);
}
h1 { font-size: 22px; font-weight: 700; letter-spacing: .5px; }
.sub { font-size: 12px; opacity: .75; margin: 6px 0 22px; }
input {
  width: 100%; padding: 10px 14px; margin-bottom: 10px; border-radius: 10px;
  border: 1px solid rgba(255,255,255,.3); background: rgba(255,255,255,.12); color: inherit;
  font-size: 14px; transition: border-color .15s;
}
html:not(.dark) input { background: #fff; border-color: var(--os-border); color: var(--os-text); }
input:focus { border-color: #0a84ff; }
button {
  width: 100%; padding: 11px; border: none; border-radius: 10px; margin-top: 6px;
  background: linear-gradient(180deg, #3d9bff, #0a84ff); color: #fff; font-size: 15px; font-weight: 600;
  cursor: pointer; transition: filter .15s;
}
button:hover { filter: brightness(1.08); }
button:disabled { opacity: .6; cursor: default; }
.error { color: #ff6b6b; font-size: 12px; margin-bottom: 8px; }
.hint { margin-top: 18px; display: flex; flex-direction: column; gap: 4px; font-size: 11px; opacity: .7; }
.hint span { cursor: pointer; }
.hint span:hover { text-decoration: underline; }
</style>
