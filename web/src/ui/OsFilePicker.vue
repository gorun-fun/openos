<script setup lang="ts">
// macOS 风格「打开/存储」文件对话框：以窗口内 Sheet 形式从顶部滑出。
// open 模式：浏览 VFS 选文件；save 模式：选目录 + 输文件名，重名时二次确认覆盖。
import { computed, onMounted, ref, watch } from 'vue'
import { useSession } from '../stores/session'
import { fsApi, type VFile } from '../api/client'
import { fileEmoji, fmtSize, extOf } from '../apps/openWith'
import OsButton from './OsButton.vue'
import OsInput from './OsInput.vue'

const props = withDefaults(defineProps<{
  mode: 'open' | 'save'
  title?: string
  defaultDir?: string
  defaultName?: string
  /** open 模式按扩展名过滤（小写，不含点）；缺省不过滤 */
  exts?: string[]
}>(), {})
const emit = defineEmits<{ confirm: [path: string]; cancel: [] }>()

const session = useSession()
const home = `/home/${session.user!.username}`
const cwd = ref(props.defaultDir || home)
const entries = ref<VFile[]>([])
const loading = ref(true)
const sel = ref('') // 选中的文件路径（open 模式）
const name = ref(props.defaultName || '')
const overwrite = ref(false) // save 模式：目标已存在，等待二次确认
const mkdirOpen = ref(false)
const mkdirName = ref('')
const homeDirs = ref<VFile[]>([])

const shown = computed(() => {
  const list = entries.value.filter((f) => !f.path.split('/').pop()!.startsWith('.'))
  if (props.mode !== 'open' || !props.exts?.length) return list
  return list.filter((f) => f.isDir || props.exts!.includes(extOf(f.path)))
})

async function load() {
  loading.value = true
  overwrite.value = false
  try { entries.value = await fsApi.list(cwd.value) } catch { entries.value = [] }
  finally { loading.value = false }
}
async function loadSide() {
  try { homeDirs.value = (await fsApi.list(home)).filter((f) => f.isDir && !f.path.split('/').pop()!.startsWith('.')) } catch { homeDirs.value = [] }
}
onMounted(() => { load(); loadSide() })
watch(cwd, () => { sel.value = ''; load() })

function enter(f: VFile) {
  if (f.isDir) { cwd.value = f.path; return }
  if (props.mode === 'open') { sel.value = f.path; name.value = f.path.split('/').pop() || '' }
  else name.value = f.path.split('/').pop() || ''
}
function pick(f: VFile) {
  if (f.isDir) { cwd.value = f.path; return }
  sel.value = f.path
  name.value = f.path.split('/').pop() || ''
  overwrite.value = false
}
function dblPick(f: VFile) {
  if (f.isDir) { cwd.value = f.path; return }
  if (props.mode === 'open') { emit('confirm', f.path); return }
  pick(f)
  confirm()
}

const upDir = computed(() => cwd.value.split('/').slice(0, -1).join('/') || home)
const crumbs = computed(() => {
  const parts = cwd.value.replace(/^\/home\/[^/]+/, '~').split('/').filter(Boolean)
  const out: { label: string; path: string }[] = []
  let acc = cwd.value.startsWith(home) ? home : ''
  const list = cwd.value.startsWith(home)
    ? ['~', ...cwd.value.slice(home.length + 1).split('/').filter(Boolean)]
    : parts
  for (const seg of list) {
    acc = seg === '~' ? home : acc + '/' + seg
    out.push({ label: seg === '~' ? '主页' : seg, path: acc })
  }
  return out
})

const target = computed(() => (props.mode === 'save' ? (cwd.value.replace(/\/$/, '') + '/' + name.value.trim()) : sel.value))
const exists = computed(() => {
  if (props.mode !== 'save' || !name.value.trim()) return false
  return entries.value.some((f) => !f.isDir && f.path === target.value)
})
const canConfirm = computed(() => props.mode === 'save' ? !!name.value.trim() && name.value !== '.' && name.value !== '..' : !!sel.value)

function confirm() {
  if (!canConfirm.value) return
  // save 模式重名：第一次点变更为「替换」，再点才真正覆盖（macOS 行为）
  if (props.mode === 'save' && exists.value && !overwrite.value) { overwrite.value = true; return }
  emit('confirm', target.value)
}

async function doMkdir() {
  const n = mkdirName.value.trim()
  if (!n) { mkdirOpen.value = false; return }
  try { await fsApi.mkdir(cwd.value + '/' + n) } catch { /* 忽略 */ }
  mkdirName.value = ''
  mkdirOpen.value = false
  load()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter') { e.preventDefault(); confirm() }
  if (e.key === 'Escape') { e.preventDefault(); emit('cancel') }
}
</script>

<template>
  <div class="fp-mask" @pointerdown.self="emit('cancel')">
    <div class="fp" @keydown="onKey">
      <header class="fp-head">
        <span class="fp-title">{{ title || (mode === 'open' ? '打开' : '存储为') }}</span>
        <nav class="crumbs">
          <button v-for="c in crumbs" :key="c.path" @click="cwd = c.path">{{ c.label }}</button>
          <span class="crumb-sep">/</span>
        </nav>
        <span class="grow" />
        <button class="fp-mini" title="上一级" @click="cwd = upDir">‹</button>
        <button class="fp-mini" title="新建文件夹" @click="mkdirOpen = !mkdirOpen">新建文件夹</button>
      </header>

      <div v-if="mkdirOpen" class="mkdir">
        <OsInput v-model="mkdirName" placeholder="文件夹名称" style="flex: 1" @keydown.enter.prevent="doMkdir" />
        <OsButton size="sm" @click="doMkdir">创建</OsButton>
      </div>

      <div class="fp-body">
        <aside class="fp-side">
          <div class="grp">收藏</div>
          <button :class="{ on: cwd === home }" @click="cwd = home">🏠 主页</button>
          <button
            v-for="d in homeDirs.filter((x) => !x.path.includes('.Trash')).slice(0, 8)" :key="d.path"
            :class="{ on: cwd === d.path }" @click="cwd = d.path"
          >📁 {{ d.path.split('/').pop() }}</button>
        </aside>

        <div class="fp-list">
          <div v-if="loading" class="fp-state">加载中…</div>
          <div v-else-if="!shown.length" class="fp-state">空文件夹</div>
          <template v-else>
            <div class="fp-row fp-row-head">
              <span class="c1">名称</span><span class="c2">大小</span>
            </div>
            <div
              v-for="f in shown" :key="f.path"
              class="fp-row" :class="{ sel: sel === f.path || (mode === 'save' && name === f.path.split('/').pop()) }"
              @click="pick(f)" @dblclick="dblPick(f)"
            >
              <span class="c1"><span class="ic">{{ f.isDir ? '📁' : fileEmoji(f.path) }}</span>{{ f.path.split('/').pop() }}</span>
              <span class="c2">{{ f.isDir ? '—' : fmtSize(f.size) }}</span>
            </div>
          </template>
        </div>
      </div>

      <footer class="fp-foot">
        <template v-if="mode === 'save'">
          <label class="name-lb">存储为：</label>
          <OsInput v-model="name" placeholder="文件名" style="width: 240px" />
          <span v-if="exists" class="warn">{{ overwrite ? '再次点「替换」覆盖同名文件' : '同名文件已存在' }}</span>
        </template>
        <span v-else class="sel-name">{{ sel ? sel.split('/').pop() : '' }}</span>
        <span class="grow" />
        <OsButton size="sm" @click="emit('cancel')">取消</OsButton>
        <OsButton size="sm" variant="primary" :disabled="!canConfirm" @click="confirm">
          {{ mode === 'open' ? '打开' : overwrite ? '替换' : '存储' }}
        </OsButton>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.fp-mask {
  position: absolute; inset: 0; z-index: 120; background: rgba(0, 0, 0, .18);
  display: flex; justify-content: center; align-items: flex-start; padding-top: 6px;
  animation: os-fade-in .13s;
}
/* Sheet：从窗口顶部滑出（macOS 保存面板形态） */
.fp {
  width: min(660px, 96%); height: min(440px, 92%); display: flex; flex-direction: column;
  background: var(--os-panel-solid); border: 1px solid var(--os-border-strong);
  border-radius: 12px; box-shadow: var(--os-shadow); overflow: hidden;
  animation: sheet-in .22s var(--os-ease-spring);
}
@keyframes sheet-in {
  from { opacity: 0; transform: translateY(-16px) scale(.98); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.fp-head {
  flex: none; display: flex; align-items: center; gap: 10px; padding: 10px 14px;
  border-bottom: 1px solid var(--os-border); background: var(--os-sidebar);
}
.fp-title { font-size: 13.5px; font-weight: 700; }
.crumbs { display: flex; align-items: center; gap: 2px; font-size: 12px; color: var(--os-text-secondary); }
.crumbs button { border: none; background: none; cursor: pointer; padding: 2px 5px; border-radius: 5px; color: var(--os-text); font-size: 12px; }
.crumbs button:hover { background: var(--os-hover); }
.crumb-sep { opacity: .5; }
.grow { flex: 1; }
.fp-mini {
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text);
  border-radius: 7px; height: 26px; padding: 0 9px; font-size: 12px; cursor: pointer;
}
.fp-mini:hover { background: var(--os-hover); }
.mkdir { flex: none; display: flex; gap: 8px; padding: 8px 14px; border-bottom: 1px solid var(--os-border); align-items: center; }
.fp-body { flex: 1; display: flex; min-height: 0; }
.fp-side {
  flex: none; width: 148px; overflow-y: auto; padding: 8px; border-right: 1px solid var(--os-border);
  background: var(--os-sidebar); display: flex; flex-direction: column; gap: 1px;
}
.fp-side .grp { font-size: 11px; font-weight: 600; color: var(--os-text-secondary); padding: 4px 8px 3px; }
.fp-side button {
  border: none; background: none; text-align: left; padding: 5px 8px; border-radius: 7px;
  font-size: 12.5px; color: var(--os-text); cursor: pointer; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.fp-side button:hover { background: var(--os-hover); }
.fp-side button.on { background: var(--os-selected); color: var(--os-accent); }
.fp-list { flex: 1; overflow-y: auto; padding: 4px 8px; }
.fp-state { padding: 36px; text-align: center; color: var(--os-text-secondary); font-size: 12.5px; }
.fp-row {
  display: flex; align-items: center; padding: 5px 10px; border-radius: 7px; font-size: 13px; cursor: default;
}
.fp-row .c1 { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; display: flex; align-items: center; gap: 7px; }
.fp-row .c2 { flex: none; width: 72px; text-align: right; font-size: 11.5px; color: var(--os-text-secondary); }
.fp-row .ic { flex: none; font-size: 15px; }
.fp-row-head { color: var(--os-text-secondary); font-size: 11px; font-weight: 600; cursor: default; padding: 6px 10px 3px; }
.fp-row:not(.fp-row-head):hover { background: var(--os-hover); }
.fp-row.sel { background: var(--os-accent); color: #fff; }
.fp-row.sel .c2 { color: rgba(255, 255, 255, .8); }
.fp-foot {
  flex: none; display: flex; align-items: center; gap: 8px; padding: 10px 14px;
  border-top: 1px solid var(--os-border); background: var(--os-sidebar);
}
.name-lb { font-size: 12.5px; color: var(--os-text-secondary); }
.sel-name { font-size: 12.5px; }
.warn { font-size: 11.5px; color: #e5a00d; }
</style>
