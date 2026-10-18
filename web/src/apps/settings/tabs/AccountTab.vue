<script setup lang="ts">
// 账户分区：资料显示名自助编辑 + 改密（本地登录）/ 统一身份跳转（SSO 模式）。
// ssoLogin 由壳层判定传入（宿主组壳注入 login.mode='sso' 时非空）：身份与密码
// 托管于宿主统一身份系统。
import { computed, onMounted, ref } from 'vue'
import { api } from '../../../api/client'
import { useSession } from '../../../stores/session'
import { t } from '../../../i18n'
import { OsButton, OsInput } from '../../../ui'
import type { ShellLogin } from '../../../shell'
import { ensureAccess, isUUID, roleLabel, shortUN } from '../useAccess'

type SSOLogin = Extract<ShellLogin, { mode: 'sso' }>
const props = defineProps<{ ssoLogin: SSOLogin | null }>()

const session = useSession()
function openSsoPassword() {
  if (props.ssoLogin?.passwordUrl) window.open(props.ssoLogin.passwordUrl, '_blank', 'noopener')
}

// ---- 改密（本地登录模式） ----
const oldPwd = ref(''), newPwd = ref(''), pwdMsg = ref('')
async function changePwd() {
  pwdMsg.value = ''
  try {
    await api.put('/api/users/password', { old: oldPwd.value, new: newPwd.value })
    pwdMsg.value = t('settings.acc.pwdDone')
    oldPwd.value = ''; newPwd.value = ''
  } catch (e: any) { pwdMsg.value = e.message }
}

// ---- 显示名自助编辑（PUT /api/users/display-name），保存后同步 session 供菜单栏等即刷 ----
const displayName = ref(''), dnMsg = ref('')
const accountSub = computed(() => {
  const u = session.user
  if (!u) return ''
  const who = isUUID(u.username)
    ? [u.displayName || shortUN(u.username)]
    : [u.username, u.displayName].filter(Boolean)
  return who.concat(t('settings.roleWith', { role: roleLabel(u.role) })).join(' · ')
})
async function saveDisplayName() {
  dnMsg.value = ''
  const name = displayName.value.trim()
  if (!name) { dnMsg.value = t('settings.acc.dnEmpty'); return }
  try {
    await api.put('/api/users/display-name', { displayName: name })
    if (session.user) session.user.displayName = name
    dnMsg.value = t('settings.acc.saved')
  } catch (e: any) { dnMsg.value = e.message }
}

onMounted(() => {
  displayName.value = session.user?.displayName || '' // 进入分区回填当前显示名（原 watch(tab) 语义）
  void ensureAccess() // 管理员预载角色目录：资料行的角色名展示依赖宿主注入的角色显示名
})
</script>

<template>
  <h2 class="sec">{{ t('settings.acc.profile') }}</h2>
  <div class="card">
    <div class="group">
      <div class="g-text"><div class="g-title">{{ t('settings.acc.account') }}</div><div class="g-sub">{{ accountSub }}</div></div>
    </div>
    <div class="group pwd">
      <div class="g-text"><div class="g-title">{{ t('settings.acc.displayName') }}</div></div>
      <OsInput v-model="displayName" :placeholder="t('settings.acc.dnPh')" @keyup.enter="saveDisplayName" />
    </div>
    <div class="group">
      <div class="g-text" />
      <OsButton variant="primary" @click="saveDisplayName">{{ t('settings.acc.saveDn') }}</OsButton>
      <span v-if="dnMsg" class="msg">{{ dnMsg }}</span>
    </div>
  </div>
  <h2 class="sec">{{ t('settings.acc.security') }}</h2>
  <!-- SSO 模式：密码托管于统一身份系统，本地改密表单不渲染；宿主可注入
       passwordUrl 提供「前往统一身份改密」跳转（新开页签，桌面不丢状态） -->
  <div v-if="ssoLogin" class="card">
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ t('settings.acc.ssoTitle') }}</div>
        <div class="g-sub">{{ t('settings.acc.ssoSub') }}</div>
      </div>
      <OsButton v-if="ssoLogin.passwordUrl" variant="primary" @click="openSsoPassword">
        {{ ssoLogin.passwordLabel || t('settings.acc.ssoBtn') }}
      </OsButton>
    </div>
  </div>
  <div v-else class="card">
    <div class="group pwd">
      <div class="g-text"><div class="g-title">{{ t('settings.acc.oldPwd') }}</div></div>
      <OsInput v-model="oldPwd" type="password" :placeholder="t('settings.acc.oldPwd')" />
    </div>
    <div class="group pwd">
      <div class="g-text"><div class="g-title">{{ t('settings.acc.newPwd') }}</div></div>
      <OsInput v-model="newPwd" type="password" :placeholder="t('settings.acc.pwdPh')" />
    </div>
    <div class="group">
      <div class="g-text" />
      <OsButton variant="primary" @click="changePwd">{{ t('settings.acc.changePwd') }}</OsButton>
      <span v-if="pwdMsg" class="msg">{{ pwdMsg }}</span>
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
/* 表单行：label 不参与收缩（防中文逐字竖排），输入框占剩余空间并限宽 */
.card .group.pwd .g-text { flex: none; white-space: nowrap; }
.card .group.pwd .os-input { flex: 1; max-width: 300px; }
.g-title { font-size: 13px; }
.g-sub { font-size: 11.5px; color: var(--os-text-secondary); margin-top: 1px; }
.card .group .msg { font-size: 12px; color: var(--os-text-secondary); }
.msg { font-size: 12px; margin-left: 8px; color: var(--os-text-secondary); }
</style>
