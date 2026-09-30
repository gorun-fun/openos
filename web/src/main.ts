// OpenOS 独立部署入口：以默认配置组装桌面壳。
// 宿主应用集成请改为引用 framework.ts 的 createOpenOS()（见其文件头示例）。
import { createOpenOS } from './framework'

createOpenOS()
