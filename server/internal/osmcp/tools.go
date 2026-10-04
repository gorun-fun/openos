// tools.go — 工具注册表：os_* 系列工具（gh 侧全名 mcp__openos__os_*）。
//
// 分两类：
//   - 服务端直答：os_info / os_apps_list / os_settings_* / os_fs_* /
//     os_bus_publish（数据在服务端，无需前端往返）
//   - 前端桥：os_desktop_state / os_window_* / os_app_open_file /
//     os_desktop_do / os_notes_do / os_notify（窗口与桌面状态在浏览器里，
//     经 bridge 往返执行）
//
// 描述文案会进 gh 的系统提示词，是 AI 的使用手册，保持准确且带操作提示。
package osmcp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"openos/server/internal/market"
)

// registerTools 注册全部工具（newServer 构造时调用）。
func (s *Server) registerTools() {
	s.registerInfoTools()
	s.registerFSTools()
	s.registerBridgeTools()
	s.registerP6Tools()
}

// ---- P6 扩展（2026-10）：文件分享 / 商店 / 用户目录 / 宿主自定义工具 ----

// registerP6Tools 服务端直答与桥工具的 P6 增量。
func (s *Server) registerP6Tools() {
	// 文件分享链接（进程内复用 vfs share handler：owner 校验/过期/审计全保留）
	s.add(Tool{
		Name:        "os_share_list",
		ReadOnly:    true,
		Description: "列出当前用户全部有效期内的文件分享链接（id/路径/文件名/大小/创建与过期时间）。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			return s.fsShareCall(c, "GET", "/api/fs/shares", nil)
		},
	})
	s.add(Tool{
		Name:        "os_share_create",
		Description: "为当前用户的某个 VFS 文件创建跨用户分享链接（默认 7 天、上限 30 天）。返回分享 URL 与过期时间。",
		Schema: obj(map[string]any{
			"path": pstr("VFS 内的文件路径，如 /home/用户/文档/报告.pdf"),
			"days": map[string]any{"type": "integer", "description": "有效天数（默认 7，上限 30）"},
		}, "path"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Path string `json:"path"`
				Days int    `json:"days"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Path == "" {
				return textErr("参数错误：需要 path")
			}
			body := map[string]any{"path": a.Path}
			if a.Days > 0 {
				body["days"] = a.Days
			}
			return s.fsShareCall(c, "POST", "/api/fs/shares", body)
		},
	})
	s.add(Tool{
		Name:        "os_share_revoke",
		Description: "撤销自己的一个分享链接（id 从 os_share_list 获取；撤销后链接即刻失效）。",
		Schema:      obj(map[string]any{"id": pstr("分享 id")}, "id"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				ID string `json:"id"`
			}
			if err := decodeArgs(c, &a); err != nil || a.ID == "" {
				return textErr("参数错误：需要 id")
			}
			return s.fsShareCall(c, "DELETE", "/api/fs/shares/"+url.PathEscape(a.ID), nil)
		},
	})

	// 应用商店（首个非本人维度的工具：按功能权限点门控，默认仅 admin）
	s.add(Tool{
		Name:        "os_market_list",
		ReadOnly:    true,
		Description: "列出应用商店当前目录（可安装应用清单：id/名称/形态/版本/说明）。需要 market.manage 权限。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			if !c.Can("market.manage") {
				return textErr("需要 market.manage 权限")
			}
			return textJSON(market.List(s.deps.DB))
		},
	})
	s.add(Tool{
		Name:        "os_market_install",
		Description: "从商店目录安装一个应用到指定角色（web/proxy/schema/bundle 形态）。需要 market.manage 权限；roles 缺省为 [admin]。",
		Schema: obj(map[string]any{
			"id":    pstr("商店目录条目 id（os_market_list 获取）"),
			"roles": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "授权角色列表（缺省 [admin]）"},
		}, "id"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			if !c.Can("market.manage") {
				return textErr("需要 market.manage 权限")
			}
			var a struct {
				ID    string   `json:"id"`
				Roles []string `json:"roles"`
			}
			if err := decodeArgs(c, &a); err != nil || a.ID == "" {
				return textErr("参数错误：需要 id")
			}
			if len(a.Roles) == 0 {
				a.Roles = []string{"admin"}
			}
			e, err := market.Install(s.deps.DB, a.ID, a.Roles)
			if err != nil {
				return textErr("安装失败：%v", err)
			}
			return textJSON(map[string]any{"installed": e.ID, "version": e.Version, "roles": a.Roles})
		},
	})
	s.add(Tool{
		Name:        "os_market_uninstall",
		Description: "卸载一个商店安装的应用（内置/宿主锁定应用会被拒绝）。需要 market.manage 权限。",
		Schema:      obj(map[string]any{"id": pstr("应用 id")}, "id"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			if !c.Can("market.manage") {
				return textErr("需要 market.manage 权限")
			}
			var a struct {
				ID string `json:"id"`
			}
			if err := decodeArgs(c, &a); err != nil || a.ID == "" {
				return textErr("参数错误：需要 id")
			}
			if err := s.deps.DB.DeleteApp(a.ID); err != nil {
				return textErr("卸载失败：%v（内置/锁定应用不可卸载）", err)
			}
			market.RemoveBundles(a.ID)
			return textJSON(map[string]any{"removed": a.ID})
		},
	})

	// 用户目录（权限点门控：AI 查身份/角色的只读窗口，管理操作仍走设置页）
	s.add(Tool{
		Name:        "os_users_list",
		ReadOnly:    true,
		Description: "列出全部用户（用户名/角色/显示名/禁用态/创建时间）。需要 users.manage 权限（admin 恒有）。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			if !c.Can("users.manage") {
				return textErr("需要 users.manage 权限")
			}
			rows, err := s.deps.DB.Query(`SELECT username, role, display_name, disabled, created_at FROM users ORDER BY id`)
			if err != nil {
				return textErr("读取用户失败：%v", err)
			}
			defer rows.Close()
			users := []map[string]any{}
			for rows.Next() {
				var name, role, dn, created string
				var disabled int
				if err := rows.Scan(&name, &role, &dn, &disabled, &created); err != nil {
					continue
				}
				users = append(users, map[string]any{"username": name, "role": role, "displayName": dn, "disabled": disabled == 1, "createdAt": created})
			}
			return textJSON(users)
		},
	})

	// 宿主/应用自定义工具（P6 扩展点：前端注册表经桥读取与执行）
	s.add(Tool{
		Name:        "os_app_tools",
		ReadOnly:    true,
		Description: "列出当前桌面上注册的宿主/应用自定义 AI 工具（app_<appId>_<name> 命名空间，含各工具的参数 Schema）。宿主业务功能（如查空房、查订单）经此暴露；无则返回空表。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			return s.bridgeCall(c, "tools_list", nil)
		},
	})
	s.add(Tool{
		Name:        "os_app_tool",
		Description: "执行一个宿主/应用自定义工具（os_app_tools 查看可用清单）。工具在当前用户桌面执行；需要窗口的操作由工具自行打开。",
		Schema: obj(map[string]any{
			"tool": pstr("完整工具名（app_<appId>_<name>）"),
			"args": map[string]any{"type": "object", "description": "工具参数（按其 Schema 提供）"},
		}, "tool"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Tool string          `json:"tool"`
				Args json.RawMessage `json:"args"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Tool == "" {
				return textErr("参数错误：需要 tool")
			}
			var args any
			if len(a.Args) > 0 {
				_ = json.Unmarshal(a.Args, &args)
			}
			if args == nil {
				args = map[string]any{}
			}
			return s.bridgeCall(c, "tool_call", map[string]any{"tool": a.Tool, "args": args})
		},
	})

	// 日历与 Dock（桥：数据权威在前端 store）
	s.add(Tool{
		Name:        "os_calendar_do",
		Description: "日历日程操作：list（列日程，可按 date=YYYY-MM-DD 过滤）/ add（title+date 必填，time=HH:MM 可选）/ remove（id）。",
		Schema: obj(map[string]any{
			"action": pstr("list | add | remove"),
			"id":     pstr("日程 id（remove 用）"),
			"title":  pstr("日程标题（add 用）"),
			"date":   pstr("日期 YYYY-MM-DD（add 必填 / list 可选过滤）"),
			"time":   pstr("时间 HH:MM（可选）"),
		}, "action"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			return s.bridgePass(c, "calendar_do")
		},
	})
	s.add(Tool{
		Name:        "os_dock_do",
		Description: "Dock 定制：list（当前固定列表）/ pin（appId 固定到末尾）/ unpin（appId 移除）/ reorder（appIds 数组全量重排）。",
		Schema: obj(map[string]any{
			"action": pstr("list | pin | unpin | reorder"),
			"appId":  pstr("应用 id（pin/unpin 用）"),
			"appIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "应用 id 全量有序数组（reorder 用）"},
		}, "action"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			return s.bridgePass(c, "dock_do")
		},
	})
}

// fsShareCall 进程内调用 vfs 分享 handler（owner/过期/审计语义与 REST 完全一致）。
func (s *Server) fsShareCall(c *ToolCall, method, urlPath string, body any) ([]Content, bool) {
	if s.deps.FS == nil || s.deps.FS.Disabled() {
		return textErr("文件系统未启用（OPENOS_VFS=off）")
	}
	var h http.HandlerFunc
	switch method {
	case "GET":
		h = s.deps.FS.ListShares
	case "POST":
		h = s.deps.FS.CreateShare
	case "DELETE":
		h = s.deps.FS.RevokeShare
	default:
		return textErr("不支持的方法")
	}
	out, failed := s.fsCall(c, h, method, urlPath, body)
	return []Content{{Type: "text", Text: out}}, failed
}

// bridgePass 桥工具直通外壳：参数原样透传给前端动作。
func (s *Server) bridgePass(c *ToolCall, action string) ([]Content, bool) {
	var args map[string]any
	if err := decodeArgs(c, &args); err != nil {
		return textErr("参数解析失败: %v", err)
	}
	return s.bridgeCall(c, action, args)
}

// ---- 系统与设置（服务端直答） ----

func (s *Server) registerInfoTools() {
	s.add(Tool{
		Name:        "os_info",
		ReadOnly:    true,
		Description: "OpenOS 系统概况：版本、当前用户（用户名/角色/显示名）、goder 集成状态、在线用户、VFS 与真实终端能力。了解环境时先调用本工具。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			res := map[string]any{
				"app":       "OpenOS",
				"version":   s.deps.Version,
				"user":      map[string]any{"username": c.User, "role": c.Role, "displayName": c.DisplayName},
				"vfs":       !s.fsDisabled(),
				"uptimeSec": int(time.Since(s.deps.StartedAt).Seconds()),
			}
			if s.deps.GoderStatus != nil {
				res["goder"] = s.deps.GoderStatus()
			}
			if s.deps.OnlineUsers != nil {
				res["onlineUsers"] = s.deps.OnlineUsers()
			}
			return textJSON(res)
		},
	})

	s.add(Tool{
		Name:        "os_apps_list",
		ReadOnly:    true,
		Description: "列出当前用户可打开的全部应用（id/名称/分类）。打开窗口用 os_window_open，appId 取本列表的 id；内置应用如 calculator（计算器）、terminal（终端）、files（文件）、docs（文档）、sheets（表格）、slides（幻灯片）、pdf、notes（便签）、settings（设置）、goder（AI 工作台）。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			apps, err := s.deps.DB.ListAppsForRole(c.Role)
			if err != nil {
				return textErr("读取应用列表失败: %v", err)
			}
			rows := make([]map[string]any, 0, len(apps))
			for _, a := range apps {
				rows = append(rows, map[string]any{
					"id": a.ID, "name": a.Name, "category": a.Category,
					"hidden": a.Hidden, "width": a.Width, "height": a.Height,
				})
			}
			return textJSON(rows)
		},
	})

	s.add(Tool{
		Name:        "os_settings_get",
		ReadOnly:    true,
		Description: "读取当前用户的服务端设置键值（user_settings，字符串值）。不存在返回空。常用键：wallpaper（壁纸编号）、dark（「1」/「0」）、dockAutoHide、notes（便签数据，建议改用 os_notes_do）。",
		Schema: obj(map[string]any{
			"key": pstr("设置键名"),
		}, "key"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Key string `json:"key"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Key == "" {
				return textErr("参数错误：需要 key")
			}
			v, err := s.deps.DB.GetUserSetting(c.User, a.Key)
			if err != nil {
				v = "" // 不存在视为空值
			}
			return []Content{{Type: "text", Text: v}}, false
		},
	})

	s.add(Tool{
		Name:        "os_settings_put",
		Description: "写入当前用户的服务端设置键值（字符串值）。注意：便签请用 os_notes_do（前端实时更新）；壁纸/深色请用 os_desktop_do（前端即时生效），本工具适合自定义键值。",
		Schema: obj(map[string]any{
			"key":   pstr("设置键名"),
			"value": pstr("设置值（字符串）"),
		}, "key", "value"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct{ Key, Value string }
			if err := decodeArgs(c, &a); err != nil || a.Key == "" {
				return textErr("参数错误：需要 key 与 value")
			}
			if err := s.deps.DB.SetUserSetting(c.User, a.Key, a.Value); err != nil {
				return textErr("保存失败: %v", err)
			}
			return []Content{{Type: "text", Text: "已保存 " + a.Key}}, false
		},
	})

	s.add(Tool{
		Name:        "os_bus_publish",
		Description: "向当前用户的桌面发布一条事件总线消息（任意主题）。通用应用控制通道：声明式应用（SchemaApp）可配置 dataSource.topic 触发刷新，第三方注册应用可监听自定义主题（如 app:xxx）。仅推给当前用户自己的浏览器，不影响其他用户。",
		Schema: obj(map[string]any{
			"topic":   pstr("主题名，如 app:calc、fs:changed、自定义主题"),
			"payload": map[string]any{"type": "object", "description": "消息内容（任意 JSON）"},
		}, "topic"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Topic   string          `json:"topic"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Topic == "" {
				return textErr("参数错误：需要 topic")
			}
			var payload any
			if len(a.Payload) > 0 {
				_ = json.Unmarshal(a.Payload, &payload)
			}
			s.deps.Hub.PublishTo(c.User, a.Topic, payload)
			return []Content{{Type: "text", Text: "已发布主题 " + a.Topic + "（当前用户桌面）"}}, false
		},
	})
}

// ---- VFS 文件（进程内复用 vfs REST handler：权限/配额/广播全保留） ----

func (s *Server) fsDisabled() bool {
	return s.deps.FS == nil || s.deps.FS.Disabled()
}

// fsTool VFS 工具的统一外壳（停用检查 + 调用 + 文本透传）。
func (s *Server) fsTool(name, desc string, readOnly bool, schema map[string]any, invoke func(c *ToolCall, a map[string]any) (string, bool)) {
	s.add(Tool{
		Name:        name,
		Description: desc,
		Schema:      schema,
		ReadOnly:    readOnly,
		Handler: func(c *ToolCall) ([]Content, bool) {
			if s.fsDisabled() {
				return textErr("文件系统已停用（OPENOS_VFS=off 部署）")
			}
			var a map[string]any
			if err := decodeArgs(c, &a); err != nil {
				return textErr("%v", err)
			}
			out, isErr := invoke(c, a)
			return []Content{{Type: "text", Text: out}}, isErr
		},
	})
}

func (s *Server) registerFSTools() {
	const homeHint = "路径必须位于 /home/<当前用户> 下（跨用户访问被拒绝）"
	s.fsTool("os_fs_list",
		"列出 OpenOS 虚拟文件系统中某目录的直接子项（文件应用的数据库，非宿主机磁盘）。"+homeHint+"；缺省为根目录 /。返回字段：path/isDir/isBinary/size/updatedAt。",
		true,
		obj(map[string]any{"path": pstr("目录绝对路径（可选，缺省 /）")}),
		func(c *ToolCall, a map[string]any) (string, bool) {
			p, _ := a["path"].(string)
			if p == "" {
				p = "/"
			}
			return s.fsCall(c, s.deps.FS.List, "GET", "/api/fs/list?path="+url.QueryEscape(p), nil)
		})

	s.fsTool("os_fs_read",
		"读取 OpenOS 虚拟文件系统中的一个文本文件（文档/表格应用可打开的内容）。"+homeHint+"。",
		true,
		obj(map[string]any{"path": pstr("文件绝对路径")}, "path"),
		func(c *ToolCall, a map[string]any) (string, bool) {
			p, _ := a["path"].(string)
			return s.fsCall(c, s.deps.FS.Read, "GET", "/api/fs/read?path="+url.QueryEscape(p), nil)
		})

	s.fsTool("os_fs_write",
		"写入 OpenOS 虚拟文件系统中的一个文本文件（新建或整体覆盖）。写入成功会广播 fs:changed，用户打开的文件/文档应用自动刷新。"+homeHint+"；单文件 ≤8MB、个人空间配额 100MB。写完可用 os_app_open_file 在对应应用中打开。",
		false,
		obj(map[string]any{
			"path":    pstr("文件绝对路径（如 /home/<用户>/文档/说明.md）"),
			"content": pstr("文件全文内容（纯文本）"),
		}, "path", "content"),
		func(c *ToolCall, a map[string]any) (string, bool) {
			return s.fsCall(c, s.deps.FS.Write, "PUT", "/api/fs/write", map[string]any{
				"path": a["path"], "content": a["content"],
			})
		})

	s.fsTool("os_fs_mkdir",
		"在 OpenOS 虚拟文件系统中创建目录（父目录自动补齐）。"+homeHint+"。",
		false,
		obj(map[string]any{"path": pstr("目录绝对路径")}, "path"),
		func(c *ToolCall, a map[string]any) (string, bool) {
			return s.fsCall(c, s.deps.FS.Mkdir, "POST", "/api/fs/mkdir", map[string]any{"path": a["path"]})
		})

	s.fsTool("os_fs_delete",
		"删除 OpenOS 虚拟文件系统中的文件或目录（递归，含 .Trash 废纸篓操作）。"+homeHint+"。不可恢复，删除前向用户确认。",
		false,
		obj(map[string]any{"path": pstr("目标绝对路径")}, "path"),
		func(c *ToolCall, a map[string]any) (string, bool) {
			p, _ := a["path"].(string)
			return s.fsCall(c, s.deps.FS.Remove, "DELETE", "/api/fs/remove?path="+url.QueryEscape(p), nil)
		})

	s.fsTool("os_fs_search",
		"按名称子串递归搜索当前用户在 OpenOS 虚拟文件系统中的全部文件（隐藏文件除外）。",
		true,
		obj(map[string]any{
			"query": pstr("搜索关键词"),
			"limit": pnum("结果数上限（可选，缺省 50，最大 200）"),
		}, "query"),
		func(c *ToolCall, a map[string]any) (string, bool) {
			q, _ := a["query"].(string)
			u := "/api/fs/search?q=" + url.QueryEscape(q)
			if n, ok := a["limit"].(float64); ok && n > 0 {
				u += "&limit=" + strconv.Itoa(int(n))
			}
			return s.fsCall(c, s.deps.FS.Search, "GET", u, nil)
		})

	s.fsTool("os_fs_usage",
		"当前用户虚拟文件空间的用量与配额（字节）。",
		true,
		obj(nil),
		func(c *ToolCall, a map[string]any) (string, bool) {
			return s.fsCall(c, s.deps.FS.Usage, "GET", "/api/fs/usage", nil)
		})
}

// ---- 前端桥（窗口/桌面/便签在浏览器里执行） ----

// bridgeCall 桥工具统一外壳：往返执行并把前端返回的 data 透传给模型。
func (s *Server) bridgeCall(c *ToolCall, action string, args map[string]any) ([]Content, bool) {
	data, err := s.bridge.call(c.User, action, args)
	if err != nil {
		return textErr("%s 失败：%v", action, err)
	}
	if len(data) == 0 || string(data) == "null" {
		return []Content{{Type: "text", Text: "ok"}}, false
	}
	return []Content{{Type: "text", Text: string(data)}}, false
}

func (s *Server) registerBridgeTools() {
	s.add(Tool{
		Name:        "os_desktop_state",
		ReadOnly:    true,
		Description: "读取当前用户桌面的实时状态：窗口列表（id/appId/标题/位置尺寸/最小化/最大化/是否激活）、深浅色、壁纸编号、Dock 自动隐藏、便签概要、视口尺寸。操控窗口前先调用本工具了解现状。",
		Schema:      obj(nil),
		Handler: func(c *ToolCall) ([]Content, bool) {
			return s.bridgeCall(c, "state", nil)
		},
	})

	s.add(Tool{
		Name:        "os_window_open",
		Description: "打开一个应用窗口（appId 用 os_apps_list 的 id）。props 为打开参数：文件/文档类应用传 {\"path\": \"/home/<用户>/...\"}。同一应用同参数的窗口已存在时会聚焦而非重复打开。",
		Schema: obj(map[string]any{
			"appId": pstr("应用 ID"),
			"props": map[string]any{"type": "object", "description": "打开参数（可选），如 {\"path\":\"/home/<用户>/文档/欢迎.md\"}"},
			"title": pstr("窗口标题（可选）"),
		}, "appId"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				AppID string          `json:"appId"`
				Props json.RawMessage `json:"props"`
				Title string          `json:"title"`
			}
			if err := decodeArgs(c, &a); err != nil || a.AppID == "" {
				return textErr("参数错误：需要 appId")
			}
			args := map[string]any{"appId": a.AppID}
			if len(a.Props) > 0 {
				args["props"] = a.Props
			}
			if a.Title != "" {
				args["title"] = a.Title
			}
			return s.bridgeCall(c, "window_open", args)
		},
	})

	s.add(Tool{
		Name:        "os_window_close",
		Description: "关闭窗口：按 windowId 关一个，或按 appId 关该应用的全部窗口。",
		Schema: obj(map[string]any{
			"windowId": pstr("窗口 ID（os_desktop_state 的 windows[].id，可选）"),
			"appId":    pstr("应用 ID（关闭其全部窗口，可选）"),
		}),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				WindowID string `json:"windowId"`
				AppID    string `json:"appId"`
			}
			if err := decodeArgs(c, &a); err != nil || (a.WindowID == "" && a.AppID == "") {
				return textErr("参数错误：需要 windowId 或 appId")
			}
			args := map[string]any{}
			if a.WindowID != "" {
				args["windowId"] = a.WindowID
			}
			if a.AppID != "" {
				args["appId"] = a.AppID
			}
			return s.bridgeCall(c, "window_close", args)
		},
	})

	s.add(Tool{
		Name:        "os_window_do",
		Description: "操作一个窗口：focus（聚焦并恢复最小化）/ minimize（最小化）/ toggle_maximize（最大化切换）/ snap（半屏吸附，zone=left|right）/ unsnap（还原）/ set_bounds（移动或调整位置尺寸）/ set_title（改标题）。windowId 缺省作用于当前激活窗口。",
		Schema: obj(map[string]any{
			"action":   pstr("focus | minimize | toggle_maximize | snap | unsnap | set_bounds | set_title"),
			"windowId": pstr("窗口 ID（可选，缺省为当前激活窗口）"),
			"zone":     pstr("snap 的吸附侧：left 或 right"),
			"bounds":   map[string]any{"type": "object", "description": "set_bounds 的目标几何 {x,y,w,h}（像素）"},
			"title":    pstr("set_title 的新标题"),
		}, "action"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Action   string          `json:"action"`
				WindowID string          `json:"windowId"`
				Zone     string          `json:"zone"`
				Bounds   json.RawMessage `json:"bounds"`
				Title    string          `json:"title"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Action == "" {
				return textErr("参数错误：需要 action")
			}
			args := map[string]any{"action": a.Action}
			if a.WindowID != "" {
				args["windowId"] = a.WindowID
			}
			if a.Zone != "" {
				args["zone"] = a.Zone
			}
			if len(a.Bounds) > 0 {
				args["bounds"] = a.Bounds
			}
			if a.Title != "" {
				args["title"] = a.Title
			}
			return s.bridgeCall(c, "window_do", args)
		},
	})

	s.add(Tool{
		Name:        "os_app_open_file",
		Description: "按扩展名自动选择应用打开 OpenOS 虚拟文件系统中的文件（md/txt→文档、csv→表格、html→幻灯片、pdf→PDF、图片→文件管理并预览）。需先经 os_fs_write 写入。",
		Schema: obj(map[string]any{
			"path": pstr("文件绝对路径"),
		}, "path"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Path == "" {
				return textErr("参数错误：需要 path")
			}
			return s.bridgeCall(c, "app_open_file", map[string]any{"path": a.Path})
		},
	})

	s.add(Tool{
		Name:        "os_desktop_do",
		Description: "桌面外观操作：wallpaper（arg=壁纸编号数字）/ dark（arg=深色开关 true|false）/ dock_autohide（arg=最大化时自动隐藏 Dock true|false）。即时生效并持久化。",
		Schema: obj(map[string]any{
			"action": pstr("wallpaper | dark | dock_autohide"),
			"arg":    map[string]any{"type": []any{"boolean", "number", "string"}, "description": "动作参数（布尔或数字）"},
		}, "action"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Action string          `json:"action"`
				Arg    json.RawMessage `json:"arg"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Action == "" {
				return textErr("参数错误：需要 action")
			}
			args := map[string]any{"action": a.Action}
			if len(a.Arg) > 0 {
				args["arg"] = a.Arg
			}
			return s.bridgeCall(c, "desktop_do", args)
		},
	})

	s.add(Tool{
		Name:        "os_notes_do",
		Description: "便签管理（桌面便签小组件与便签应用共用数据，实时更新）：list（列出全部，含文本预览）/ create（新建，text 可选、color 为 0-4 颜色下标可选）/ update（改 text 或 color）/ remove（删除）。",
		Schema: obj(map[string]any{
			"action": pstr("list | create | update | remove"),
			"id":     pstr("便签 ID（update/remove 必填）"),
			"text":   pstr("便签文本（create/update 可选）"),
			"color":  pnum("颜色下标 0-4（create/update 可选）"),
		}, "action"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Action string   `json:"action"`
				ID     string   `json:"id"`
				Text   *string  `json:"text"`
				Color  *float64 `json:"color"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Action == "" {
				return textErr("参数错误：需要 action")
			}
			args := map[string]any{"action": a.Action}
			if a.ID != "" {
				args["id"] = a.ID
			}
			if a.Text != nil {
				args["text"] = *a.Text
			}
			if a.Color != nil {
				args["color"] = *a.Color
			}
			return s.bridgeCall(c, "notes_do", args)
		},
	})

	s.add(Tool{
		Name:        "os_notify",
		Description: "向用户桌面发送一条系统通知（右上角横幅 + 通知中心可回看）。type：info（默认）/ success / error。",
		Schema: obj(map[string]any{
			"text": pstr("提示内容"),
			"type": pstr("info | success | error（可选）"),
		}, "text"),
		Handler: func(c *ToolCall) ([]Content, bool) {
			var a struct {
				Text string `json:"text"`
				Type string `json:"type"`
			}
			if err := decodeArgs(c, &a); err != nil || a.Text == "" {
				return textErr("参数错误：需要 text")
			}
			args := map[string]any{"text": a.Text}
			if a.Type != "" {
				args["type"] = a.Type
			}
			return s.bridgeCall(c, "notify", args)
		},
	})
}

// ---- Schema 构造助手（JSON Schema 最小子集） ----

func obj(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func pstr(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func pnum(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
