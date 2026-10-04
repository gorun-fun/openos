// menu.ts — 全局菜单单例与右键守卫（右键/下拉菜单统一整改的核心）。
//
// 设计：
// 1. 单例 owner：全系统同一时刻最多显示一个菜单。任何组件经 useMenu(owner)
//    接入，open() 自动顶掉当前菜单，从机制上杜绝"多个菜单同屏"。
// 2. 统一关闭：点菜单外任意处（含其他窗口 / 桌面 / shadow DOM 内）、按 Esc、
//    滚动、窗口缩放，都会关闭当前菜单——由本模块在 document capture 层
//    统一实现，组件不再各自挂监听或铺遮罩。
// 3. 右键守卫：未被应用 stopPropagation 接管的 contextmenu 一律屏蔽浏览器
//    原生菜单；input/textarea/contenteditable 与 [data-os-native-menu] 区域
//    保留原生菜单（编辑操作与宿主逃生口）。
//
// 接入约定：
// - 菜单根元素加 class "os-menu-root"：点在其内部不触发全局关闭；
// - 用 click 切换开合的触发按钮加 "data-os-menu-anchor"：全局层不抢关，
//   交给按钮自身的 toggle 语义，保证"再点一次收起"仍然可用；
// - 组件在自己的 contextmenu 处理器中 preventDefault + stopPropagation 后
//   调 open()，守卫便不再介入（否则守卫会把刚打开的菜单又关掉）。
import { computed, reactive, ref } from 'vue'
import { onKey, KEY_PRIORITY, isTextFieldEvent } from './keys'

/** 当前显示中的菜单 owner（空串 = 无菜单） */
const owner = ref('')
/** 最近一次右键菜单的视口坐标（跟随弹出点；锚定式下拉不使用） */
export const menuPos = ref({ x: 0, y: 0 })

export function openMenu(o: string, x = menuPos.value.x, y = menuPos.value.y) {
  ensureCloseWatch()
  menuPos.value = { x, y }
  owner.value = o
}

/** 关闭菜单；带 owner 时只在该菜单正在显示时关闭（菜单项动作里安全调用） */
export function closeMenu(o?: string) {
  if (!o || owner.value === o) owner.value = ''
}

/**
 * 组件接入入口。owner 用带前缀的稳定字符串区分来源
 * （'desktop' / 'files' / 'menubar' / 'dock-trash' / 'files:sort' …）。
 * 返回 reactive 对象，模板里可直接 menu.isOpen / menu.toggle()。
 */
export function useMenu(o: string) {
  return reactive({
    isOpen: computed(() => owner.value === o),
    open(x?: number, y?: number) { openMenu(o, x, y) },
    close() { closeMenu(o) },
    toggle(x?: number, y?: number) { owner.value === o ? closeMenu(o) : openMenu(o, x, y) },
  })
}

// ---- 统一关闭：惰性注册一次（capture 层保证先于任何组件的 stopPropagation） ----
let watched = false
function ensureCloseWatch() {
  if (watched) return
  watched = true
  // 点在菜单内部或触发按钮上：交给组件自身处理；其余一律关闭
  const inside = (e: Event) => {
    // composedPath 穿透 shadow DOM（goder 等 custom element 内的点击也能正确判定）
    const path = (e.composedPath?.() || []) as EventTarget[]
    for (const t of path) {
      if (!(t instanceof Element)) continue
      if (t.closest('.os-menu-root, [data-os-menu-anchor]')) return true
    }
    return false
  }
  document.addEventListener('pointerdown', (e) => {
    if (owner.value && !inside(e)) closeMenu()
  }, true)
  // 键盘：统一键盘中枢的 menu 层（见 ui/keys.ts）——Esc 关闭、方向键/Home/End
  // 在菜单项间移动焦点（焦点落在按钮上后 Enter/Space 走原生激活，无需接管）
  onKey('capture', KEY_PRIORITY.menu, ({ info, ev }) => {
    if (!owner.value || !ev) return false
    if (info.key === 'Escape') { closeMenu(); return true }
    const root = menuRootEl()
    if (!root) return false
    // 菜单内含输入框（如筛选）时方向键/Tab 留给输入语义
    if (isTextFieldEvent(ev) && root.contains(document.activeElement)) return false
    const items = menuItems(root)
    if (!items.length) return false
    const move = (n: number) => {
      const el = items[(n + items.length) % items.length]
      el.focus()
      el.scrollIntoView({ block: 'nearest' })
      return true
    }
    const idx = items.indexOf(document.activeElement as HTMLElement)
    switch (info.key) {
      case 'ArrowDown': return move(idx < 0 ? 0 : idx + 1)
      case 'ArrowUp': return move(idx < 0 ? items.length - 1 : idx - 1)
      case 'Home': return move(0)
      case 'End': return move(items.length - 1)
      case 'Tab': closeMenu(); return true // 菜单是瞬时浮层，Tab 收起而非跑到菜单后方
    }
    return false
  })
  // 滚动（含内部容器，capture 捕获）与窗口缩放后菜单锚点失效，直接关闭
  document.addEventListener('wheel', () => closeMenu(), { capture: true, passive: true })
  window.addEventListener('resize', () => closeMenu())
}

/** 当前可见的菜单根（菜单均 v-if 渲染，同一时刻至多一个） */
function menuRootEl(): HTMLElement | null {
  for (const el of document.querySelectorAll<HTMLElement>('.os-menu-root')) {
    if (el.getClientRects().length) return el
  }
  return null
}

/** 菜单项（可聚焦的按钮/菜单项角色，跳过禁用项） */
function menuItems(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>('button:not(:disabled), [role="menuitem"]:not(:disabled)')]
}

// ---- 右键守卫：默认屏蔽浏览器原生菜单，应用自定义菜单自行接管 ----
let guarded = false
export function installMenuGuard() {
  if (guarded) return
  guarded = true
  document.addEventListener('contextmenu', (e) => {
    // 走到这里说明没有应用 stopPropagation 接管：先收掉现有菜单
    closeMenu()
    if (nativeMenuWanted(e)) return
    e.preventDefault()
  })
}

/** 可编辑元素保留系统编辑菜单（复制/粘贴/拼写检查）；[data-os-native-menu] 显式放行原生菜单 */
function nativeMenuWanted(e: Event): boolean {
  const path = (e.composedPath?.() || []) as EventTarget[]
  for (const t of path) {
    if (!(t instanceof Element)) continue
    if (t.closest('[data-os-native-menu]')) return true
    if (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA') return true
    if (t instanceof HTMLElement && t.isContentEditable) return true
  }
  return false
}
