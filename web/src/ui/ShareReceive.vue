<script setup lang="ts">
// 分享接收弹窗：打开 /#share=<id> 链接后展示文件元信息，提供「存入我的文档 /
// 下载」。params.id 为分享 id；关闭/取消不带结果。
import { onMounted, ref } from 'vue'
import { api, getToken, type ApiError } from '../api/client'
import { toast } from './toast'
import { t } from '../i18n'

const props = defineProps<{ params: { id: string }; close: (v?: any) => void; cancel: () => void }>()

interface ShareMeta { path: string; name: string; owner: string; size: number }

const meta = ref<ShareMeta | null>(null)
const err = ref('')
const saving = ref(false)

onMounted(async () => {
  try {
    meta.value = await api.get<ShareMeta>(`/api/share/${encodeURIComponent(props.params.id)}`)
  } catch (e) {
    err.value = (e as ApiError).message || t('share.invalid')
  }
})

function fmtSize(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(1) + ' MB'
}

async function saveToDocs() {
  if (!meta.value || saving.value) return
  saving.value = true
  try {
    const r = await api.post<{ path: string }>(`/api/share/${encodeURIComponent(props.params.id)}/save`)
    toast(t('share.saved', { name: r.path.split('/').pop() || '' }), { type: 'success' })
    props.close({ saved: r.path })
  } catch (e) {
    toast((e as ApiError).message || '保存失败', { type: 'error' })
  } finally {
    saving.value = false
  }
}

async function download() {
  if (!meta.value) return
  try {
    const resp = await fetch(`/api/share/${encodeURIComponent(props.params.id)}/download`, {
      headers: { Authorization: 'Bearer ' + getToken() },
    })
    if (!resp.ok) throw new Error('下载失败')
    const blob = await resp.blob()
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = meta.value.name
    a.click()
    URL.revokeObjectURL(a.href)
  } catch (e: any) {
    toast(e.message || '下载失败', { type: 'error' })
  }
}
</script>

<template>
  <div class="share-receive">
    <template v-if="meta">
      <div class="sr-icon">🔗</div>
      <div class="sr-name" :title="meta.path">{{ meta.name }}</div>
      <div class="sr-meta">{{ t('share.from', { owner: meta.owner, size: fmtSize(meta.size) }) }}</div>
      <div class="sr-btns">
        <button class="primary" :disabled="saving" @click="saveToDocs">{{ saving ? t('share.saving') : t('share.save') }}</button>
        <button @click="download">{{ t('share.download') }}</button>
        <button class="ghost" @click="cancel()">{{ t('common.close') }}</button>
      </div>
    </template>
    <template v-else-if="err">
      <div class="sr-icon">⚠️</div>
      <div class="sr-err">{{ err }}</div>
      <div class="sr-btns"><button class="ghost" @click="cancel()">{{ t('common.close') }}</button></div>
    </template>
    <template v-else>
      <div class="sr-icon loading">⏳</div>
      <div class="sr-meta">{{ t('share.loading') }}</div>
    </template>
  </div>
</template>

<style scoped>
.share-receive { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 26px 20px 18px; text-align: center; }
.sr-icon { font-size: 44px; line-height: 1; }
.sr-icon.loading { animation: os-fade-in .4s ease infinite alternate; opacity: .55; }
.sr-name { font-size: 15px; font-weight: 600; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sr-meta { font-size: 12.5px; color: var(--os-text-secondary); }
.sr-err { font-size: 13px; color: var(--os-danger, #d33); }
.sr-btns { display: flex; gap: 8px; margin-top: 8px; }
.sr-btns button {
  padding: 6px 14px; border-radius: 7px; border: 1px solid var(--os-border, #d4d4d8);
  background: var(--os-panel, #fff); font-size: 13px; cursor: pointer;
}
.sr-btns button.primary { background: var(--os-accent, #2b7); border-color: transparent; color: #fff; }
.sr-btns button.primary:disabled { opacity: .6; cursor: default; }
.sr-btns button.ghost { border-color: transparent; color: var(--os-text-secondary); }
</style>
