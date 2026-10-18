<script setup lang="ts">
// 网页应用分区（admin）：统一代理的网页嵌入管理（kind=web 直嵌 / kind=proxy
// 服务端反代）。列表底稿来自 useAccess 的共享应用目录；保存/删除后强制刷新目录。
import { computed, onMounted, ref } from 'vue'
import { api, type AppDef } from '../../../api/client'
import { t } from '../../../i18n'
import { bus } from '../../../bus/bus'
import { OsButton, OsInput, OsModal, OsPanel, OsEmpty, AppIcon } from '../../../ui'
import { allApps, appType, ensureAccess, isWebApp, loadAccess, parseCfg, removeApp } from '../useAccess'

const webName = ref('')
const webUrl = ref('')
const webId = ref('')
const webMsg = ref('')
const webDlg = ref(false)
// 代理嵌入开关：config.kind=proxy，目标站经服务端反代（可嵌入拒绝 iframe 的站点）
const webProxy = ref(false)
// 自定义请求头（仅代理模式生效——iframe 直嵌无法附带自定义头）：行式编辑，
// 保存时合并为 config.headers 对象（同名行后者覆盖前者）
const webHeaders = ref<{ name: string; value: string }[]>([])
function addWebHeader() { webHeaders.value.push({ name: '', value: '' }) }
function delWebHeader(i: number) { webHeaders.value.splice(i, 1) }
// 编辑中的应用（null = 添加模式；编辑时 ID 只读，提交走 upsert）
const editing = ref<AppDef | null>(null)

// 网页应用列表 = 全部网页应用（含内置/商店安装的 kind=web|proxy）
const webApps = computed(() => allApps.value.filter((a) => isWebApp(a)))
const appUrl = (a: AppDef) => parseCfg(a).url || ''

// ---- 统一代理状态（/px/ 通用转发通道是否可用；OPENOS_PROXY=off 时关闭）----
const pxStatus = ref<{ enabled: boolean } | null>(null)
async function loadPxStatus() {
  try { pxStatus.value = await api.get<{ enabled: boolean }>('/api/proxy/status') } catch { pxStatus.value = null }
}

function openAddWeb() {
  editing.value = null
  webName.value = ''; webUrl.value = ''; webId.value = ''; webMsg.value = ''; webProxy.value = false
  webHeaders.value = []
  webDlg.value = true
}
function openEditWeb(a: AppDef) {
  editing.value = a
  webName.value = a.name
  webId.value = a.id
  webUrl.value = parseCfg(a).url || ''
  webProxy.value = parseCfg(a).kind === 'proxy'
  const hs = parseCfg(a).headers || {}
  webHeaders.value = Object.entries(hs).map(([name, value]) => ({ name, value: String(value) }))
  webMsg.value = ''
  webDlg.value = true
}

// ---- 网页应用注册（iframe 嵌入，config.kind=web，渲染走 WebApp 容器）----
async function registerWebApp() {
  webMsg.value = ''
  const raw = webUrl.value.trim()
  let u: URL
  try { u = new URL(raw) } catch { webMsg.value = t('settings.registry.badUrl'); return }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') { webMsg.value = t('settings.registry.proto'); return }
  // 与服务端 sanitizeID 同规则（小写、剔除 [a-z0-9-_.] 以外字符）——图标等
  // 派生 URL 必须用最终落库 ID，否则会指向不存在的应用（曾因点号被剥出过 404）
  const id = editing.value ? editing.value.id
    : (webId.value.trim() || 'web-' + u.hostname).toLowerCase().replace(/[^a-z0-9-_.]/g, '')
  if (!id) { webMsg.value = t('settings.registry.emptyId'); return }
  // config.headers 仅代理模式写入（直嵌模式带不上自定义头，存了也是死配置）
  const cfg: Record<string, any> = { kind: webProxy.value ? 'proxy' : 'web', url: u.href }
  if (webProxy.value) {
    const hs = webHeaders.value.filter((h) => h.name.trim())
    if (hs.length) cfg.headers = Object.fromEntries(hs.map((h) => [h.name.trim(), h.value]))
  }
  try {
    await api.post('/api/apps', {
      id, name: webName.value.trim() || u.hostname,
      icon: webProxy.value ? `/app/${id}/${u.origin.replace('://', '/')}/favicon.ico` : `${u.origin}/favicon.ico`,
      category: '网络', config: JSON.stringify(cfg), width: 1000, height: 660,
    })
    webMsg.value = t('settings.registry.webSaved')
    webName.value = ''; webUrl.value = ''; webId.value = ''; webHeaders.value = []
    editing.value = null
    webDlg.value = false
    loadAccess()
  } catch (e: any) { webMsg.value = t('settings.registry.saveFail', { msg: e.message }) }
}

onMounted(() => {
  void ensureAccess() // 应用列表（原根 onMounted 加载；watch 的“空则兜底”语义由幂等加载承接）
  void loadPxStatus()
})
</script>

<template>
  <div class="reg-head">
    <h2 class="sec" style="margin: 0">{{ t('settings.webapps.count', { n: webApps.length }) }}</h2>
    <span class="grow" />
    <OsButton variant="primary" size="sm" @click="openAddWeb">{{ t('settings.webapps.add') }}</OsButton>
  </div>

  <!-- 统一代理状态：开启时网页应用可用应用代理嵌入拒绝 iframe 的站点，
       且网络请求类应用（网络请求/行情等）默认经 /px/ 通用转发发送 -->
  <div class="card" :class="{ off: pxStatus && !pxStatus.enabled }">
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ pxStatus?.enabled ? t('settings.webapps.pxOn') : t('settings.webapps.pxOff') }}</div>
        <div class="g-sub">{{ pxStatus?.enabled ? t('settings.webapps.pxOnSub') : t('settings.webapps.pxOffSub') }}</div>
      </div>
      <span class="px-dot" :class="{ on: pxStatus?.enabled }">{{ pxStatus?.enabled ? 'ON' : 'OFF' }}</span>
    </div>
  </div>

  <OsPanel v-if="webApps.length" class="reg-list">
    <div v-for="a in webApps" :key="a.id" class="reg-item">
      <AppIcon :app="a.id" :icon="a.icon" :size="34" />
      <div class="reg-meta">
        <span class="an">{{ a.name }}<em v-if="a.builtin || a.locked" class="builtin-badge">{{ t('settings.registry.builtin') }}</em></span>
        <span class="aid">{{ a.id }} · {{ appType(a) }} · {{ appUrl(a) }}</span>
      </div>
      <span class="grow" />
      <OsButton size="sm" @click="bus.emit('sys:open', { app: a.id })">{{ t('settings.registry.open') }}</OsButton>
      <!-- 内置应用配置随版本发布、宿主锁定（locked）应用由宿主种子维护，均不可编辑/删除 -->
      <OsButton v-if="!a.builtin && !a.locked" size="sm" @click="openEditWeb(a)">{{ t('settings.registry.edit') }}</OsButton>
      <OsButton v-if="!a.builtin && !a.locked" size="sm" variant="danger" @click="removeApp(a.id)">{{ t('common.delete') }}</OsButton>
    </div>
  </OsPanel>
  <OsEmpty v-else :text="t('settings.webapps.empty')" />

  <!-- 弹窗：添加 / 编辑网页应用（iframe 嵌入，可选服务端代理） -->
  <OsModal v-if="webDlg" :title="editing ? t('settings.registry.webTitleEdit') : t('settings.registry.webTitleAdd')" :ok-text="editing ? t('common.save') : t('settings.registry.addBtn')"
           @ok="registerWebApp" @cancel="webDlg = false; editing = null">
    <div class="dlg-form">
      <div><label>{{ t('settings.registry.fName') }}</label><OsInput v-model="webName" placeholder="企业邮箱" /></div>
      <div><label>{{ t('settings.registry.fUrl') }}</label><OsInput v-model="webUrl" placeholder="https://example.com/" mono /></div>
      <div v-if="!editing"><label>{{ t('settings.registry.fId') }}</label><OsInput v-model="webId" placeholder="web-mail" mono /></div>
      <label class="px-toggle"><input type="checkbox" v-model="webProxy" /> {{ t('settings.registry.proxy') }}</label>
      <!-- 自定义请求头：仅代理模式生效（iframe 直嵌无法附带自定义头） -->
      <div v-if="webProxy" class="hdr-list">
        <label>{{ t('settings.registry.headers') }}</label>
        <div v-for="(h, i) in webHeaders" :key="i" class="hdr-row">
          <OsInput v-model="h.name" placeholder="Header-Name" mono />
          <OsInput v-model="h.value" :placeholder="t('settings.registry.hValue')" mono />
          <OsButton size="sm" variant="danger" @click="delWebHeader(i)">{{ t('common.delete') }}</OsButton>
        </div>
        <OsButton size="sm" @click="addWebHeader">{{ t('settings.registry.addHeader') }}</OsButton>
      </div>
      <p class="hint">{{ t('settings.registry.webHint') }}</p>
      <span class="msg">{{ webMsg }}</span>
    </div>
  </OsModal>
</template>

<style scoped>
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.card {
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 10px; overflow: hidden;
}
.card .group { display: flex; align-items: center; gap: 14px; padding: 11px 14px; min-height: 44px; }
.g-text { flex: 1; min-width: 0; }
.g-title { font-size: 13px; }
.g-sub { font-size: 11.5px; color: var(--os-text-secondary); margin-top: 1px; }
.reg-head { display: flex; align-items: center; gap: 8px; margin-bottom: 10px; }
/* 统一代理状态卡：开启为常规卡片，关闭时描边提示 */
.card.off { border-color: #b25b00; }
.px-dot {
  font-family: var(--os-mono); font-size: 11px; font-weight: 700; flex: none;
  padding: 2px 8px; border-radius: 999px; color: #b25b00; background: rgba(255, 159, 10, .14);
}
.px-dot.on { color: #1d8a3e; background: rgba(48, 209, 88, .12); }
.card.off { margin-bottom: 12px; }
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
/* 弹窗表单：覆盖 OsModal 内容默认居中，改左对齐纵排 */
.dlg-form { text-align: left; display: flex; flex-direction: column; gap: 8px; width: 460px; max-width: 70vw; }
.dlg-form label { font-size: 12px; color: var(--os-text-secondary); display: flex; align-items: center; gap: 8px; }
.dlg-form .hint { margin-top: 2px; }
.dlg-form .px-toggle { cursor: pointer; user-select: none; }
.dlg-form .hdr-list { display: flex; flex-direction: column; gap: 6px; }
.dlg-form .hdr-row { display: flex; gap: 6px; align-items: center; }
.dlg-form .hdr-row > :first-child { flex: 2; }
.dlg-form .hdr-row > :nth-child(2) { flex: 3; }
</style>
