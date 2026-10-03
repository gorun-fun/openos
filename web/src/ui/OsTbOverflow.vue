<script setup lang="ts">
// OsTbOverflow：融合工具栏（.os-tb）按钮组的宽度自适应折叠器——桌面窄窗与
// 手机全屏通用。用法：把「空间不足时可以收进更多菜单」的按钮组包一层放进
// .os-tb；可用宽度不够时整组隐藏、原地出现「⋯」，被折叠按钮经弹层展示
// （图标 + title），点击代理回原按钮——不克隆不搬移 DOM，Vue 状态零破坏。
//
// 折叠判定按「固定内容需求」而非 scrollWidth 溢出：工具栏里的弹性元素
// （fx 输入框 / .grow / 标题）会吸收宽度使容器永不溢出，故遍历工具栏子元素
// 求和固定项宽度（跳过 flex 弹性区），弹性区想保留的最小宽度由 minFree 声明。
import { computed, onBeforeUnmount, onMounted, onUpdated, ref } from 'vue'

const props = withDefaults(defineProps<{ minFree?: number }>(), { minFree: 0 })

const groupEl = ref<HTMLElement>()
const anchorEl = ref<HTMLElement>()
const collapsed = ref(false)

const MORE_W = 44 // 「⋯」按钮 + gap 的占位
let ro: ResizeObserver | null = null
let fullW = 0 // 展开态整组宽（缓存：折叠后组 display:none 量不到）
let otherFixed = 0

/** 工具栏内除本组外的固定内容总宽（跳过弹性区，弹性保留量走 minFree）。 */
function fixedWidth(tb: HTMLElement, skip: HTMLElement): number {
  let w = 0, n = 0
  const gap = parseFloat(getComputedStyle(tb).gap) || 0
  for (const el of [...tb.children] as HTMLElement[]) {
    if (el === skip) continue
    const cs = getComputedStyle(el)
    if (cs.display === 'none') continue
    if (parseFloat(cs.flexGrow) > 0 || el.classList.contains('grow')) continue
    w += el.offsetWidth + parseFloat(cs.marginLeft) + parseFloat(cs.marginRight)
    n++
  }
  return w + gap * n
}

function recheck() {
  const g = groupEl.value
  const tb = g?.closest<HTMLElement>('.os-tb')
  if (!g || !tb) return
  if (!collapsed.value) fullW = Math.max(fullW, g.offsetWidth)
  otherFixed = fixedWidth(tb, g)
  collapsed.value = tb.clientWidth < otherFixed + fullW + MORE_W + props.minFree
}

onMounted(() => {
  recheck()
  const tb = groupEl.value?.closest('.os-tb')
  if (tb && 'ResizeObserver' in window) {
    ro = new ResizeObserver(() => recheck())
    ro.observe(tb)
  }
  document.addEventListener('pointerdown', onDocDown, true)
  document.addEventListener('wheel', closeMenu, { passive: true, capture: true })
})
onUpdated(recheck) // 按钮组内容变化（v-if 显隐）后重测展开态宽度
onBeforeUnmount(() => {
  ro?.disconnect()
  document.removeEventListener('pointerdown', onDocDown, true)
  document.removeEventListener('wheel', closeMenu, true)
})

// —— 弹层菜单（Teleport 到 body：.os-tb overflow:hidden 会裁剪内部弹层）——
const menuOpen = ref(false)
const menuPos = ref({ x: 0, y: 0 })

type Item = { label: string; icon: string; disabled: boolean; active: boolean; run: () => void }
const items = computed<Item[]>(() => {
  const g = groupEl.value
  if (!g || !collapsed.value) return []
  return [...g.querySelectorAll('button')]
    // 跳过组内自带的弹出菜单按钮（如文件应用排序钮的 sort-menu，title 缺失且非工具栏动作）
    .filter((b) => !b.closest('.os-menu'))
    .map((b) => {
      const btn = b as HTMLButtonElement
      return {
        label: (btn.title || btn.textContent.trim() || '').slice(0, 24),
        icon: btn.querySelector('svg')?.outerHTML || '',
        disabled: btn.disabled,
        active: btn.classList.contains('on') || btn.classList.contains('active'),
        run: () => btn.click(),
      }
    })
    .filter((i) => i.label)
})

function toggleMore() {
  if (menuOpen.value) { closeMenu(); return }
  const r = anchorEl.value!.getBoundingClientRect()
  const h = 46 + items.value.length * 32
  menuPos.value = {
    x: Math.max(6, Math.min(r.left, window.innerWidth - 216)),
    y: Math.min(r.bottom + 4, window.innerHeight - h - 6),
  }
  menuOpen.value = true
}
function closeMenu() { menuOpen.value = false }
function onDocDown(e: PointerEvent) {
  if (!menuOpen.value) return
  const t = e.target as HTMLElement
  if (t.closest('[data-os-tb-more]') || t.closest('.tb-overflow-menu')) return
  closeMenu()
}
</script>

<template>
  <!-- 展开态：slot 原样参与 flex；折叠态整组隐藏（宽度测量靠展开态缓存） -->
  <div ref="groupEl" class="tb-overflow-group" :class="{ collapsed }"><slot /></div>
  <button
    v-if="collapsed"
    ref="anchorEl" class="os-tb-btn" data-os-tb-more title="更多"
    @click="toggleMore"
  >
    <svg viewBox="0 0 20 20" width="15" height="15"><circle cx="4.6" cy="10" r="1.7" fill="currentColor"/><circle cx="10" cy="10" r="1.7" fill="currentColor"/><circle cx="15.4" cy="10" r="1.7" fill="currentColor"/></svg>
  </button>
  <Teleport to="body">
    <div v-if="menuOpen" class="os-menu tb-overflow-menu" :style="{ left: menuPos.x + 'px', top: menuPos.y + 'px' }">
      <button v-for="(it, i) in items" :key="i" :disabled="it.disabled" :class="{ on: it.active }" @click="it.run(); closeMenu()">
        <span class="ic" v-html="it.icon" />{{ it.label }}
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
.tb-overflow-group { display: flex; align-items: center; gap: 4px; flex: none; }
.tb-overflow-group.collapsed { display: none; }
.tb-overflow-menu button.on { color: var(--os-accent); font-weight: 600; }
.tb-overflow-menu .ic { width: 18px; height: 18px; display: inline-flex; align-items: center; justify-content: center; flex: none; }
</style>
