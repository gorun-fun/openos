<script setup lang="ts">
// 反馈应用：全员提交意见/问题，管理员查看全部并处理（状态流转 + 回复）。
// 数据走 REST（/api/feedback），实时性走总线 sys:feedback 主题（他人提交/处理
// 即时刷新列表；自己发起的操作直接刷新——总线发布不回声发布者）。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { api } from '../../api/client'
import { bus } from '../../bus/bus'
import { OsEmpty, OsSelect, toast } from '../../ui'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()
const isAdmin = computed(() => session.user?.role === 'admin')

// 类别/状态词表（与服务端 main.go 白名单一致）
const CATEGORIES: Record<string, string> = {
  suggest: '功能建议', issue: '问题报告', experience: '体验反馈', other: '其他',
}
const STATUSES: Record<string, string> = {
  pending: '待处理', processing: '处理中', resolved: '已解决', closed: '已关闭',
}
const statusOptions = Object.entries(STATUSES).map(([value, label]) => ({ value, label }))

interface FeedbackItem {
  id: number
  username: string
  category: string
  content: string
  status: string
  reply: string
  createdAt: string
  updatedAt: string
}

const list = ref<FeedbackItem[]>([])
const loading = ref(false)
// admin 的「全部 / 我的」视图（普通用户恒为本人列表，服务端同判定）
const viewAll = ref(true)
const selectedId = ref<number | null>(null)
// 右栏形态：null = 详情；'compose' = 提交表单
const compose = ref(false)

const selected = computed(() => list.value.find((f) => f.id === selectedId.value) || null)
const me = () => session.user?.username || ''
const catName = (c: string) => CATEGORIES[c] || '其他'
const stName = (s: string) => STATUSES[s] || s

// SQLite datetime('now') 是 UTC 文本（YYYY-MM-DD HH:MM:SS），补 T/Z 才能按
// 本地时区正确解析（直接 new Date 会当本地时间，差一个时区）。
function parseUTC(s: string): Date {
  return new Date(s.trim().replace(' ', 'T') + 'Z')
}
function fmtTime(s: string): string {
  const t = parseUTC(s)
  if (Number.isNaN(t.getTime())) return s
  const hm = `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
  if (t.toDateString() === new Date().toDateString()) return `今天 ${hm}`
  return `${t.getMonth() + 1}月${t.getDate()}日 ${hm}`
}

async function load() {
  loading.value = true
  try {
    const mine = isAdmin.value && !viewAll.value
    list.value = await api.get<FeedbackItem[]>(`/api/feedback${mine ? '?mine=1' : ''}`)
    // 选中项被列表刷新淘汰时清空详情
    if (selectedId.value != null && !list.value.some((f) => f.id === selectedId.value)) {
      selectedId.value = null
    }
  } catch (e: any) {
    toast(e?.message || '加载失败', { type: 'error' })
  } finally {
    loading.value = false
  }
}

// 他人提交/处理 → 刷新（自己的操作在对应动作里直接刷新）
const offBus = bus.on('sys:feedback', () => { void load() })

onMounted(() => {
  windows.setTitle(props.win.id, '反馈')
  void load()
})
onBeforeUnmount(offBus)

function openItem(f: FeedbackItem) {
  compose.value = false
  selectedId.value = f.id
  editStatus.value = f.status
  editReply.value = f.reply
}

function startCompose() {
  selectedId.value = null
  compose.value = true
  draftCategory.value = 'suggest'
  draftContent.value = ''
}

// ---- 提交 ----
const draftCategory = ref('suggest')
const draftContent = ref('')
const submitting = ref(false)

async function submit() {
  const content = draftContent.value.trim()
  if (content.length < 5) { toast('内容太短了，多说两句吧（至少 5 个字）', { type: 'error' }); return }
  if (content.length > 4000) { toast('内容过长（上限 4000 字）', { type: 'error' }); return }
  submitting.value = true
  try {
    const r = await api.post<{ id: number }>('/api/feedback', { category: draftCategory.value, content })
    toast('反馈已提交，感谢你的意见！', { type: 'success' })
    compose.value = false
    await load()
    selectedId.value = r.id
  } catch (e: any) {
    toast(e?.message || '提交失败，请稍后再试', { type: 'error' })
  } finally {
    submitting.value = false
  }
}

// ---- 管理员处理 ----
const editStatus = ref('pending')
const editReply = ref('')
const saving = ref(false)

async function saveHandle() {
  const f = selected.value
  if (!f) return
  saving.value = true
  try {
    await api.put(`/api/feedback/${f.id}`, { status: editStatus.value, reply: editReply.value.trim() })
    toast('处理结果已保存', { type: 'success' })
    await load()
    selectedId.value = f.id
  } catch (e: any) {
    toast(e?.message || '保存失败', { type: 'error' })
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="fb-app">
    <!-- 融合窗体工具栏：整条可拖动窗口（data-os-drag 事件委托，控件自身不参与） -->
    <div class="os-tb fb-bar" data-os-drag>
      <div class="tb-title">反馈</div>
      <template v-if="isAdmin">
        <span class="seg">
          <button class="seg-btn" :class="{ on: viewAll }" @click="viewAll = true; selectedId = null; load()">全部</button>
          <button class="seg-btn" :class="{ on: !viewAll }" @click="viewAll = false; selectedId = null; load()">我的</button>
        </span>
      </template>
      <span v-else class="hint">你的意见会直达管理员</span>
      <span class="grow" />
      <button class="os-tb-btn primary" title="提交反馈" @click="startCompose">＋ 提交反馈</button>
    </div>

    <div class="fb-body">
      <!-- 左栏：反馈列表 -->
      <aside class="fb-list">
        <div v-if="!list.length && !loading" class="empty-wrap">
          <OsEmpty :text="isAdmin && viewAll ? '还没有收到反馈' : '还没有提交过反馈，点右上角「提交反馈」说两句'" />
        </div>
        <button
          v-for="f in list" :key="f.id"
          class="item" :class="{ on: f.id === selectedId && !compose }"
          @click="openItem(f)"
        >
          <div class="row1">
            <span class="cat" :data-cat="f.category">{{ catName(f.category) }}</span>
            <span class="st" :data-st="f.status">{{ stName(f.status) }}</span>
          </div>
          <div class="excerpt">{{ f.content }}</div>
          <div class="meta">
            <span v-if="isAdmin && viewAll" class="who">{{ f.username }}</span>
            <span class="time">{{ fmtTime(f.createdAt) }}</span>
            <span v-if="f.reply" class="replied">已回复</span>
          </div>
        </button>
      </aside>

      <!-- 右栏：详情 / 提交表单 -->
      <section class="fb-detail">
        <!-- 提交表单 -->
        <div v-if="compose" class="pane compose">
          <h2>提交反馈</h2>
          <div class="cats">
            <button
              v-for="(name, key) in CATEGORIES" :key="key"
              class="cat-chip" :class="{ on: draftCategory === key }"
              @click="draftCategory = key"
            >{{ name }}</button>
          </div>
          <textarea
            v-model="draftContent"
            class="os-flat content-input"
            placeholder="遇到的问题、想要的功能、改进建议……（5~4000 字）"
            spellcheck="false"
          />
          <div class="compose-foot">
            <span class="count">{{ draftContent.length }}/4000</span>
            <span class="grow" />
            <button class="btn" @click="compose = false">取消</button>
            <button class="btn primary" :disabled="submitting" @click="submit">{{ submitting ? '提交中…' : '提交' }}</button>
          </div>
        </div>

        <!-- 详情 -->
        <div v-else-if="selected" class="pane">
          <header class="d-head">
            <span class="cat big" :data-cat="selected.category">{{ catName(selected.category) }}</span>
            <span class="st big" :data-st="selected.status">{{ stName(selected.status) }}</span>
            <span class="grow" />
            <span class="d-meta">#{{ selected.id }} · {{ isAdmin && viewAll ? selected.username + ' · ' : '' }}{{ fmtTime(selected.createdAt) }}</span>
          </header>
          <div class="d-content">{{ selected.content }}</div>

          <!-- 管理员回复（只读展示） -->
          <div v-if="selected.reply" class="d-reply">
            <div class="d-reply-title">管理员回复</div>
            <div class="d-reply-body">{{ selected.reply }}</div>
          </div>

          <!-- 管理员处理区 -->
          <div v-if="isAdmin" class="d-handle">
            <div class="d-handle-row">
              <span class="lbl">状态</span>
              <OsSelect
                :model-value="editStatus" :options="statusOptions"
                @update:model-value="(v: any) => editStatus = String(v)"
              />
            </div>
            <textarea
              v-model="editReply"
              class="os-flat reply-input"
              placeholder="回复提交者（可选，保存后对方会收到通知）"
              spellcheck="false"
            />
            <div class="d-handle-foot">
              <span class="grow" />
              <button class="btn primary" :disabled="saving" @click="saveHandle">{{ saving ? '保存中…' : '保存处理结果' }}</button>
            </div>
          </div>
        </div>

        <!-- 空态 -->
        <div v-else class="empty-wrap">
          <OsEmpty text="选择左侧一条反馈查看详情，或提交新反馈" />
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.fb-app { width: 100%; height: 100%; display: flex; flex-direction: column; background: var(--os-window); overflow: hidden; }
.fb-bar .tb-title { font-size: 13.5px; font-weight: 700; }
.fb-bar .hint { font-size: 12px; color: var(--os-text-secondary); white-space: nowrap; }
.fb-bar .primary { background: var(--os-accent); border-color: var(--os-accent); color: #fff; font-weight: 600; }
.fb-bar .primary:hover { background: var(--os-accent-hover); }
.grow { flex: 1; }

.seg { display: inline-flex; margin-left: 6px; background: var(--os-input-bg); border: 1px solid var(--os-border-strong); border-radius: 8px; overflow: hidden; }
.seg-btn { border: none; background: transparent; color: var(--os-text-secondary); font-size: 12px; padding: 4px 12px; cursor: pointer; }
.seg-btn.on { background: var(--os-accent); color: #fff; }

.fb-body { flex: 1; display: flex; min-height: 0; }
.fb-list {
  width: 300px; flex: none; overflow-y: auto; padding: 10px;
  display: flex; flex-direction: column; gap: 8px;
  border-right: 1px solid var(--os-border, rgba(0, 0, 0, .08));
  background: var(--os-panel, rgba(0, 0, 0, .02));
}
.item {
  text-align: left; border: 1px solid var(--os-border, rgba(0, 0, 0, .08)); border-radius: 10px;
  background: var(--os-window); padding: 10px 12px; cursor: pointer; display: flex; flex-direction: column; gap: 6px;
}
.item:hover { border-color: var(--os-accent); }
.item.on { border-color: var(--os-accent); box-shadow: 0 0 0 2px color-mix(in srgb, var(--os-accent) 25%, transparent); }
.row1 { display: flex; align-items: center; gap: 6px; }
.cat { font-size: 11px; font-weight: 600; padding: 1.5px 8px; border-radius: 10px; background: color-mix(in srgb, var(--os-accent) 15%, transparent); color: var(--os-accent); }
.cat[data-cat="issue"] { background: rgba(255, 159, 10, .16); color: #c77700; }
.cat[data-cat="other"] { background: rgba(142, 142, 147, .16); color: #6f6f76; }
.st { font-size: 11px; padding: 1.5px 8px; border-radius: 10px; background: rgba(142, 142, 147, .16); color: #6f6f76; }
.st[data-st="pending"] { background: rgba(255, 159, 10, .16); color: #c77700; }
.st[data-st="processing"] { background: rgba(0, 122, 255, .14); color: #0a6ceb; }
.st[data-st="resolved"] { background: rgba(52, 199, 89, .16); color: #1e9e45; }
.excerpt {
  font-size: 12.5px; line-height: 1.5; color: var(--os-text);
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  white-space: pre-wrap; word-break: break-word;
}
.meta { display: flex; align-items: center; gap: 8px; font-size: 11px; color: var(--os-text-secondary); }
.meta .grow { flex: 1; }
.who { max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.replied { color: #1e9e45; }

.fb-detail { flex: 1; min-width: 0; overflow-y: auto; padding: 16px; display: flex; flex-direction: column; }
.pane { display: flex; flex-direction: column; gap: 14px; max-width: 720px; margin: 0 auto; width: 100%; }
.pane h2 { font-size: 16px; margin: 0; }
.empty-wrap { flex: 1; display: flex; align-items: center; justify-content: center; }

.cats { display: flex; flex-wrap: wrap; gap: 8px; }
.cat-chip {
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text-secondary);
  font-size: 12.5px; padding: 5px 14px; border-radius: 14px; cursor: pointer;
}
.cat-chip.on { background: var(--os-accent); border-color: var(--os-accent); color: #fff; }

.content-input {
  flex: 1; min-height: 220px; resize: none; outline: none;
  border: 1px solid var(--os-border-strong); border-radius: 10px; background: var(--os-input-bg);
  color: var(--os-text); font-size: 13.5px; line-height: 1.8; font-family: inherit; padding: 12px 14px;
}
.compose { flex: 1; }
.compose-foot { display: flex; align-items: center; gap: 8px; }
.count { font-size: 11.5px; color: var(--os-text-secondary); }

.btn {
  border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text);
  font-size: 12.5px; padding: 6px 16px; border-radius: 8px; cursor: pointer;
}
.btn.primary { background: var(--os-accent); border-color: var(--os-accent); color: #fff; font-weight: 600; }
.btn.primary:hover:not(:disabled) { background: var(--os-accent-hover); }
.btn:disabled { opacity: .55; cursor: default; }

.d-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.cat.big, .st.big { font-size: 12px; padding: 3px 10px; }
.d-meta { font-size: 11.5px; color: var(--os-text-secondary); }
.d-content {
  font-size: 13.5px; line-height: 1.9; white-space: pre-wrap; word-break: break-word;
  padding: 14px 16px; border: 1px solid var(--os-border, rgba(0, 0, 0, .08)); border-radius: 10px;
  background: var(--os-panel, rgba(0, 0, 0, .02));
}
.d-reply { border-left: 3px solid var(--os-accent); padding: 2px 0 2px 12px; }
.d-reply-title { font-size: 12px; font-weight: 700; color: var(--os-accent); margin-bottom: 4px; }
.d-reply-body { font-size: 13px; line-height: 1.8; white-space: pre-wrap; word-break: break-word; color: var(--os-text); }

.d-handle {
  display: flex; flex-direction: column; gap: 10px;
  border-top: 1px dashed var(--os-border-strong); padding-top: 14px;
}
.d-handle-row { display: flex; align-items: center; gap: 10px; }
.d-handle-row .lbl { font-size: 12.5px; color: var(--os-text-secondary); flex: none; }
.reply-input {
  min-height: 90px; resize: vertical; outline: none;
  border: 1px solid var(--os-border-strong); border-radius: 10px; background: var(--os-input-bg);
  color: var(--os-text); font-size: 13px; line-height: 1.7; font-family: inherit; padding: 10px 12px;
}
.d-handle-foot { display: flex; align-items: center; }
</style>
