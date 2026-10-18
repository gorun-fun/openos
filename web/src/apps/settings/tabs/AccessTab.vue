<script setup lang="ts">
// 角色与权限分区（admin）：应用授权 + 功能级权限点双区（P2 2026-10）。
// 数据与编辑态（accessRole/双区脏标记）在 useAccess 共享单例中——分区组件随
// tab 切换卸载重建，未保存的勾选必须跨切换存活。
import { computed, onMounted } from 'vue'
import { api } from '../../../api/client'
import { useApps } from '../../../stores/apps'
import { useSession } from '../../../stores/session'
import { t } from '../../../i18n'
import { OsButton, OsTabs } from '../../../ui'
import {
  access, accessDirty, accessRole, allApps, ensureAccess, hostWritable, permsDirty,
  permDefs, roleDefs, roleLabel, rolePerms, roles, type PermDefVM,
} from '../useAccess'

const session = useSession()
const apps = useApps()

const isAdminUser = computed(() => session.user?.role === 'admin')
const activeRoleDef = computed(() => roleDefs.value.find((r) => r.key === accessRole.value))
/** 只读查看者不可编辑；宿主托管角色在宿主未声明回写通道前同样只读（权威在宿主库） */
const roleReadonly = computed(() => !isAdminUser.value || (activeRoleDef.value?.managedBy === 'host' && !hostWritable.value))
// 权限点按声明的 group（业务模块，宿主声明如「客房管理」）分组展示；组名为空回落
// 归属标签（宿主业务权限/系统权限），openos 内置组 "system" 映射为系统权限文案。
// 目录顺序即展示顺序（host 组在前、system 在后，组内按 code）。
const permGroupsUI = computed(() => {
  const out: { key: string; group: string; managedBy: string; perms: PermDefVM[] }[] = []
  const idx = new Map<string, (typeof out)[number]>()
  for (const p of permDefs.value) {
    const group = p.group && p.group !== 'system' ? p.group : ''
    const mk = p.managedBy + '\u0000' + group
    let g = idx.get(mk)
    if (!g) {
      g = { key: mk, group, managedBy: p.managedBy, perms: [] }
      idx.set(mk, g)
      out.push(g)
    }
    g.perms.push(p)
  }
  return out
})
const rolesDirty = computed(() => accessDirty.value || permsDirty.value)

function toggleApp(role: string, id: string) {
  const cur = access.value[role] || []
  access.value[role] = cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]
  accessDirty.value = true
}
function togglePerm(role: string, code: string) {
  const cur = rolePerms.value[role] || []
  rolePerms.value[role] = cur.includes(code) ? cur.filter((x) => x !== code) : [...cur, code]
  permsDirty.value = true
}
async function saveAccess() {
  const role = accessRole.value
  if (roleDefs.value.length) {
    // 新通道：目录 + 权限点 + 应用授权一并保存（PUT /api/roles/{key}）
    const def = roleDefs.value.find((r) => r.key === role)
    await api.put(`/api/roles/${encodeURIComponent(role)}`, {
      label: roleLabel(role),
      perms: rolePerms.value[role] || [],
      apps: access.value[role] || [],
    })
    if (def) def.perms = [...(rolePerms.value[role] || [])]
  } else {
    // 老服务端兼容：仅应用授权
    await api.put('/api/apps/access', { role, apps: access.value[role] })
  }
  accessDirty.value = false
  permsDirty.value = false
  apps.refresh() // 自己桌面立即生效
}

onMounted(() => { void ensureAccess() })
</script>

<template>
  <div class="panel-actions">
    <OsTabs
      :tabs="roles.map(r => ({ key: r, label: roleLabel(r) }))"
      v-model="accessRole"
    />
    <span class="grow" />
    <OsButton variant="primary" size="sm" :disabled="roleReadonly || !rolesDirty" @click="saveAccess">
      {{ rolesDirty && !roleReadonly ? t('settings.roles.dirty') : t('settings.roles.saved') }}
    </OsButton>
  </div>
  <p v-if="activeRoleDef?.managedBy === 'host' && !hostWritable" class="hint">{{ t('settings.roles.hostManaged') }}</p>
  <p v-else-if="activeRoleDef?.managedBy === 'host'" class="hint">{{ t('settings.roles.hostEditable') }}</p>
  <p v-else-if="!isAdminUser" class="hint">{{ t('settings.roles.readonly') }}</p>
  <div class="roles-grid">
    <!-- 左区：功能级权限点（admin 直通提示 + 按业务模块分组勾选） -->
    <div class="roles-perms">
      <h3 class="sec">{{ t('settings.roles.perms') }}</h3>
      <p v-if="accessRole === 'admin'" class="hint">{{ t('settings.roles.allPerms') }}</p>
      <template v-else>
        <div v-for="g in permGroupsUI" :key="g.key">
          <p v-if="g.perms.length" class="perm-group">
            {{ g.group || t(g.managedBy === 'host' ? 'settings.roles.hostGroup' : 'settings.roles.systemGroup') }}
          </p>
          <label v-for="p in g.perms" :key="p.code" class="perm-item" :class="{ ro: roleReadonly }">
            <input
              type="checkbox"
              :disabled="roleReadonly"
              :checked="(rolePerms[accessRole] || []).includes(p.code)"
              @change="togglePerm(accessRole, p.code)"
            />
            <span class="an">{{ p.label || p.code }}</span>
            <span class="aid">{{ p.code }}</span>
          </label>
        </div>
        <p v-if="!permDefs.length" class="hint">{{ t('settings.roles.noPerms') }}</p>
      </template>
    </div>
    <!-- 右区：应用授权卡片网格（原 access 分区） -->
    <div class="roles-apps">
      <h3 class="sec">{{ t('settings.roles.apps') }}</h3>
      <div class="access-grid">
        <label v-for="a in allApps" :key="a.id" class="access-item" :class="{ ro: roleReadonly }">
          <input
            type="checkbox"
            :disabled="roleReadonly"
            :checked="(access[accessRole] || []).includes(a.id)"
            @change="toggleApp(accessRole, a.id)"
          />
          <span class="an">{{ a.name }}</span>
          <span class="aid">{{ a.id }} · {{ a.category }}</span>
        </label>
      </div>
      <p class="hint">{{ t('settings.access.hint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.panel-actions { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.grow { flex: 1; }
.hint { font-size: 12px; color: var(--os-text-secondary); margin-top: 10px; }
.access-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); gap: 8px; }
.access-item {
  display: flex; align-items: center; gap: 8px; background: var(--os-panel-solid);
  border: 1px solid var(--os-border); border-radius: 9px; padding: 9px 12px; cursor: pointer;
}
.access-item:hover { border-color: var(--os-border-strong); }
.access-item.ro, .perm-item.ro { cursor: not-allowed; opacity: .7; }
/* 角色与权限双区布局：左权限点 / 右应用授权（窄屏自动叠放） */
.roles-grid { display: grid; grid-template-columns: minmax(280px, 2fr) 3fr; gap: 18px; align-items: start; }
@media (max-width: 900px) { .roles-grid { grid-template-columns: 1fr; } }
.roles-perms h3.sec, .roles-apps h3.sec { margin: 2px 0 10px; }
.perm-group { font-size: 12px; font-weight: 700; color: var(--os-text-secondary); margin: 12px 0 6px; }
.perm-item {
  display: flex; align-items: center; gap: 8px; padding: 7px 10px; border-radius: 9px;
  border: 1px solid transparent; cursor: pointer;
}
.perm-item:hover { border-color: var(--os-border); background: var(--os-panel-solid); }
.perm-item .aid { flex: none; }
.an { font-size: 13px; font-weight: 600; }
.aid { font-size: 11px; color: var(--os-text-secondary); }
</style>
