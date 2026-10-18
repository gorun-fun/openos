<script setup lang="ts">
// 第三方应用分区（admin）：声明式应用（JSON Schema）注册，添加/编辑走弹窗。
// 列表底稿来自 useAccess 的共享应用目录；保存/删除后强制刷新目录。
import { computed, onMounted, ref } from 'vue'
import { api, type AppDef } from '../../../api/client'
import { t } from '../../../i18n'
import { bus } from '../../../bus/bus'
import { OsButton, OsInput, OsModal, OsPanel, OsEmpty, AppIcon } from '../../../ui'
import { allApps, appType, ensureAccess, isWebApp, loadAccess, removeApp } from '../useAccess'

const schemaText = ref('')
const regName = ref('')
const regId = ref('')
const regMsg = ref('')
const schemaDlg = ref(false)
// 编辑中的应用（null = 添加模式；编辑时 ID 只读，提交走 upsert）
const editing = ref<AppDef | null>(null)

// 应用管理列表 = 声明式第三方应用（网页应用归「网页应用」分区管理）
const registryApps = computed(() => allApps.value.filter((a) => !a.builtin && !isWebApp(a)))

function openAddSchema() {
  editing.value = null
  regId.value = ''; regName.value = ''; schemaText.value = ''; regMsg.value = ''
  schemaDlg.value = true
}
function openEditSchema(a: AppDef) {
  editing.value = a
  regId.value = a.id
  regName.value = a.name
  schemaText.value = a.config || ''
  regMsg.value = ''
  schemaDlg.value = true
}

const EXAMPLE_SCHEMA = JSON.stringify({
  title: '团队公告板',
  dataSource: {
    kind: 'static',
    items: [
      { title: 'v1.0 发布', body: 'OpenOS 桌面系统正式上线', author: 'admin', date: '周一' },
      { title: '新功能', body: '支持第三方声明式应用注册', author: 'goder', date: '周三' },
      { title: '修复', body: '窗口缩放在小屏下的边界问题', author: 'user', date: '周五' },
    ],
  },
  views: [
    { type: 'cards', title: '卡片', fields: [
      { key: 'title', label: '标题', primary: true },
      { key: 'body', label: '内容' },
      { key: 'author', label: '作者' },
    ] },
    { type: 'table', title: '列表', columns: [
      { key: 'title', label: '标题' }, { key: 'author', label: '作者' }, { key: 'date', label: '日期' },
    ] },
  ],
}, null, 2)

async function registerApp() {
  regMsg.value = ''
  try {
    JSON.parse(schemaText.value) // 校验
    await api.post('/api/apps', {
      id: editing.value ? editing.value.id : regId.value, name: regName.value, icon: 'default',
      category: '第三方', config: schemaText.value, width: 760, height: 560,
    })
    regMsg.value = t('settings.acc.saved')
    regId.value = ''; regName.value = ''
    editing.value = null
    schemaDlg.value = false
    loadAccess()
  } catch (e: any) { regMsg.value = t('settings.registry.saveFail', { msg: e.message }) }
}

onMounted(() => { void ensureAccess() })
</script>

<template>
  <div class="reg-head">
    <h2 class="sec" style="margin: 0">{{ t('settings.registry.count', { n: registryApps.length }) }}</h2>
    <span class="grow" />
    <OsButton size="sm" @click="openAddSchema">{{ t('settings.registry.addSchema') }}</OsButton>
  </div>

  <OsPanel v-if="registryApps.length" class="reg-list">
    <div v-for="a in registryApps" :key="a.id" class="reg-item">
      <AppIcon :app="a.id" :icon="a.icon" :size="34" />
      <div class="reg-meta">
        <span class="an">{{ a.name }}<em v-if="a.builtin || a.locked" class="builtin-badge">{{ t('settings.registry.builtin') }}</em></span>
        <span class="aid">{{ a.id }} · {{ appType(a) }}</span>
      </div>
      <span class="grow" />
      <OsButton size="sm" @click="bus.emit('sys:open', { app: a.id })">{{ t('settings.registry.open') }}</OsButton>
      <!-- 内置应用配置随版本发布、宿主锁定（locked）应用由宿主种子维护，均不可编辑/删除。
           列表已滤除网页应用（归「网页应用」分区编辑），编辑恒走 Schema 弹窗 -->
      <OsButton v-if="!a.builtin && !a.locked" size="sm" @click="openEditSchema(a)">{{ t('settings.registry.edit') }}</OsButton>
      <OsButton v-if="!a.builtin && !a.locked" size="sm" variant="danger" @click="removeApp(a.id)">{{ t('common.delete') }}</OsButton>
    </div>
  </OsPanel>
  <OsEmpty v-else :text="t('settings.registry.empty')" />

  <!-- 弹窗：注册 / 编辑声明式应用（JSON Schema） -->
  <OsModal v-if="schemaDlg" :title="editing ? t('settings.registry.schemaTitleEdit') : t('settings.registry.schemaTitleAdd')" :ok-text="editing ? t('common.save') : t('settings.registry.okRegister')"
           @ok="registerApp" @cancel="schemaDlg = false; editing = null">
    <div class="dlg-form">
      <div class="two">
        <div><label>{{ t('settings.registry.fIdEn') }}</label><OsInput v-model="regId" placeholder="notice-board" mono :disabled="!!editing" /></div>
        <div><label>{{ t('settings.registry.fName') }}</label><OsInput v-model="regName" placeholder="公告板" /></div>
      </div>
      <label>{{ t('settings.registry.fSchema') }}
        <OsButton size="sm" @click="schemaText = EXAMPLE_SCHEMA">{{ t('settings.registry.example') }}</OsButton>
      </label>
      <textarea v-model="schemaText" class="schema" spellcheck="false" :placeholder="t('settings.registry.schemaPh')" />
      <span class="msg">{{ regMsg }}</span>
      <p class="hint">{{ t('settings.registry.schemaHint') }}</p>
    </div>
  </OsModal>
</template>

<style scoped>
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.reg-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
.reg-list { padding: 4px 8px; }
.reg-item { display: flex; align-items: center; gap: 12px; padding: 8px 4px; border-bottom: 1px solid var(--os-border); }
.reg-item:last-child { border-bottom: none; }
.reg-item:hover { background: var(--os-hover); }
.reg-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.reg-meta .an, .reg-meta .aid { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.builtin-badge {
  font-style: normal; font-size: 10px; font-weight: 600; margin-left: 6px; padding: 1px 5px;
  border-radius: 4px; color: var(--os-text-secondary); background: var(--os-hover);
  border: 1px solid var(--os-border); vertical-align: 1px;
}
.grow { flex: 1; }
.hint { font-size: 12px; color: var(--os-text-secondary); margin-top: 10px; }
.an { font-size: 13px; font-weight: 600; }
.aid { font-size: 11px; color: var(--os-text-secondary); }
.msg { font-size: 12px; margin-left: 8px; color: var(--os-text-secondary); }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.schema {
  width: 100%; min-height: 200px; resize: vertical; border-radius: 9px; padding: 10px;
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  font-family: var(--os-mono); font-size: 12px; line-height: 1.5;
}
/* 弹窗表单：覆盖 OsModal 内容默认居中，改左对齐纵排 */
.dlg-form { text-align: left; display: flex; flex-direction: column; gap: 8px; width: 460px; max-width: 70vw; }
.dlg-form label { font-size: 12px; color: var(--os-text-secondary); display: flex; align-items: center; gap: 8px; }
.dlg-form .hint { margin-top: 2px; }
.dlg-form .schema { max-height: 32vh; }
</style>
