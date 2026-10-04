# OpenOS UI 组件库约定（web/src/ui）

Os* 前缀组件库是全系统（openos 内置应用 + 宿主业务应用 + 商店 bundle 应用）的唯一
视觉与交互基线。本文是新增/修改组件的 checklist 与既有约定的权威说明。

## 组件清单速览

| 类别 | 组件 |
|---|---|
| 基础控件 | OsButton / OsInput / OsTextarea / OsSelect / OsCheckbox / OsToggle / OsRadio / OsUpload |
| 数据展示 | OsTable（排序+双模分页）/ OsChip（信息胶囊）/ OsStatusDot（状态圆点）/ OsProgress / OsEmpty / AppIcon |
| 布局与壳 | OsPanel / OsLayout（侧栏+内容+状态栏，移动端自动抽屉）/ OsToolbar（merged 融合标题栏）/ OsTabs / OsTabApp（tab 型应用聚合壳）/ OsTbOverflow（工具栏折叠） |
| 反馈 | OsModal / OsConfirm（经 osConfirm() 拉起）/ OsToasts（经 toast()） |
| 表单 | OsForm / OsField（rules 校验：required/min/max/pattern/validator） |
| 文件 | OsFilePicker（macOS Sheet 式对话框）/ OsUpload（VFS 或自定义端点） |
| 门控 | can()/v-perm（功能级权限，见 ui/perms.ts） |

## 硬约定（新增组件 checklist）

1. **令牌引用**：颜色/圆角/阴影/字号一律引用 `--os-*` CSS 变量（清单见
   `styles/base.css :root` 与 `html.dark`），**禁止硬编码色值**（语义色例外：
   ok `#30a46c` / warn `#ff9f0a`/`#b25b00` / err `#e5484d` 体系内统一用这四个）。
2. **类名前缀**：scoped 类一律 `os-` 前缀；会被外部借用的全局类放 base.css。
3. **焦点可达**：可交互元素走原生 `:focus-visible`（base.css 全局焦点环）；
   图标按钮必须带 `aria-label`（title 只服务 tooltip）。
4. **控件尺寸**：默认 13px 字号、8px 控件圆角、高度对齐 OsButton md（28px）；
   `size="sm"` 变体 24px。工具栏沿 `.os-tb` 46px 约定。
5. **弹窗/确认**：业务弹窗走 `openDialog`/`registerDialog`，确认类走
   `osConfirm()`——**严禁浏览器原生 confirm()/alert()**，错误提示走 `toast()`。
6. **菜单**：右键/下拉一律 `useMenu()` 全局单例，勿自建遮罩与 document 监听。
7. **权限门控**：功能级按钮/页签用 `can('perm')` 或 `v-perm`，勿自读 role 字符串。
8. **移动端**：容器需 `min-width: 0` 防 flex 溢出；侧栏布局用 OsLayout 自带抽屉化；
   `prefers-reduced-motion` 下动画收敛（全局已有，组件内自定义动画需自带降级）。
9. **响应式 props**：变更类 props 用 v-model 命名（`modelValue` + `update:modelValue`）。
10. **导出面**：新组件加入 `ui/index.ts` 导出清单即自动进 `@openos/framework` 与
    importmap 裸名 `@openos/ui`——**deps.ts OPENOS_BARE_IDS 不需要逐组件登记**
    （全部经 openos-all 单产物分发）；新增**模块级**公共入口才需登记裸名。

## 主题定制（宿主）

`createOpenOS({ theme: { accent, dark, tokens } })`：

- `accent`：强调色（派生 hover/grad/soft/selected/focus-ring 由 color-mix 自动计算）；
- `dark`：`'auto'`（跟随系统）/`'dark'`/`'light'` 初始深浅色（用户仍可在设置切换）；
- `tokens`：CSS 变量级覆盖，键可省 `--os-` 前缀，如 `{ radius: '10px' }` → `--os-radius`。

## 稳定性边界

- `@openos/framework` / `@openos/ui` 的导出面是对宿主的公共 API：**删除或改名
  导出 = 破坏性变更**，须升 host 的 minHostVersion 并在发布说明标注；
- 组件 props 新增可选字段恒为安全变更；
- 商店 bundle 应用只有 `window.OpenOS`（含 `ui`），不深入内部路径。
