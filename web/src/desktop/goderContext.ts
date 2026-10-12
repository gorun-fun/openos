// goderContext.ts — 宿主向 goder AI 会话注入的环境简报通道（P7 扩展点）。
//
// goder（gh ≥ host_context 版本）支持 grf:session.host_context：宿主平台
// 注入的运行环境事实，作为独立段拼在身份提示词之后，与 system_prompt
// 叠加而非替换。OpenOS 内置 goder 应用（GoderStudio）据此组装通用简报
// （运行环境/当前用户/角色/os_* 桌面工具面），再拼接业务宿主经
// createOpenOS({ goderContext }) 声明的业务段——让 AI 知道自己运行在
// 哪个业务系统里（grf-hotel 的酒店域、grf-os 的运维域等），宿主此前
// 只能把环境信息塞进 agentTools 的工具 description（唯一常驻上下文通道，
// 见 grf-os agent-tools.ts 的历史注释），现在有了正规通道。
let extraContext = ''

/** createOpenOS 静态声明（宿主配置面，框架启动时调用一次）。 */
export function setGoderContext(text: string | undefined): void {
  extraContext = (text || '').trim()
}

/** 业务宿主声明的附加简报段（空 = 未声明）。 */
export function goderContext(): string {
  return extraContext
}
