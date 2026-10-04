// 文件分享接收：/#share=<id> 片段路由 + 系统级接收弹窗（内建注册保留 id 'os:share'，
// 与 os:confirm 同范式：模块导入即注册，复用 DialogHost 浮层）。
//
// 链接形态：<origin>/#share=<32 位十六进制 id>——进入桌面后（未登录则先过登录，
// 片段在同页 SPA 登录下保留）弹出接收弹窗；存入文档走 SaveToDocs（配额/幂等继承），
// 下载恒 attachment。SSO 全程跳转可能丢失片段（IdP 回跳不保留 hash），属已知限制。
import ShareReceive from './ShareReceive.vue'
import { t } from '../i18n'
import { registerDialog, openDialog } from './dialogService'

registerDialog('os:share', { component: ShareReceive, title: '收到分享', width: 360 })

/** 消费地址栏的 #share=<id> 片段（有则清掉地址栏）；无片段返回 null。 */
export function consumeShareHash(): string | null {
  const m = location.hash.match(/^#share=([0-9a-fA-F]{32})/)
  if (!m) return null
  history.replaceState(null, '', location.pathname + location.search)
  return m[1]
}

/** 拉起分享接收弹窗（Esc/遮罩 = 关闭）。 */
export function openShareDialog(id: string): Promise<unknown> {
  return openDialog('os:share', { id }, { title: t('share.title') })
}
