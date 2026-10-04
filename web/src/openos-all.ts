// openos-all.ts — OpenOS 共享依赖聚合入口（App 独立发布通道的 deps 库集之一）。
//
// 由 web/tooling/deps.ts 作为库构建入口消费，产出单文件 openos-all.js；宿主页面
// importmap 把 @openos/framework、@openos/ui、@openos/stores/*、@openos/api/client
// 等裸名全部映射到该产物 URL——同一 URL 即同一模块实例，宿主壳与远程 App 共享
// 同一份组件库/仓库/单例状态（pinia store、toast/menu 注册表、runtimeComponents、
// window.OpenOS SDK 注入），与全打包形态行为一致。
//
// 因此宿主壳（main.ts 等编译进宿主 dist 的代码）对 @openos/* 与 vue/pinia 的导入
// 同样经 importmap external 到本产物——全站 openos/vue 只此一份，任何"宿主打包一份
// +远程 App 一份"的双实例都会造成 toast 不显示、store 断裂等隐蔽故障。
export * from './framework'
export * from './ui'
export * from './api/client'
export * from './stores/windows'
export * from './stores/calendar'
export * from './stores/session'
export * from './stores/apps'
export * from './bus/bus'
