<script setup lang="ts">
// AI 服务分区（全员可见，admin 可改）：OpenOS 唯一的 AI 应用是 goder，此处配置
// 与其模型配置联动。goder 未部署/停用时给全员引导 + 管理员部署指引。
import { computed, onMounted, ref } from 'vue'
import { api } from '../../../api/client'
import { useSession } from '../../../stores/session'
import { t } from '../../../i18n'
import { OsButton, OsInput, OsPanel } from '../../../ui'

// GET /api/ai/status 的最小形状（全员可读、无密钥）。goder 子对象来自服务端
// goderMgr.Status()：available/running/disabled/aiProvider/aiSyncedUsers。
interface GoderStatus {
  available?: boolean
  running?: boolean
  disabled?: boolean
  aiProvider?: string
  aiSyncedUsers?: number
}
interface AiStatus {
  model?: string
  goder?: GoderStatus
}
// POST /api/ai/test 响应（判别联合）：成功带延迟/模型/回复；服务端失败与前端
// catch 分支同为 { ok: false, error } 形态
type AiTestResult =
  | { ok: true; latencyMs: number; model: string; reply: string }
  | { ok: false; error: string }

const session = useSession()
const isAdmin = session.user?.role === 'admin'

const aiCfg = ref({ baseUrl: '', apiKey: '', model: '' })
const aiConfigured = ref(false)
const aiMsg = ref('')
const aiTesting = ref(false)
const aiTestResult = ref<AiTestResult | null>(null)
// goder 状态（available=false = 未部署/被停用；全员可见的启用引导依据）
const aiStatus = ref<AiStatus | null>(null)
const goderDisabled = computed(() => !!aiStatus.value?.goder?.disabled)
const goderUnavailable = computed(() => !!aiStatus.value?.goder && !aiStatus.value.goder.available)
const goderIdle = computed(() => {
  const g = aiStatus.value?.goder
  return !!g && g.available && !g.disabled && !g.running
})
const goderSynced = computed(() => Number(aiStatus.value?.goder?.aiSyncedUsers) || 0)
// 状态行文案参数：model 保持原实现——配置模型优先、回落状态接口模型，两者皆空
// 时传 undefined（t() 会渲染字面 "undefined"，不静默兜底空串）；i18n 参数类型
// 不含 undefined，故按原始行为整包断言透传
const okLineParams = computed(() => ({
  model: aiCfg.value.model || aiStatus.value?.model,
  provider: aiStatus.value?.goder?.aiProvider || 'OpenOS',
}) as Record<string, string>)

async function loadAI() {
  try {
    aiStatus.value = await api.get<AiStatus>('/api/ai/status')
  } catch { /* ignore */ }
  if (!isAdmin) return
  try {
    const c = await api.get<{ baseUrl: string; apiKey: string; model: string; configured: boolean }>('/api/ai/config')
    aiCfg.value = { baseUrl: c.baseUrl, apiKey: c.apiKey, model: c.model }
    aiConfigured.value = c.configured
  } catch { /* ignore */ }
}
async function saveAI() {
  aiMsg.value = ''
  try {
    await api.put('/api/ai/config', aiCfg.value)
    aiConfigured.value = true
    aiMsg.value = t('settings.ai.saved')
    await loadAI()
  } catch (e: any) { aiMsg.value = e.message }
}
async function testAI() {
  aiTesting.value = true
  aiTestResult.value = null
  try {
    aiTestResult.value = await api.post<AiTestResult>('/api/ai/test')
  } catch (e: any) {
    aiTestResult.value = { ok: false, error: e.message }
  } finally {
    aiTesting.value = false
  }
}

onMounted(() => { void loadAI() }) // 原在设置打开时加载；随分区挂载迁移（每次进入刷新一次状态）
</script>

<template>
  <!-- goder 未启用：全员提示找管理员，管理员另给部署指引 -->
  <div v-if="goderDisabled || goderUnavailable" class="card">
    <div class="group">
      <div class="g-text">
        <div class="g-title">{{ t('settings.ai.notEnabled') }}</div>
        <div class="g-sub">{{ t('settings.ai.notEnabledSub') }}</div>
      </div>
    </div>
    <div v-if="isAdmin" class="group">
      <div class="g-text">
        <div class="g-title">{{ t('settings.ai.adminGuide') }}</div>
        <div class="g-sub g-code">
          {{ goderDisabled ? t('settings.ai.goderOff') : t('settings.ai.noBinary') }}
        </div>
      </div>
    </div>
  </div>

  <template v-else>
    <h2 class="sec">{{ t('settings.ai.title') }}</h2>
    <div class="card">
      <div class="group">
        <div class="g-text">
          <div class="g-title">{{ t('settings.ai.status') }}</div>
          <div class="g-sub">
            <template v-if="aiConfigured">
              {{ t('settings.ai.okLine', okLineParams) }}<template v-if="goderSynced">{{ t('settings.ai.okSync', { n: goderSynced }) }}</template>
            </template>
            <template v-else>{{ t('settings.ai.off') }}</template>
            <template v-if="goderIdle">{{ t('settings.ai.idle') }}</template>
          </div>
        </div>
      </div>
      <div v-if="!isAdmin" class="group">
        <div class="g-text">
          <div class="g-title">{{ t('settings.ai.managed') }}</div>
          <div class="g-sub">{{ t('settings.ai.managedSub') }}</div>
        </div>
      </div>
    </div>

    <OsPanel v-if="isAdmin">
      <div class="form">
        <div class="ai-status" :class="{ on: aiConfigured }">
          {{ aiConfigured ? t('settings.ai.bannerOk') : t('settings.ai.bannerOff') }}
        </div>
        <div class="two">
          <div>
            <label>Base URL</label>
            <OsInput v-model="aiCfg.baseUrl" placeholder="https://open.bigmodel.cn/api/paas/v4" mono />
          </div>
          <div>
            <label>{{ t('settings.ai.model') }}</label>
            <OsInput v-model="aiCfg.model" placeholder="glm-4-flash" mono />
          </div>
        </div>
        <label>{{ t('settings.ai.key') }}</label>
        <OsInput v-model="aiCfg.apiKey" type="password" placeholder="sk-…" mono />
        <div class="row-actions">
          <OsButton variant="primary" @click="saveAI">{{ t('settings.ai.save') }}</OsButton>
          <OsButton :disabled="aiTesting" @click="testAI">{{ aiTesting ? t('settings.ai.testing') : t('settings.ai.test') }}</OsButton>
          <span class="msg">{{ aiMsg }}</span>
        </div>
        <div v-if="aiTestResult" class="test-result" :class="{ ok: aiTestResult.ok }">
          <template v-if="aiTestResult.ok">
            {{ t('settings.ai.testOk', { ms: aiTestResult.latencyMs, model: aiTestResult.model, reply: aiTestResult.reply }) }}
          </template>
          <template v-else>✕ {{ aiTestResult.error }}</template>
        </div>
        <p class="hint">{{ t('settings.ai.hint') }}</p>
      </div>
    </OsPanel>
  </template>
</template>

<style scoped>
.sec { font-size: 13px; font-weight: 700; color: var(--os-text); margin: 4px 0 7px; }
.sec:not(:first-child) { margin-top: 18px; }
.card {
  background: var(--os-panel-solid); border: 1px solid var(--os-border);
  border-radius: 10px; overflow: hidden;
}
.card .group { display: flex; align-items: center; gap: 14px; padding: 11px 14px; min-height: 44px; }
.card .group + .group { border-top: 1px solid var(--os-border); }
.g-text { flex: 1; min-width: 0; }
.g-title { font-size: 13px; }
.g-sub { font-size: 11.5px; color: var(--os-text-secondary); margin-top: 1px; }
.g-sub.g-code { font-family: var(--os-mono); word-break: break-all; }
.form { display: flex; flex-direction: column; gap: 8px; }
.form label { font-size: 12px; color: var(--os-text-secondary); display: flex; gap: 8px; align-items: center; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
.msg { font-size: 12px; margin-left: 8px; color: var(--os-text-secondary); }
.hint { font-size: 12px; color: var(--os-text-secondary); margin-top: 10px; }
.ai-status { font-size: 13px; color: #b25b00; background: rgba(255,159,10,.12); padding: 8px 12px; border-radius: 8px; }
.ai-status.on { color: #1d8a3e; background: rgba(48,209,88,.12); }
.row-actions { display: flex; gap: 8px; align-items: center; }
.test-result { font-size: 12.5px; color: #e5484d; background: rgba(229,72,77,.08); padding: 8px 12px; border-radius: 8px; }
.test-result.ok { color: #1d8a3e; background: rgba(48,209,88,.1); }
</style>
