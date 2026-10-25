<script setup lang="ts">
// 设置 — 上报接入（地址 / 密钥 / curl 示例，外部系统故障上报就绪度一页看清）、
// 修复模型选择（token-hub 套餐可用清单）、GitHub Token（issue 同步提限额）。
// 环境变量（GRF_OS_STEWARD_REPORT_KEY / GRF_OS_STEWARD_GITHUB_TOKEN / GRF_OS_STEWARD_MODEL）
// 优先于此处保存的值——被覆盖的项界面只读提示。
import { computed, ref } from 'vue'
import { OsButton, OsInput, OsPanel, OsSelect, OsToggle, toast } from '../../../ui'
import { grf } from '../core'

const st = ref<any>(null)
const model = ref('')
const ghToken = ref('')
const busy = ref(false)
const keyShown = ref(false)

async function load() {
  try {
    st.value = await grf.get<any>('/api/steward/settings')
    model.value = st.value.model || ''
  } catch (e: any) { toast(e.message || '设置加载失败', { type: 'error' }) }
}
load()

const reportURL = computed(() => `${location.origin}/api/v1/steward/report`)
const curlSample = computed(() =>
  `curl -X POST ${reportURL.value} \\
  -H 'Content-Type: application/json' \\
  -H 'X-Report-Key: ${keyShown.value && st.value?.report_key ? st.value.report_key : '<上报密钥>'}' \\
  -d '{"repo":"仓库名","title":"问题标题","body":"现象/复现步骤/期望","reporter":"来源系统"}'`)

async function save() {
  busy.value = true
  try {
    await grf.put<any>('/api/steward/settings', { model: model.value, github_token: ghToken.value.trim() })
    toast('已保存', { type: 'success' })
    ghToken.value = ''
    await load()
  } catch (e: any) { toast(e.message || '保存失败', { type: 'error' }) } finally { busy.value = false }
}

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast(`${what}已复制`, { type: 'success' })
  } catch { toast('复制失败，请手动选择', { type: 'error' }) }
}
</script>

<template>
  <div class="tabpage">
    <div v-if="st" class="panels">
      <OsPanel title="问题上报接入">
        <p class="row">外部系统（如 grf-log 告警、巡检脚本）把问题 POST 到下列端点，带 <code>X-Report-Key</code> 即入列：</p>
        <div class="fr">
          <span class="lb">上报地址</span>
          <input class="in grow mono" readonly :value="reportURL" @click="copy(reportURL, '地址')" />
          <OsButton size="sm" @click="copy(reportURL, '地址')">复制</OsButton>
        </div>
        <div class="fr">
          <span class="lb">上报密钥</span>
          <input class="in grow mono" readonly :type="keyShown ? 'text' : 'password'" :value="st.report_key" @click="keyShown = !keyShown" />
          <OsButton size="sm" @click="keyShown = !keyShown">{{ keyShown ? '隐藏' : '显示' }}</OsButton>
          <OsButton size="sm" @click="copy(st.report_key, '密钥')">复制</OsButton>
        </div>
        <p class="hint">可用 <code>GET /api/v1/steward/report/{id}</code>（同密钥）轮询修复状态。密钥可被环境变量 GRF_OS_STEWARD_REPORT_KEY 覆盖。</p>
        <pre class="code">{{ curlSample }}</pre>
      </OsPanel>

      <OsPanel title="修复引擎">
        <div class="fr">
          <span class="lb">模型</span>
          <OsSelect v-if="st.models?.length" v-model="model" :options="st.models.map((m: string) => ({ value: m, label: m }))" style="flex:1" />
          <span v-else class="hint">{{ st.llm_ready ? '模型清单拉取失败（稍后刷新）' : '未配置 LLM 密钥（GRF_OS_TH_API_KEY / GRF_OS_STEWARD_LLM_API_KEY）' }}</span>
        </div>
        <div class="fr">
          <span class="lb">GitHub</span>
          <input v-model="ghToken" class="in grow" type="password" :placeholder="st.github_token_set ? '已配置，输入新值 = 更换' : 'Personal Access Token（issue 同步提限额/私仓，可空）'" />
        </div>
        <div class="fr stats">
          <span class="hint">gh：{{ st.gh_bin || '未找到（修复会失败）' }}</span>
          <span class="hint">git：{{ st.git_bin }}</span>
          <span class="hint">单次上限 {{ st.timeout_min }} 分钟</span>
          <span class="hint">issue 同步每 {{ st.sync_min }} 分钟</span>
        </div>
        <div class="fr">
          <OsButton variant="primary" size="sm" :disabled="busy" @click="save">保存设置</OsButton>
          <span class="hint">环境变量 GRF_OS_STEWARD_MODEL / GRF_OS_STEWARD_GITHUB_TOKEN 优先生效</span>
        </div>
      </OsPanel>
    </div>
  </div>
</template>

<style scoped>
.tabpage { display: flex; flex-direction: column; gap: 10px; }
.panels { display: flex; flex-direction: column; gap: 12px; max-width: 760px; }
.row { margin: 0 0 8px; font-size: 12.5px; color: var(--os-text-secondary); }
.fr { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.lb { width: 60px; flex: none; font-size: 12.5px; color: var(--os-text-secondary); }
.in { padding: 6px 9px; border-radius: 7px; font-size: 12.5px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); min-width: 0; }
.in[readonly] { cursor: pointer; }
.mono { font-family: ui-monospace, monospace; font-size: 12px; }
.hint { font-size: 11.5px; color: var(--os-text-tertiary); }
.stats { flex-wrap: wrap; gap: 14px; margin-top: 4px; }
code { padding: 1px 6px; border-radius: 5px; background: var(--os-active); font-size: 11.5px; }
pre.code { margin: 4px 0 0; padding: 10px 12px; border-radius: 8px; border: 1px solid var(--os-border); background: var(--os-input-bg); font-size: 11.5px; line-height: 1.6; white-space: pre-wrap; word-break: break-all; }
</style>
