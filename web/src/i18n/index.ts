// i18n：轻量运行时多语言（响应式 t() + 每用户持久化 + 宿主注入）。
//
// 定位与边界：OpenOS 以中文为一等语言，i18n 是「宿主出现海外场景时的能力底座」——
// 基建（本模块 + 语言设置 + 壳面覆盖）已就位，应用内文案按以下约定渐进接入，
// 不做一次性全量抽取：
//   import { t } from '@/i18n'
//   <button>{{ t('login.submit') }}</button>   // 渲染期调用，切换语言即时重渲染
//   t('login.hello', { name })                 // {name} 占位替换
//
// 语言持久化：登录后存服务端 user_settings 的 locale 键（localStorage 仅缓存，
// 与 dock/notifications 同一模式）；登录页（未知用户）用缓存值。持久化由调用方
// 负责（Settings 选择器 / Desktop 挂载同步），本模块不依赖 api——client.ts 需要
// 反向引用 t()，保持 i18n 零依赖避免环。
// 宿主注入：createOpenOS({ i18n: { locale, messages } })——messages 按 locale
// 深合并覆盖内置词条（品牌名等自定义文案），locale 可锁定初始语言。
import { reactive } from 'vue'

export type Locale = 'zh-CN' | 'en-US'

export const LOCALES: { value: Locale; label: string }[] = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'en-US', label: 'English' },
]

const CACHE_KEY = 'openos_locale'
const SETTINGS_KEY = 'locale'

const state = reactive({ locale: 'zh-CN' as Locale })

const zh: Record<string, string> = {
  'login.username': '用户名',
  'login.password': '密码',
  'login.submit': '登 录',
  'login.submitting': '登录中…',
  'login.demoHint': '点击快速填入演示账号',
  'login.expired': '登录状态已失效（可能已修改密码、账号被禁用或长时间未活动），请重新登录',
  'login.failed': '登录失败',
  'login.brandSub': '仿 macOS 网页桌面 · 多用户 · 事件总线',
  'login.ssoBtn': '使用统一身份登录',
  'login.ssoHint': '将跳转至统一登录页面，完成后自动返回',
  'login.ssoRedirecting': '跳转中…',
  'menu.about': '关于',
  'menu.settings': '系统设置…',
  'menu.logout': '退出登录',
  'menu.lockScreen': '锁定屏幕',
  'menu.launchpad': '启动台',
  'menu.guide': '使用指南…',
  'menu.dark': '切换深色模式（当前{mode}）',
  'menu.darkDark': '深',
  'menu.darkLight': '浅',
  'menu.wallpaper': '更换壁纸',
  'menu.lock': '锁定屏幕 / 退出登录',
  'common.cancel': '取消',
  'common.confirm': '确认',
  'common.close': '关闭',
  'common.save': '保存',
  'common.delete': '删除',
  'common.retry': '重试',
  'share.title': '收到分享',
  'share.save': '存入我的文档',
  'share.download': '下载',
  'share.saving': '保存中…',
  'share.loading': '正在获取分享…',
  'share.invalid': '分享不存在或已失效',
  'share.from': '{owner} 分享 · {size}',
  'share.saved': '已存入「文档」：{name}',
  'share.copied': '分享链接已复制（{days} 天内有效）',
  'share.revoked': '已撤销分享',
  'share.none': '暂无分享链接',
  'share.section': '分享',
}

const en: Record<string, string> = {
  'login.username': 'Username',
  'login.password': 'Password',
  'login.submit': 'Log In',
  'login.submitting': 'Logging in…',
  'login.demoHint': 'Click to fill a demo account',
  'login.expired': 'Your session has expired (password changed, account disabled, or signed out elsewhere). Please log in again.',
  'login.failed': 'Login failed',
  'login.brandSub': 'macOS-like Web Desktop · Multi-user · Event Bus',
  'login.ssoBtn': 'Sign in with SSO',
  'login.ssoHint': 'You will be redirected to the identity provider and return automatically',
  'login.ssoRedirecting': 'Redirecting…',
  'menu.about': 'About',
  'menu.settings': 'System Settings…',
  'menu.logout': 'Log Out',
  'menu.lockScreen': 'Lock Screen',
  'menu.launchpad': 'Launchpad',
  'menu.guide': 'User Guide…',
  'menu.dark': 'Toggle Dark Mode (currently {mode})',
  'menu.darkDark': 'dark',
  'menu.darkLight': 'light',
  'menu.wallpaper': 'Change Wallpaper',
  'menu.lock': 'Lock Screen / Log Out',
  'common.cancel': 'Cancel',
  'common.confirm': 'OK',
  'common.close': 'Close',
  'common.save': 'Save',
  'common.delete': 'Delete',
  'common.retry': 'Retry',
  'share.title': 'Shared with you',
  'share.save': 'Save to Documents',
  'share.download': 'Download',
  'share.saving': 'Saving…',
  'share.loading': 'Loading share…',
  'share.invalid': 'Share not found or expired',
  'share.from': 'Shared by {owner} · {size}',
  'share.saved': 'Saved to Documents: {name}',
  'share.copied': 'Share link copied (valid for {days} days)',
  'share.revoked': 'Share revoked',
  'share.none': 'No active shares',
  'share.section': 'Shares',
}

const dicts: Record<Locale, Record<string, string>> = { 'zh-CN': zh, 'en-US': en }

/** 宿主注入词条（按 locale 深合并覆盖内置；品牌文案自定义用）。 */
export function extendMessages(locale: Locale, dict: Record<string, string>) {
  Object.assign(dicts[locale], dict)
}

/** 取词：缺 key 回落中文，再缺回落 key 本身（开发期肉眼可辨）。 */
export function t(key: string, params?: Record<string, string | number>): string {
  let s = dicts[state.locale]?.[key]
  if (s === undefined) s = dicts['zh-CN'][key] ?? key
  if (params) {
    for (const [k, v] of Object.entries(params)) s = s.replaceAll(`{${k}}`, String(v))
  }
  return s
}

export function locale(): Locale { return state.locale }

/** 切换语言：即时生效（响应式）+ 写本地缓存；服务端 user_settings 的写入由调用方
 *  负责（已登录场景），未登录（登录页切换）仅本地。 */
export function setLocale(l: Locale) {
  state.locale = l
  document.documentElement.lang = l
  try { localStorage.setItem(CACHE_KEY, l) } catch { /* ignore */ }
}

/** 启动恢复：saved 为服务端 user_settings 的值（无则用本地缓存）。 */
export function initLocale(saved?: string) {
  let l: Locale = 'zh-CN'
  try { l = (localStorage.getItem(CACHE_KEY) as Locale) || 'zh-CN' } catch { /* ignore */ }
  if (saved === 'zh-CN' || saved === 'en-US') l = saved
  state.locale = l
  document.documentElement.lang = l
}

/** 通用设置键名（调用方读 user_settings 时用；服务端值优先于本地缓存）。 */
export const LOCALE_SETTINGS_KEY = SETTINGS_KEY
