<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { WinState } from '../../stores/windows'
import { useWindows } from '../../stores/windows'
import { useSession } from '../../stores/session'
import { fsApi } from '../../api/client'
import { OsToolbar, OsButton } from '../../ui'

interface Slide { title: string; body: string }

const props = defineProps<{ win: WinState }>()
const windows = useWindows()
const session = useSession()

const slides = ref<Slide[]>([{ title: '标题页', body: '副标题\n演讲者：' + (session.user?.displayName || session.user?.username) }])
const cur = ref(0)
const playing = ref(false)
const fileName = ref((props.win.props?.path || '').split('/').pop() || '未命名演示.slides.json')

const current = computed(() => slides.value[cur.value] || slides.value[0])

onMounted(async () => {
  if (props.win.props?.path) {
    try {
      const data = await fsApi.read(props.win.props.path)
      const parsed = JSON.parse(data.content)
      if (Array.isArray(parsed) && parsed.length) slides.value = parsed
    } catch { /* 新建 */ }
  }
  windows.setTitle(props.win.id, fileName.value + ' — 幻灯片')
})

function addSlide() {
  slides.value.splice(cur.value + 1, 0, { title: '新幻灯片', body: '要点一\n要点二' })
  cur.value++
}
function removeSlide() {
  if (slides.value.length <= 1) return
  slides.value.splice(cur.value, 1)
  cur.value = Math.min(cur.value, slides.value.length - 1)
}
function move(d: number) {
  const i = cur.value + d
  if (i < 0 || i >= slides.value.length) return
  const [s] = slides.value.splice(cur.value, 1)
  slides.value.splice(i, 0, s)
  cur.value = i
}

function onKey(e: KeyboardEvent) {
  if (!playing.value) return
  if (e.key === 'ArrowRight' || e.key === ' ') { cur.value = Math.min(cur.value + 1, slides.value.length - 1); e.preventDefault() }
  else if (e.key === 'ArrowLeft') { cur.value = Math.max(cur.value - 1, 0); e.preventDefault() }
  else if (e.key === 'Escape') playing.value = false
}

async function save() {
  const name = prompt('保存为（.slides.json）', fileName.value)
  if (!name) return
  fileName.value = name.endsWith('.slides.json') ? name : name + '.slides.json'
  const path = `/home/${session.user!.username}/文档/${fileName.value}`
  await fsApi.write(path, JSON.stringify(slides.value, null, 2))
  windows.setTitle(props.win.id, fileName.value + ' — 幻灯片')
}

const bullets = computed(() => current.value.body.split('\n').filter(Boolean))
</script>

<template>
  <div class="slides" tabindex="0" @keydown="onKey">
    <OsToolbar>
      <OsButton size="sm" @click="addSlide">＋ 添加</OsButton>
      <OsButton size="sm" @click="removeSlide">删除</OsButton>
      <OsButton size="sm" @click="move(-1)">上移</OsButton>
      <OsButton size="sm" @click="move(1)">下移</OsButton>
      <span class="sep" />
      <OsButton size="sm" variant="primary" @click="playing = true">▶ 演示</OsButton>
      <OsButton size="sm" @click="save">保存</OsButton>
      <span class="grow" />
      <span class="pageinfo">{{ cur + 1 }} / {{ slides.length }}</span>
    </OsToolbar>

    <div class="main">
      <aside class="thumbs">
        <div
          v-for="(s, i) in slides" :key="i"
          class="thumb" :class="{ cur: i === cur }" @click="cur = i"
        >
          <div class="t-title">{{ s.title }}</div>
          <div class="t-body">{{ s.body.split('\n')[0] }}</div>
          <span class="t-num">{{ i + 1 }}</span>
        </div>
      </aside>

      <div class="stage">
        <div class="canvas">
          <h1>{{ current.title }}</h1>
          <ul>
            <li v-for="(b, i) in bullets" :key="i">{{ b }}</li>
          </ul>
        </div>
        <div class="editor">
          <input v-model="current.title" placeholder="幻灯片标题" class="title-in" />
          <textarea v-model="current.body" placeholder="正文（每行一个要点）" class="body-in" spellcheck="false" />
        </div>
      </div>
    </div>

    <!-- 演示模式 -->
    <div v-if="playing" class="play">
      <div class="play-slide">
        <h1>{{ current.title }}</h1>
        <ul>
          <li v-for="(b, i) in bullets" :key="i">{{ b }}</li>
        </ul>
      </div>
      <div class="play-bar">
        <OsButton size="sm" @click="cur = Math.max(0, cur - 1)">‹</OsButton>
        <span>{{ cur + 1 }} / {{ slides.length }} · 方向键翻页，Esc 退出</span>
        <OsButton size="sm" @click="cur = Math.min(slides.length - 1, cur + 1)">›</OsButton>
        <OsButton size="sm" @click="playing = false">退出</OsButton>
      </div>
    </div>
  </div>
</template>

<style scoped>
.slides { width: 100%; display: flex; flex-direction: column; background: var(--os-window); outline: none; }
.pageinfo { font-size: 12px; color: var(--os-text-secondary); }
.main { flex: 1; display: flex; min-height: 0; }
.thumbs { width: 150px; flex: none; overflow-y: auto; border-right: 1px solid var(--os-border); background: var(--os-sidebar); padding: 8px; display: flex; flex-direction: column; gap: 8px; }
.thumb {
  position: relative; aspect-ratio: 16/9; border-radius: 7px; background: #fff; color: #1d1d1f;
  border: 2px solid transparent; cursor: pointer; padding: 8px; overflow: hidden;
}
.thumb.cur { border-color: var(--os-accent); }
.t-title { font-size: 10px; font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.t-body { font-size: 8px; color: #666; margin-top: 2px; white-space: nowrap; overflow: hidden; }
.t-num { position: absolute; bottom: 2px; right: 5px; font-size: 8px; color: #999; }
.stage { flex: 1; display: flex; flex-direction: column; min-width: 0; padding: 14px; gap: 10px; }
.canvas {
  flex: 1; aspect-ratio: auto; background: #fff; color: #1d1d1f; border-radius: 10px;
  box-shadow: 0 3px 16px rgba(0,0,0,.15); padding: 5% 7%; overflow: auto; min-height: 0;
}
.canvas h1, .play-slide h1 { font-size: clamp(20px, 3.2vw, 34px); margin-bottom: 3%; }
.canvas ul, .play-slide ul { padding-left: 1.4em; line-height: 1.9; font-size: clamp(13px, 1.6vw, 19px); }
.editor { flex: none; display: flex; gap: 8px; }
.title-in { flex: 1; padding: 7px 10px; border-radius: 8px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); font-size: 13px; font-weight: 600; }
.body-in { flex: 2; padding: 7px 10px; border-radius: 8px; border: 1px solid var(--os-border-strong); background: var(--os-input-bg); color: var(--os-text); font-size: 13px; resize: vertical; min-height: 44px; }
.play { position: absolute; inset: 0; z-index: 50; background: #0b0b0e; display: flex; flex-direction: column; }
.play-slide { flex: 1; display: flex; flex-direction: column; justify-content: center; padding: 6% 9%; background: #fff; margin: 18px; border-radius: 12px; overflow: auto; }
.play-bar { flex: none; display: flex; align-items: center; gap: 10px; justify-content: center; padding: 8px; color: rgba(255,255,255,.7); font-size: 12px; }
</style>
