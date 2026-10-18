<script setup lang="ts">
// 用户管理分区（admin）：用户列表（角色/禁用/删除/行内改显示名）+ 新建用户弹窗。
// 角色下拉与角色显示名依赖 useAccess 的共享角色目录。
import { computed, onMounted, ref } from 'vue'
import { api } from '../../../api/client'
import { useSession } from '../../../stores/session'
import { t } from '../../../i18n'
import { osConfirm, toast, OsButton, OsInput, OsModal, OsSelect, OsTable } from '../../../ui'
import type { ShellLogin } from '../../../shell'
import { ensureAccess, roleLabel, roles, shortUN } from '../useAccess'

// SSO 模式（壳层判定传入）：新建用户密码留空 → 服务端分配随机值，登录统一走宿主交换
type SSOLogin = Extract<ShellLogin, { mode: 'sso' }>
defineProps<{ ssoLogin: SSOLogin | null }>()

const session = useSession()
interface UserRow { id: number; username: string; displayName: string; role: string; disabled: boolean; createdAt: string }
const users = ref<UserRow[]>([])
const showCreate = ref(false)
const nu = ref({ username: '', password: '', role: 'user', displayName: '' })
// 创建用户的角色下拉跟随系统实际角色（含宿主注入的自定义角色）
const roleOptions = computed(() => roles.value.map(r => ({ value: r, label: roleLabel(r) })))

async function loadUsers() { users.value = await api.get<UserRow[]>('/api/users') }

async function createUser() {
  try {
    await api.post('/api/users', nu.value)
    showCreate.value = false
    nu.value = { username: '', password: '', role: 'user', displayName: '' }
    loadUsers()
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}
async function setRole(row: UserRow, role: string) {
  await api.put(`/api/users/${row.username}`, { role })
  loadUsers()
}
async function setDisabled(row: UserRow, disabled: boolean) {
  try {
    await api.put(`/api/users/${row.username}`, { disabled })
    loadUsers()
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}
async function removeUser(row: UserRow) {
  if (!(await osConfirm({ title: t('settings.users.delTitle'), body: t('settings.users.delBody', { name: row.displayName || row.username }), danger: true, confirmText: t('common.delete') }))) return
  try { await api.del(`/api/users/${row.username}`); loadUsers() } catch (e: any) { toast(e.message, { type: 'error' }) }
}
// 管理员在用户管理直接改显示名（与账户页自助接口互为补充，走 admin 通道）
const dnEdit = ref<{ row: UserRow } | null>(null)
const dnEditVal = ref('')
function editDisplayName(row: UserRow) {
  dnEdit.value = { row }
  dnEditVal.value = row.displayName || ''
}
async function saveDisplayNameEdit() {
  const name = dnEditVal.value.trim()
  if (!name) { toast(t('settings.acc.dnEmpty'), { type: 'error' }); return }
  const row = dnEdit.value!.row
  try {
    await api.put(`/api/users/${row.username}`, { displayName: name })
    row.displayName = name
    const su = session.user
    if (su && su.username === row.username) su.displayName = name // 管理员改的是自己时，菜单栏即刷
    dnEdit.value = null
  } catch (e: any) { toast(e.message, { type: 'error' }) }
}

onMounted(() => {
  void ensureAccess() // 角色下拉选项依赖共享角色目录
  void loadUsers() // 进入分区即刷新（原 watch(tab) 语义：账户页/别处改名后切回本页即时同步）
})
</script>

<template>
  <div class="panel-actions">
    <OsButton variant="primary" size="sm" @click="showCreate = true">{{ t('settings.users.create') }}</OsButton>
  </div>
  <OsTable :columns="[
    { key: 'displayName', label: t('settings.users.colDisplayName'), width: '24%' },
    { key: 'username', label: t('settings.users.colUsername'), width: '14%' },
    { key: 'disabled', label: t('settings.users.colStatus'), width: '12%' },
    { key: 'createdAt', label: t('settings.users.colCreatedAt'), width: '22%' },
  ]" :rows="users">
    <!-- OsTable 插槽的 row 是 Record<string, any>（组件非泛型），传给行处理函数时断言收窄为 UserRow -->
    <template #cell-displayName="{ row }">
      <span class="dname" :class="{ unset: !row.displayName }">{{ row.displayName || t('settings.users.unset') }}</span>
      <button class="mini-edit" :title="t('settings.users.editDn')" @click="editDisplayName(row as UserRow)">✎</button>
    </template>
    <template #cell-username="{ value }">
      <span class="umono" :title="value">{{ shortUN(value) }}</span>
    </template>
    <template #cell-disabled="{ value }">
      <span class="ustate" :class="{ off: value }">{{ value ? t('settings.users.disabled') : t('settings.users.active') }}</span>
    </template>
    <template #ops="{ row }">
      <OsSelect
        :model-value="row.role" :options="roleOptions" style="padding:3px 6px;font-size:12px"
        @update:model-value="(v: any) => setRole(row as UserRow, String(v))"
      />
      <!-- 按角色保护（宿主体系下管理员用户名多为 UUID，字面量 'admin' 判定会漏保护
           服务账号）；服务端另有「最后一名管理员」兜底 -->
      <OsButton size="sm" :variant="row.disabled ? 'primary' : 'plain'" :disabled="row.role === 'admin'" @click="setDisabled(row as UserRow, !row.disabled)">
        {{ row.disabled ? t('settings.users.enable') : t('settings.users.disable') }}
      </OsButton>
      <OsButton size="sm" variant="danger" :disabled="row.role === 'admin'" @click="removeUser(row as UserRow)">{{ t('common.delete') }}</OsButton>
    </template>
  </OsTable>
  <p class="hint">{{ t('settings.users.hint') }}</p>

  <!-- 弹窗：修改显示名（管理员通道，用户自己走账户页自助） -->
  <OsModal v-if="dnEdit" :title="t('settings.users.editDn')" :ok-text="t('common.save')" @ok="saveDisplayNameEdit" @cancel="dnEdit = null">
    <div class="dlg-form">
      <div><label>{{ t('settings.users.fUser') }}</label><div class="dlg-ro">{{ dnEdit.row.displayName || shortUN(dnEdit.row.username) }}（{{ shortUN(dnEdit.row.username) }}）</div></div>
      <div><label>{{ t('settings.users.fNewDn') }}</label><OsInput v-model="dnEditVal" :placeholder="t('settings.acc.dnPh')" @keyup.enter="saveDisplayNameEdit" /></div>
    </div>
  </OsModal>

  <!-- 弹窗：新建用户（SSO 模式下密码留空 → 服务端分配随机值，登录统一走宿主交换） -->
  <OsModal v-if="showCreate" :title="t('settings.users.newTitle')" :ok-text="t('settings.users.okCreate')" @ok="createUser" @cancel="showCreate = false">
    <div class="dlg-form">
      <div><label>{{ t('settings.users.fUsername') }}</label><OsInput v-model="nu.username" placeholder="zhangsan" mono /></div>
      <div><label>{{ t('settings.users.fDisplayName') }}</label><OsInput v-model="nu.displayName" placeholder="张三" /></div>
      <div><label>{{ t('settings.users.fRole') }}</label><OsSelect v-model="nu.role" :options="roleOptions" /></div>
      <div v-if="!ssoLogin"><label>{{ t('settings.users.fPassword') }}</label><OsInput v-model="nu.password" type="password" :placeholder="t('settings.users.pwdPh')" /></div>
      <p v-else class="hint">{{ t('settings.users.ssoHint') }}</p>
    </div>
  </OsModal>
</template>

<style scoped>
.panel-actions { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.hint { font-size: 12px; color: var(--os-text-secondary); margin-top: 10px; }
.ustate {
  display: inline-block; font-size: 12px; padding: 1px 10px; border-radius: 999px;
  color: #1d8a3e; background: rgba(48,209,88,.12);
}
/* 用户管理：显示名 + 行内改名小按钮；用户名列等宽字体（UUID 截断形态） */
.dname.unset { color: var(--os-text-secondary); }
.mini-edit {
  margin-left: 6px; padding: 0 5px; border: none; background: transparent;
  color: var(--os-text-secondary); font-size: 12px; line-height: 18px;
  border-radius: 4px; cursor: pointer;
}
.mini-edit:hover { color: var(--os-accent, #2563eb); background: rgba(37,99,235,.08); }
.umono { font-family: var(--os-mono); font-size: 12px; }
.ustate.off { color: #b25b00; background: rgba(255,159,10,.14); }
/* 弹窗表单：覆盖 OsModal 内容默认居中，改左对齐纵排 */
.dlg-form { text-align: left; display: flex; flex-direction: column; gap: 8px; width: 460px; max-width: 70vw; }
.dlg-form label { font-size: 12px; color: var(--os-text-secondary); display: flex; align-items: center; gap: 8px; }
.dlg-form .hint { margin-top: 2px; }
.dlg-form .dlg-ro { font-size: 13px; padding: 7px 0 1px; }
</style>
