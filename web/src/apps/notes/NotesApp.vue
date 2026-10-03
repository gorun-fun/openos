<script setup lang="ts">
// 便签应用：多张彩色便签的创建 / 编辑 / 换色 / 删除，可一键钉到桌面成为小组件。
// 数据来自 notes 仓库（与桌面便签小组件实时共享）；钉选通过本地总线事件
// notes:pin / notes:widgets 与桌面小组件层同步（同页各窗口状态一致）。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useNotes, NOTE_COLORS } from '../../stores/notes'
import { bus } from '../../bus/bus'
import { OsEmpty, osConfirm, icons } from '../../ui'

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const notes = useNotes()

onMounted(() => {
  windows.setTitle(props.win.id, '便签')
  notes.load()
  // 查询桌面小组件当前钉选的便签 id（Widgets 应答 notes:widgets）
  bus.emit('notes:query', {}, { remote: false })
})

// ---- 桌面钉选状态 ----
const deskIds = ref(new Set<string>())
function onWidgets(p: any) {
  deskIds.value = new Set(Array.isArray(p?.ids) ? p.ids : [])
}
const offWidgets = bus.on('notes:widgets', onWidgets)
onBeforeUnmount(offWidgets)

const onDesk = (id: string) => deskIds.value.has(id)
function toggleDesk(id: string) {
  bus.emit('notes:pin', { id, on: !onDesk(id) }, { remote: false })
}

function addNote() {
  notes.create()
}

async function delNote(id: string) {
  if (!(await osConfirm({ body: '删除这张便签？', danger: true, confirmText: '删除' }))) return
  if (onDesk(id)) bus.emit('notes:pin', { id, on: false }, { remote: false }) // 同步撤下桌面
  notes.remove(id)
}

const colorOf = (n: { color: number }) => NOTE_COLORS[n.color] || NOTE_COLORS[0]

function fmt(ts: number) {
  const d = Date.now() - ts
  if (d < 60_000) return '刚刚'
  if (d < 3600_000) return Math.floor(d / 60_000) + ' 分钟前'
  const t = new Date(ts)
  const hm = `${String(t.getHours()).padStart(2, '0')}:${String(t.getMinutes()).padStart(2, '0')}`
  if (t.toDateString() === new Date().toDateString()) return hm
  return `${t.getMonth() + 1}月${t.getDate()}日 ${hm}`
}
</script>

<template>
  <div class="notes-app">
    <!-- 融合窗体工具栏：整条可拖动窗口（data-os-drag 事件委托，控件自身不参与） -->
    <div class="os-tb notes-bar" data-os-drag>
      <div class="tb-title">便签</div>
      <span class="count">{{ notes.notes.length }} 张 · 自动保存 · 钉选后桌面同步显示</span>
      <span class="grow" />
      <button class="os-tb-btn primary" title="新建便签" v-html="icons.note" @click="addNote" />
    </div>

    <div v-if="!notes.notes.length" class="empty-wrap">
      <OsEmpty text="还没有便签，点右上角「＋」随手记一条" />
    </div>

    <div v-else class="grid">
      <article
        v-for="n in notes.notes" :key="n.id"
        class="card" :style="{ background: colorOf(n).bg }"
      >
        <textarea
          :value="n.text"
          class="os-flat"
          placeholder="记点什么…（自动保存）"
          spellcheck="false"
          @input="notes.setText(n.id, ($event.target as HTMLTextAreaElement).value)"
        />
        <footer>
          <span class="swatches">
            <button
              v-for="(c, i) in NOTE_COLORS" :key="i"
              class="sw" :class="{ on: n.color === i }" :style="{ background: c.bar }"
              :title="`颜色 ${i + 1}`" @click="notes.setColor(n.id, i)"
            />
          </span>
          <span class="grow" />
          <span class="time" :title="new Date(n.updated).toLocaleString()">{{ fmt(n.updated) }}</span>
          <button class="act pin" :class="{ on: onDesk(n.id) }" @click="toggleDesk(n.id)">
            {{ onDesk(n.id) ? '🖥 已在桌面' : '🖥 钉到桌面' }}
          </button>
          <button class="act del" title="删除便签" @click="delNote(n.id)">✕</button>
        </footer>
      </article>
    </div>
  </div>
</template>

<style scoped>
.notes-app { width: 100%; height: 100%; display: flex; flex-direction: column; background: var(--os-window); overflow: hidden; }
.notes-bar .tb-title { font-size: 13.5px; font-weight: 700; }
.notes-bar .count { font-size: 12px; color: var(--os-text-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.notes-bar .primary { background: var(--os-accent); border-color: var(--os-accent); color: #fff; font-weight: 600; }
.notes-bar .primary:hover { background: var(--os-accent-hover); }
.grow { flex: 1; }
.empty-wrap { flex: 1; display: flex; align-items: center; justify-content: center; }

.grid {
  flex: 1; overflow-y: auto; padding: 16px;
  display: grid; grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  gap: 14px; align-content: start;
}
.card {
  display: flex; flex-direction: column; min-height: 190px;
  border-radius: 14px; padding: 10px 12px 8px;
  border: 1px solid rgba(0, 0, 0, .08);
  box-shadow: 0 4px 14px rgba(0, 0, 0, .12);
}
.card textarea {
  flex: 1; resize: none; border: none; outline: none; background: transparent;
  color: #3a3020; font-size: 13.5px; line-height: 1.7; font-family: inherit; min-height: 120px;
}
.card textarea::placeholder { color: rgba(0, 0, 0, .35); }

footer {
  display: flex; align-items: center; gap: 6px; padding-top: 6px; flex: none;
  border-top: 1px dashed rgba(0, 0, 0, .15);
}
.swatches { display: flex; gap: 5px; }
.sw {
  width: 14px; height: 14px; border-radius: 50%; border: 1px solid rgba(0, 0, 0, .25);
  cursor: pointer; padding: 0;
}
.sw.on { outline: 2px solid rgba(0, 0, 0, .55); outline-offset: 1px; }
.time { font-size: 11px; color: rgba(0, 0, 0, .45); }
.act {
  border: 1px solid rgba(0, 0, 0, .18); background: rgba(255, 255, 255, .35);
  color: #3a3020; font-size: 11.5px; padding: 3px 9px; border-radius: 12px; cursor: pointer;
  white-space: nowrap;
}
.act:hover { background: rgba(255, 255, 255, .6); }
.act.pin.on { background: rgba(0, 0, 0, .65); color: #fff; border-color: transparent; }
.act.del:hover { background: rgba(220, 53, 69, .85); color: #fff; border-color: transparent; }
</style>
