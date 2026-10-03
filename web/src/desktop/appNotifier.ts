// 后台应用通知：商店 bundle 应用卸载后没有代码在场（组件 unmount 即失聪），
// 「应用没打开时来了消息」的提醒由桌面壳常驻兜底——注册总线主题规则，
// 消息到达且应用不在前台使用时经通知中心弹横幅（点击打开来源应用）。
// 新应用接入 = 往 rules 加一条，无需改桌面壳其他部分。
import { bus } from '../bus/bus'
import { useWindows } from '../stores/windows'
import { useSession } from '../stores/session'
import { notify } from '../stores/notifications'

interface NotifierRule {
  /** 监听的总线主题 */
  topic: string
  /** 通知归属应用（点击横幅打开） */
  appId: string
  /** 应用「正在使用」判定（为 true 时不打扰；桌面=有可见窗口，手机=当前全屏应用） */
  active: () => boolean
  title: (payload: any, from: string) => string
  body: (payload: any, from: string) => string
}

// 与服务端 attach.Inline 白名单一致（SVG 排除）
const IMAGE_MIMES = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/bmp', 'image/avif'])

const rules: NotifierRule[] = [
  {
    topic: 'chat:room',
    appId: 'chat',
    // 可见窗口判定同时覆盖两形态：手机回桌面 = 窗口 minimized 挂后台，同样该提醒
    active: () => useWindows().list.some((w) => w.appId === 'chat' && !w.minimized),
    title: (_p, from) => `聊天 · ${from || '新消息'}`,
    body: (p) => {
      const a = p && p.attach
      if (a) return a.mime && IMAGE_MIMES.has(a.mime) ? '[图片]' : `[文件] ${a.name || ''}`
      const text = ((p && p.text) || '').slice(0, 60)
      return text || '[新消息]'
    },
  },
]

/** createOpenOS 时安装一次：全部规则的总线订阅。 */
export function installAppNotifiers() {
  for (const r of rules) {
    bus.on(r.topic, (p: any, from: string) => {
      if (!from || from === 'system') return // 系统侧不提醒；广播不回声发布者，from 必为他人
      if (from === useSession().user?.username) return // 多标签页自己发的（服务端已不回声，本地分发兜底）
      if (r.active()) return
      notify({ title: r.title(p, from), body: r.body(p, from), appId: r.appId })
    })
  }
}
