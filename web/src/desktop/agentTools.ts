// agentTools — 宿主/应用向 goder AI 注册自定义桌面工具的扩展点（P6）。
//
// 背景：osmcp 的 20 个 os_* 内置工具硬编码，宿主自建 App 的业务功能（如酒店
// 「查空房」）AI 操控不到。本注册表把这些能力动态暴露给 AI：
//   - 宿主静态声明：createOpenOS({ agentTools: [{ appId:'rooms', name:'vacant',
//     description:'查询今日空房', params:{...JSON Schema}, run: (args)=>... }] })
//   - 应用运行期注册：OpenOS.registerAgentTool(...) / unregisterAgentTool(...)
//   - 命名空间：最终工具名为 `app_<appId>_<name>`（防跨应用冲突；不传 appId
//     视为宿主级工具，名 = `app_host_<name>`）
//
// 链路：osmcp tools/list 经前端桥拉取在线工具表合并展示；AI 调用
// os_app_tool({tool, args}) → 桥 tool_call 分发到注册的 run 回调（应用窗口
// 不需要在场——run 由桌面壳执行；需要窗口的操作在 run 里自行 OpenOS.open）。
import type { Component } from 'vue'

export interface AgentToolDef {
  /** 应用归属（注册名最终为 app_<appId>_<name>；缺省 = app_host） */
  appId?: string
  /** 工具短名（小写字母数字与下划线） */
  name: string
  /** 给 AI 的功能描述（越具体越容易被正确调用） */
  description: string
  /** 参数 JSON Schema（可省略 = 无参工具） */
  params?: Record<string, any>
  /** 执行回调：抛错即工具失败（错误信息会回传给 AI） */
  run: (args: any) => any | Promise<any>
}

export interface AgentToolMeta {
  name: string
  appId: string
  description: string
  params?: Record<string, any>
}

const registry = new Map<string, AgentToolDef>()

const fullName = (appId: string | undefined, name: string) =>
  `app_${appId || 'host'}_${name}`

/** 注册一个 AI 工具（同名覆盖）。返回完整工具名。 */
export function registerAgentTool(def: AgentToolDef): string {
  if (!def || !def.name) throw new Error('registerAgentTool: 缺少 name')
  const key = fullName(def.appId, def.name)
  registry.set(key, def)
  return key
}

/** 注销（完整名或 {appId,name} 均可定位）。 */
export function unregisterAgentTool(name: string | { appId?: string; name: string }) {
  const key = typeof name === 'string' ? name : fullName(name.appId, name.name)
  registry.delete(key)
}

/** 当前在线工具表（osmcp tools/list / os_app_tools 经桥读取）。 */
export function agentTools(): AgentToolMeta[] {
  return [...registry.values()].map((d) => ({
    name: fullName(d.appId, d.name),
    appId: d.appId || 'host',
    description: d.description,
    params: d.params,
  }))
}

/** 按完整名执行（agentBridge tool_call 分发用）。 */
export async function runAgentTool(name: string, args: any): Promise<any> {
  const def = registry.get(name)
  if (!def) {
    throw new Error(`未注册的工具：${name}（在线：${agentTools().map((t) => t.name).join(', ') || '无'}）`)
  }
  return def.run(args)
}

/** createOpenOS 静态声明批注册（宿主配置面）。 */
export function installAgentTools(defs: AgentToolDef[] | undefined, _app?: Component) {
  for (const d of defs || []) registerAgentTool(d)
}
