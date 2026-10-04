// Package vfs 提供基于 SQLite 的虚拟文件系统（REST）与挂载在事件总线上的虚拟终端。
package vfs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"openos/server/internal/auth"
	"openos/server/internal/bus"
	"openos/server/internal/safeenv"
	"openos/server/internal/store"
)

// 单文件上传上限。
const maxFileSize = 8 << 20

// maxFileRows 每用户文件行数上限：touch/mkdir 曾不做配额检查，可无限插行
// 绕过 100MB 配额（SQLite 膨胀 DoS，安全审计 2026-10）。行数与配额双重设限。
const maxFileRows = 20000

// 按扩展名判定是否按文本存储（其余一律按二进制 base64）。
var textExts = map[string]bool{
	".txt": true, ".md": true, ".csv": true, ".tsv": true, ".json": true,
	".html": true, ".js": true, ".ts": true, ".css": true, ".xml": true,
	".log": true, ".yml": true, ".yaml": true, ".sh": true, ".slides.json": true,
}

func isTextExt(p string) bool {
	lower := strings.ToLower(p)
	for ext := range textExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func (f *FS) quotaUsed(user string) int64 {
	var used int64
	f.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(content)),0) FROM files WHERE owner=?`, user).Scan(&used)
	return used
}

// AIChat goder 非流式问答接口（由 ai 包注入，避免循环依赖）。
type AIChat func(ctx string, prompt string) string

// FS 虚拟文件系统服务。
type FS struct {
	db  *store.DB
	hub *bus.Hub
	ai  AIChat

	mu  sync.Mutex
	cwd map[string]string // username → 当前工作目录（虚拟终端会话）

	// 真实终端（受控执行：sh -c，10s 超时，输出截断，工作目录限定个人沙箱）
	realShell bool
	realMu    sync.Mutex
	realCwd   map[string]string
	homesDir  string

	// disabled：OPENOS_VFS=off 时整体关闭（宿主模式——业务文件交由集成方管理，
	// OpenOS 只保留演示数据与界面设置）；seedDemo：OPENOS_SEED_DEMO=0 时不种示例文件
	disabled bool
	seedDemo bool
}

// New 创建服务。dataDir 用于真实终端的沙箱目录（<dataDir>/homes/<user>）。
func New(dataDir string, db *store.DB, hub *bus.Hub, ai AIChat) *FS {
	// 真实 shell 默认关闭（安全审计 2026-10：sh -c 以服务进程权限执行任意命令，
	// 曾默认开启且继承全部环境变量——秘密可被读取自我提权）。on=受限沙箱执行、
	// full=解除目录限制；独立演示部署在 compose/run.sh 显式开 on。
	v := os.Getenv("OPENOS_REAL_SHELL")
	realShell := v == "on" || v == "full"
	fs := &FS{
		db: db, hub: hub, ai: ai,
		cwd: map[string]string{}, realCwd: map[string]string{},
		realShell: realShell, homesDir: filepath.Join(dataDir, "homes"),
		disabled: os.Getenv("OPENOS_VFS") == "off",
		seedDemo: os.Getenv("OPENOS_SEED_DEMO") != "0",
	}
	if !fs.disabled {
		hub.Handle("term:*", fs.onTerminal)
	}
	return fs
}

// Disabled 文件系统是否被整体关闭（OPENOS_VFS=off）。
func (f *FS) Disabled() bool { return f.disabled }

// Guard 包裹 fs REST 处理器：整体关闭时统一返回 503（防御纵深——即使前端残留旧引用）。
func (f *FS) Guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.disabled {
			http.Error(w, `{"error":"文件系统未启用（OPENOS_VFS=off）"}`, http.StatusServiceUnavailable)
			return
		}
		h(w, r)
	}
}

// RealShellEnabled 真实终端是否可用（状态展示用）。
func (f *FS) RealShellEnabled() bool { return f.realShell && !f.disabled }

// DeleteHome 删除用户的真实终端沙箱目录（删用户时级联清理）。
func (f *FS) DeleteHome(user string) {
	_ = os.RemoveAll(filepath.Join(f.homesDir, filepath.Base(user)))
}

// SaveToDocs 把字节以 name 存进用户「文档」目录（聊天附件归档等系统侧写入用）：
// 内容级幂等——目标路径已存在且内容相同直接返回现路径（重复保存同一附件不堆积）；
// 同名不同内容则按「名 (2).ext」递增重命名落盘。配额满返回错误。
// 返回落盘的 VFS 路径，并广播 fs:changed 让文件应用即时刷新。
func (f *FS) SaveToDocs(user, name string, data []byte) (string, error) {
	if f.disabled {
		return "", fmt.Errorf("文件系统未启用")
	}
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	if len(name) > 200 {
		ext := path.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		name = stem[len(stem)-(200-len(ext)):] + ext
	}
	dir := homeOf(user) + "/文档"
	if _, err := clean(user, dir); err != nil {
		return "", err
	}

	text := isTextExt(name)
	content := string(data)
	isBin := 0
	if !text {
		isBin = 1
		content = base64.StdEncoding.EncodeToString(data)
	}

	// 幂等：同路径同内容视为已保存
	target := path.Join(dir, name)
	var existing string
	if err := f.db.QueryRow(`SELECT content FROM files WHERE owner=? AND path=? AND is_dir=0`, user, target).Scan(&existing); err == nil {
		if existing == content {
			return target, nil
		}
	}
	// 同名不同内容：名 (2).ext 递增
	for i := 2; ; i++ {
		var n int
		f.db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner=? AND path=?`, user, target).Scan(&n)
		if n == 0 {
			break
		}
		ext := path.Ext(name)
		target = path.Join(dir, strings.TrimSuffix(name, ext)+" ("+strconv.Itoa(i)+")"+ext)
	}

	if f.quotaUsed(user)+int64(len(content)) > store.StorageQuota {
		return "", fmt.Errorf("个人空间已满（配额 %dMB），请清理后重试", store.StorageQuota>>20)
	}
	f.ensureDirs(user, target)
	if _, err := f.db.Exec(`INSERT INTO files(owner,path,is_dir,is_binary,content,updated_at) VALUES(?,?,0,?,?,datetime('now'))`,
		user, target, isBin, content); err != nil {
		return "", err
	}
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": target, "kind": "save"})
	return target, nil
}

// ---- 路径 ----

func homeOf(user string) string { return "/home/" + user }

// clean 规范化并校验路径必须位于用户 home 内。
func clean(user, p string) (string, error) {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if p == "/" {
		return "/", nil
	}
	home := homeOf(user)
	// 前缀比较必须带 / 边界：user=adm 时 /home/admin/x 不允许通过
	//（现有 SQL 均带 owner=? 过滤兜底，此处收紧防未来误用）
	if p != home && !strings.HasPrefix(p, home+"/") {
		return "", fmt.Errorf("权限不足：只能访问 %s 下的文件", home)
	}
	return p, nil
}

// ---- REST Handlers ----

// EnsureHome 为用户初始化 home 目录与示例文件（文件系统整体关闭时跳过；
// OPENOS_SEED_DEMO=0 时只建目录结构，不种欢迎文档与示例表格）。
func (f *FS) EnsureHome(user string) {
	if f.disabled {
		return
	}
	home := homeOf(user)
	// 废纸篓目录（点前缀：列表/搜索默认隐藏，仅侧栏与 Dock 以专用入口呈现）；
	// 幂等建在存量检查之前，保证老用户每次登录也补齐（计数需排除自身，否则首次插入会误判已有文件）
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, home+"/.Trash")
	var n int
	f.db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner=? AND path<>?`, user, home+"/.Trash").Scan(&n)
	if n > 0 {
		return
	}
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, "/home")
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, home)
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, home+"/文档")
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, home+"/表格")
	if !f.seedDemo {
		return
	}
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir,content) VALUES(?,?,0,?)`, user, home+"/欢迎.md",
		"# 欢迎使用 OpenOS\n\n这是一份示例文档，可由「文档」应用打开编辑。\n\n- 前后端分离：Go + Vite\n- 事件总线通信\n- 多用户多角色\n")
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir,content) VALUES(?,?,0,?)`, user, home+"/表格/收支.csv",
		"项目,收入,支出\n工资,12000,0\n房租,0,3500\n餐饮,0,1800\n结余,=SUM(B2:B3)-SUM(C2:C4),\n")
}

// List GET /api/fs/list?path=
func (f *FS) List(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	rows, err := f.db.Query(`SELECT path,is_dir,is_binary,content,updated_at FROM files WHERE owner=?`, user)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	prefix := strings.TrimSuffix(p, "/") + "/"
	var out []store.File
	for rows.Next() {
		var e store.File
		var isDir, isBin int
		if err := rows.Scan(&e.Path, &isDir, &isBin, &e.Content, &e.UpdatedAt); err != nil {
			continue
		}
		e.IsDir = isDir == 1
		e.IsBinary = isBin == 1
		rest := strings.TrimPrefix(e.Path, prefix)
		if rest == "" || e.Path == p || strings.Contains(rest, "/") {
			continue // 非直接子项
		}
		e.Size = len(e.Content)
		if e.IsBinary && e.Size > 0 {
			e.Size = e.Size * 3 / 4 // base64 还原后近似大小
		}
		e.Content = ""
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Path < out[j].Path
	})
	if out == nil {
		out = []store.File{}
	}
	writeJSON(w, out)
}

// Read GET /api/fs/read?path=
func (f *FS) Read(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	var content string
	var isDir, isBin int
	if err := f.db.QueryRow(`SELECT content,is_dir,is_binary FROM files WHERE owner=? AND path=?`, user, p).Scan(&content, &isDir, &isBin); err != nil {
		http.Error(w, "文件不存在", 404)
		return
	}
	if isDir == 1 {
		http.Error(w, "这是一个目录", 400)
		return
	}
	writeJSON(w, map[string]any{"path": p, "content": content, "isBinary": isBin == 1})
}

// Raw GET /api/fs/raw?path= 返回文件原始字节（下载 / 图片 / PDF 预览）。
func (f *FS) Raw(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	var content string
	var isBin int
	if err := f.db.QueryRow(`SELECT content,is_binary FROM files WHERE owner=? AND path=?`, user, p).Scan(&content, &isBin); err != nil {
		http.Error(w, "文件不存在", 404)
		return
	}
	data := []byte(content)
	if isBin == 1 {
		if decoded, err := base64.StdEncoding.DecodeString(content); err == nil {
			data = decoded
		}
	}
	name := path.Base(p)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// Usage GET /api/fs/usage 个人空间用量。
func (f *FS) Usage(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	writeJSON(w, map[string]any{"used": f.quotaUsed(user), "quota": store.StorageQuota})
}

// Upload POST /api/fs/upload（multipart：dir + 一个或多个 file）
func (f *FS) Upload(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	if err := r.ParseMultipartForm(maxFileSize); err != nil {
		http.Error(w, "上传失败：单文件不能超过 8MB", 400)
		return
	}
	dirRaw := r.FormValue("dir")
	dir, err := clean(user, dirRaw)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Error(w, "未选择文件", 400)
		return
	}
	used := f.quotaUsed(user)
	saved := []string{}
	for _, fh := range files {
		if fh.Size > maxFileSize {
			http.Error(w, "文件 "+fh.Filename+" 超过 8MB 限制", 400)
			return
		}
		src, err := fh.Open()
		if err != nil {
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(src, maxFileSize+1))
		src.Close()

		name := path.Base(strings.ReplaceAll(fh.Filename, "\\", "/"))
		p := path.Join(dir, name)
		cp, err := clean(user, p)
		if err != nil {
			continue
		}
		text := isTextExt(name)
		content := string(data)
		isBin := 0
		if !text {
			isBin = 1
			content = base64.StdEncoding.EncodeToString(data)
		}
		if used+int64(len(content)) > store.StorageQuota {
			http.Error(w, "个人空间已满（配额 100MB），请清理后重试", 400)
			return
		}
		used += int64(len(content))
		f.ensureDirs(user, cp)
		if _, err := f.db.Exec(`INSERT INTO files(owner,path,is_dir,is_binary,content,updated_at) VALUES(?,?,0,?,?,datetime('now'))
			ON CONFLICT(owner,path) DO UPDATE SET is_binary=excluded.is_binary, content=excluded.content, updated_at=datetime('now')`,
			user, cp, isBin, content); err != nil {
			continue
		}
		saved = append(saved, cp)
		f.db.Audit(user, "fs.upload", cp, "")
		f.hub.PublishTo(user, "fs:changed", map[string]any{"path": cp, "kind": "upload"})
	}
	writeJSON(w, map[string]any{"ok": "1", "saved": saved})
}

// Write PUT /api/fs/write {path, content}
func (f *FS) Write(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ Path, Content string }
	if err := jsonDecode(r, &req); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	p, err := clean(user, req.Path)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	var oldLen int64
	f.db.QueryRow(`SELECT LENGTH(content) FROM files WHERE owner=? AND path=?`, user, p).Scan(&oldLen)
	if f.quotaUsed(user)-oldLen+int64(len(req.Content)) > store.StorageQuota {
		http.Error(w, "个人空间已满（配额 100MB）", 400)
		return
	}
	f.ensureDirs(user, p)
	if _, err := f.db.Exec(`INSERT INTO files(owner,path,is_dir,content,updated_at) VALUES(?,?,0,?,datetime('now'))
		ON CONFLICT(owner,path) DO UPDATE SET content=excluded.content, updated_at=datetime('now')`, user, p, req.Content); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.db.Audit(user, "fs.write", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "write"})
	writeJSON(w, ok())
}

// Mkdir POST /api/fs/mkdir {path}
func (f *FS) Mkdir(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ Path string }
	jsonDecode(r, &req)
	p, err := clean(user, req.Path)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if !f.rowBudget(user) {
		http.Error(w, "文件数量已达上限（2 万条），请清理后再试", 400)
		return
	}
	f.ensureDirs(user, p+"/x")
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, p)
	f.db.Audit(user, "fs.mkdir", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "mkdir"})
	writeJSON(w, ok())
}

// Touch POST /api/fs/touch {path}
func (f *FS) Touch(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ Path, Content string }
	jsonDecode(r, &req)
	p, err := clean(user, req.Path)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if !f.rowBudget(user) {
		http.Error(w, "文件数量已达上限（2 万条），请清理后再试", 400)
		return
	}
	// INSERT OR IGNORE：已存在时不改内容，配额按净增量校验（扣除现内容长度）
	var oldLen int64
	f.db.QueryRow(`SELECT LENGTH(content) FROM files WHERE owner=? AND path=?`, user, p).Scan(&oldLen)
	if f.quotaUsed(user)-oldLen+int64(len(req.Content)) > store.StorageQuota {
		http.Error(w, "个人空间已满（配额 100MB）", 400)
		return
	}
	f.ensureDirs(user, p)
	f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir,content) VALUES(?,?,0,?)`, user, p, req.Content)
	f.db.Audit(user, "fs.touch", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "create"})
	writeJSON(w, ok())
}

// Remove DELETE /api/fs/remove?path= （递归）
func (f *FS) Remove(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil || p == "/" {
		http.Error(w, "非法路径", 403)
		return
	}
	f.db.Exec(`DELETE FROM files WHERE owner=? AND (path=? OR path LIKE ?)`, user, p, p+"/%")
	f.db.Audit(user, "fs.remove", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "remove"})
	writeJSON(w, ok())
}

// rowBudget 该用户文件行数是否还有余量（touch/mkdir 防行数炸弹，
// 绕过字节数配额的旧漏洞——安全审计 2026-10）。
func (f *FS) rowBudget(user string) bool {
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner=?`, user).Scan(&n); err != nil {
		return true // 统计失败不阻断正常路径
	}
	return n < maxFileRows
}

// Rename POST /api/fs/rename {from,to}
func (f *FS) Rename(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ From, To string }
	jsonDecode(r, &req)
	from, err1 := clean(user, req.From)
	to, err2 := clean(user, req.To)
	if err1 != nil || err2 != nil {
		http.Error(w, "非法路径", 403)
		return
	}
	rows, _ := f.db.Query(`SELECT path,is_dir,content FROM files WHERE owner=? AND (path=? OR path LIKE ?)`, user, from, from+"/%")
	type entry struct {
		p    string
		dir  bool
		body string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var d int
		rows.Scan(&e.p, &d, &e.body)
		e.dir = d == 1
		entries = append(entries, e)
	}
	rows.Close()
	for _, e := range entries {
		np := to + strings.TrimPrefix(e.p, from)
		f.db.Exec(`UPDATE files SET path=?, updated_at=datetime('now') WHERE owner=? AND path=?`, np, user, e.p)
	}
	f.db.Audit(user, "fs.rename", from+" → "+to, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": to, "kind": "rename"})
	writeJSON(w, ok())
}

// Search GET /api/fs/search?q=&limit= 按名称子串递归搜索用户全部文件（聚焦搜索用）。
// 排序：精确匹配 > 前缀匹配 > 包含；同级目录在前、路径短者在前；隐藏文件（. 开头）不参与。
func (f *FS) Search(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if q == "" {
		writeJSON(w, []store.File{})
		return
	}
	lq := strings.ToLower(q)
	rows, err := f.db.Query(`SELECT path,is_dir,is_binary,content,updated_at FROM files WHERE owner=? AND path LIKE ?`,
		user, "%"+strings.ReplaceAll(lq, "%", "")+"%")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	type hit struct {
		f    store.File
		rank int
	}
	hits := []hit{}
	for rows.Next() {
		var e store.File
		var isDir, isBin int
		if err := rows.Scan(&e.Path, &isDir, &isBin, &e.Content, &e.UpdatedAt); err != nil {
			continue
		}
		e.IsDir = isDir == 1
		e.IsBinary = isBin == 1
		name := strings.ToLower(path.Base(e.Path))
		if name != lq && !strings.Contains(name, lq) {
			continue // LIKE 命中的是路径中段而非文件名
		}
		if strings.HasPrefix(path.Base(e.Path), ".") {
			continue // 隐藏文件（含 .Trash）不参与搜索
		}
		e.Size = len(e.Content)
		if e.IsBinary && e.Size > 0 {
			e.Size = e.Size * 3 / 4
		}
		e.Content = ""
		rank := 2
		if strings.HasPrefix(name, lq) {
			rank = 1
		}
		if name == lq {
			rank = 0
		}
		hits = append(hits, hit{e, rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if hits[i].f.IsDir != hits[j].f.IsDir {
			return hits[i].f.IsDir
		}
		if len(hits[i].f.Path) != len(hits[j].f.Path) {
			return len(hits[i].f.Path) < len(hits[j].f.Path)
		}
		return hits[i].f.Path < hits[j].f.Path
	})
	out := []store.File{}
	for _, h := range hits {
		out = append(out, h.f)
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, out)
}

// Copy POST /api/fs/copy {from,to} 复制文件或目录（含子树）；目标已存在时返回 409。
func (f *FS) Copy(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ From, To string }
	jsonDecode(r, &req)
	from, err1 := clean(user, req.From)
	to, err2 := clean(user, req.To)
	if err1 != nil || err2 != nil || from == to {
		http.Error(w, "非法路径", 403)
		return
	}
	// 源不存在
	var srcCount int
	f.db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner=? AND (path=? OR path LIKE ?)`, user, from, from+"/%").Scan(&srcCount)
	if srcCount == 0 {
		http.Error(w, "源文件不存在", 404)
		return
	}
	// 目标已存在
	var dstCount int
	f.db.QueryRow(`SELECT COUNT(*) FROM files WHERE owner=? AND path=?`, user, to).Scan(&dstCount)
	if dstCount > 0 {
		http.Error(w, "目标已存在", 409)
		return
	}
	// 配额：复制会增加全量子树内容
	var total int64
	f.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(content)),0) FROM files WHERE owner=? AND (path=? OR path LIKE ?)`,
		user, from, from+"/%").Scan(&total)
	if f.quotaUsed(user)+total > store.StorageQuota {
		http.Error(w, "个人空间不足（配额 100MB）", 400)
		return
	}
	rows, _ := f.db.Query(`SELECT path,is_dir,is_binary,content FROM files WHERE owner=? AND (path=? OR path LIKE ?)`, user, from, from+"/%")
	type entry struct {
		p, body string
		dir     bool
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var d, b int
		rows.Scan(&e.p, &d, &b, &e.body)
		e.dir = d == 1
		_ = b
		entries = append(entries, e)
	}
	rows.Close()
	for _, e := range entries {
		np := to + strings.TrimPrefix(e.p, from)
		if e.dir {
			f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, np)
		} else {
			f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,content,updated_at) VALUES(?,?,?,datetime('now'))`, user, np, e.body)
		}
	}
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": to, "kind": "copy"})
	writeJSON(w, ok())
}

func (f *FS) ensureDirs(user, file string) {
	dir := path.Dir(file)
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	cur := ""
	for _, s := range segs {
		cur += "/" + s
		f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, cur)
	}
}

// ---- 终端（事件总线 topic: term:<user>）----

func (f *FS) onTerminal(env bus.Envelope, reply func(string, any)) {
	if f.disabled {
		return // 防御：正常情况下 New 不会注册该处理器
	}
	topic := env.Topic
	user := strings.TrimPrefix(topic, "term:")
	isReal := strings.HasSuffix(topic, ":real")
	if isReal {
		user = strings.TrimSuffix(user, ":real")
	}
	if user == "" || env.From != user {
		return
	}
	var cmd string
	if err := json.Unmarshal(env.Payload, &cmd); err != nil {
		return
	}
	if isReal {
		f.execReal(user, cmd, reply)
		return
	}
	f.mu.Lock()
	cwd := f.cwd[user]
	f.mu.Unlock()
	if cwd == "" {
		cwd = homeOf(user)
	}
	out, next := f.exec(user, cwd, cmd)
	if next != "" {
		cwd = next
	}
	f.mu.Lock()
	f.cwd[user] = cwd
	f.mu.Unlock()
	if out != "" {
		reply("term:"+user+":out", map[string]any{"cwd": cwd, "text": out})
	}
}

// ---- 真实终端（受控执行） ----

func (f *FS) realHome(user string) string {
	return filepath.Join(f.homesDir, filepath.Base(user))
}

func (f *FS) ensureRealHome(user string) string {
	home := f.realHome(user)
	os.MkdirAll(home, 0o755)
	return home
}

func (f *FS) execReal(user, line string, reply func(string, any)) {
	if !f.realShell {
		reply("term:"+user+":real:out", map[string]any{"cwd": "", "text": "真实终端未启用（OPENOS_REAL_SHELL=on 受限沙箱 / =full 解除目录限制）"})
		return
	}
	home := f.ensureRealHome(user)
	f.realMu.Lock()
	cwd := f.realCwd[user]
	f.realMu.Unlock()
	if cwd == "" || !strings.HasPrefix(cwd, home) {
		cwd = home
	}
	homeName := "~"

	line = strings.TrimSpace(line)
	if line == "" {
		reply("term:"+user+":real:out", map[string]any{"cwd": cwd, "home": home, "text": ""})
		return
	}
	if line == "clear" {
		reply("term:"+user+":real:out", map[string]any{"cwd": cwd, "home": home, "text": "\x00CLEAR"})
		return
	}
	// cd 单命令：服务端跟踪工作目录（受限在个人沙箱内）
	if strings.HasPrefix(line, "cd ") || line == "cd" {
		arg := strings.TrimSpace(strings.TrimPrefix(line, "cd"))
		var target string
		switch {
		case arg == "" || arg == "~":
			target = home
		case strings.HasPrefix(arg, "~/"):
			target = filepath.Join(home, strings.TrimPrefix(arg, "~/"))
		case filepath.IsAbs(arg):
			target = filepath.Clean(arg)
		default:
			target = filepath.Join(cwd, arg)
		}
		// 解析符号链接后校验仍在沙箱内
		if resolved, err := filepath.EvalSymlinks(target); err == nil {
			target = resolved
		}
		if !strings.HasPrefix(target, home) {
			reply("term:"+user+":real:out", map[string]any{"cwd": cwd, "home": home, "text": "cd: 受限模式仅允许在个人沙箱内移动：" + homeName + "（OPENOS_REAL_SHELL=full 可解除）"})
			return
		}
		if st, err := os.Stat(target); err != nil || !st.IsDir() {
			reply("term:"+user+":real:out", map[string]any{"cwd": cwd, "home": home, "text": "cd: 目录不存在: " + target})
			return
		}
		f.realMu.Lock()
		f.realCwd[user] = target
		f.realMu.Unlock()
		reply("term:"+user+":real:out", map[string]any{"cwd": target, "home": home, "text": ""})
		return
	}

	// 沙箱护栏：工作目录不在沙箱时拉回（full 模式除外）
	full := os.Getenv("OPENOS_REAL_SHELL") == "full"
	if !full && !strings.HasPrefix(cwd, home) {
		cwd = home
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 敏感操作留痕：命令原文（截断）入审计，无论退出码
	d := line
	if len(d) > 300 {
		d = d[:300] + "…"
	}
	f.db.Audit(user, "term.real", d, "")
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	cmd.Dir = cwd
	// 输出合并 + 截断
	var buf limitedBuf
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	// 环境白名单化：绝不继承服务进程秘密（OPENOS_* 密钥等），full 模式同样清洗
	cmd.Env = safeenv.Env("HOME="+home, "TERM=xterm-256color")
	err := cmd.Run()
	out := buf.String()
	if ctx.Err() == context.DeadlineExceeded {
		out += "\n⏱ 命令超过 10s 已终止"
	}
	if err != nil && ctx.Err() == nil {
		if ee, ok := err.(*exec.ExitError); ok {
			// 非零退出码不算异常，输出已含 stderr
			_ = ee
		} else {
			out += "\n执行失败: " + err.Error()
		}
	}
	f.realMu.Lock()
	f.realCwd[user] = cwd
	f.realMu.Unlock()
	reply("term:"+user+":real:out", map[string]any{"cwd": cwd, "home": home, "text": out, "truncated": buf.truncated})
}

// limitedBuf 输出上限 64KB 的缓冲。
type limitedBuf struct {
	b         bytes.Buffer
	truncated bool
}

func (l *limitedBuf) Write(p []byte) (int, error) {
	const max = 64 * 1024
	if l.b.Len() >= max {
		l.truncated = true
		return len(p), nil // 丢弃
	}
	if l.b.Len()+len(p) > max {
		l.b.Write(p[:max-l.b.Len()])
		l.truncated = true
		return len(p), nil
	}
	return l.b.Write(p)
}

func (l *limitedBuf) String() string {
	if l.truncated {
		return l.b.String() + "\n…（输出超过 64KB 已截断）"
	}
	return l.b.String()
}

func (f *FS) exec(user, cwd, line string) (string, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", cwd
	}
	fields := splitQuoted(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "help":
		return `OpenOS 虚拟终端 —— 可用命令：
  pwd / ls [路径] / cd <路径> / cat <文件>
  echo <文本> [> 文件]        追加请用 >>
  touch <文件> / mkdir <目录> / rm <路径>
  open <app id>               在桌面打开一个应用
  apps                        列出可用应用
  goder <问题>                向 goder AI 提问
  whoami / date / uname / clear / help`, cwd
	case "pwd":
		return cwd, cwd
	case "whoami":
		return user, cwd
	case "uname":
		return "OpenOS 1.0 (web) go/virtual-shell", cwd
	case "date":
		return time.Now().Format("2006-01-02 15:04:05 Mon"), cwd
	case "ls":
		target := cwd
		if len(args) > 0 {
			p := args[0]
			if !strings.HasPrefix(p, "/") {
				p = cwd + "/" + p
			}
			cp, err := clean(user, p)
			if err != nil {
				return err.Error(), cwd
			}
			target = cp
		}
		return f.lsOutput(user, target), cwd
	case "cd":
		target := homeOf(user)
		if len(args) > 0 {
			p := args[0]
			if !strings.HasPrefix(p, "/") {
				p = cwd + "/" + p
			}
			cp, err := clean(user, p)
			if err != nil {
				return err.Error(), cwd
			}
			target = cp
		}
		var isDir int
		if err := f.db.QueryRow(`SELECT is_dir FROM files WHERE owner=? AND path=?`, user, target).Scan(&isDir); err != nil || isDir != 1 {
			return "cd: 目录不存在: " + target, cwd
		}
		return "", target
	case "cat":
		if len(args) == 0 {
			return "用法: cat <文件>", cwd
		}
		p := resolve(cwd, args[0])
		cp, err := clean(user, p)
		if err != nil {
			return err.Error(), cwd
		}
		var content string
		var isDir, isBin int
		if err := f.db.QueryRow(`SELECT content,is_dir,is_binary FROM files WHERE owner=? AND path=?`, user, cp).Scan(&content, &isDir, &isBin); err != nil {
			return "cat: 文件不存在", cwd
		}
		if isDir == 1 {
			return "cat: 这是一个目录", cwd
		}
		if isBin == 1 {
			return fmt.Sprintf("cat: %s（二进制文件 %d 字节，请在文件应用中预览/下载）", path.Base(cp), len(content)*3/4), cwd
		}
		if content == "" {
			return "(空文件)", cwd
		}
		return content, cwd
	case "echo":
		if len(args) == 0 {
			return "", cwd
		}
		if idx := indexOf(args, ">"); idx >= 0 && idx+1 < len(args) {
			return f.writeShell(user, cwd, args[idx+1], strings.Join(args[:idx], " ")+"\n", false), cwd
		}
		if idx := indexOf(args, ">>"); idx >= 0 && idx+1 < len(args) {
			return f.writeShell(user, cwd, args[idx+1], strings.Join(args[:idx], " ")+"\n", true), cwd
		}
		return strings.Join(args, " "), cwd
	case "touch":
		if len(args) == 0 {
			return "用法: touch <文件>", cwd
		}
		p := resolve(cwd, args[0])
		cp, err := clean(user, p)
		if err != nil {
			return err.Error(), cwd
		}
		f.ensureDirs(user, cp)
		f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,0)`, user, cp)
		return "", cwd
	case "mkdir":
		if len(args) == 0 {
			return "用法: mkdir <目录>", cwd
		}
		p := resolve(cwd, args[0])
		cp, err := clean(user, p)
		if err != nil {
			return err.Error(), cwd
		}
		f.ensureDirs(user, cp+"/x")
		f.db.Exec(`INSERT OR IGNORE INTO files(owner,path,is_dir) VALUES(?,?,1)`, user, cp)
		return "", cwd
	case "rm":
		if len(args) == 0 {
			return "用法: rm <路径>", cwd
		}
		p := resolve(cwd, args[0])
		cp, err := clean(user, p)
		if err != nil {
			return err.Error(), cwd
		}
		f.db.Exec(`DELETE FROM files WHERE owner=? AND (path=? OR path LIKE ?)`, user, cp, cp+"/%")
		return "", cwd
	case "apps":
		apps, _ := f.db.ListAppsForRole(roleOf(f.db, user))
		var sb strings.Builder
		for _, a := range apps {
			fmt.Fprintf(&sb, "%-12s %s\n", a.ID, a.Name)
		}
		return sb.String(), cwd
	case "open":
		if len(args) == 0 {
			return "用法: open <app id>", cwd
		}
		f.hub.PublishTo(user, "sys:open", map[string]any{"app": args[0]})
		return "正在打开 " + args[0], cwd
	case "goder":
		if f.ai == nil {
			return "goder 未配置 AI 服务", cwd
		}
		q := strings.Join(args, " ")
		if q == "" {
			return "用法: goder <问题>", cwd
		}
		return f.ai("terminal", q), cwd
	case "clear":
		return "\x00CLEAR", cwd
	}
	return cmd + ": 命令未找到（输入 help 查看可用命令）", cwd
}

func (f *FS) writeShell(user, cwd, file, text string, appendMode bool) string {
	p := resolve(cwd, file)
	cp, err := clean(user, p)
	if err != nil {
		return err.Error()
	}
	f.ensureDirs(user, cp)
	if appendMode {
		var old string
		f.db.QueryRow(`SELECT content FROM files WHERE owner=? AND path=?`, user, cp).Scan(&old)
		text = old + text
	}
	f.db.Exec(`INSERT INTO files(owner,path,is_dir,content,updated_at) VALUES(?,?,0,?,datetime('now'))
		ON CONFLICT(owner,path) DO UPDATE SET content=excluded.content, updated_at=datetime('now')`, user, cp, text)
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": cp, "kind": "write"})
	return ""
}

func (f *FS) lsOutput(user, dir string) string {
	rows, err := f.db.Query(`SELECT path,is_dir FROM files WHERE owner=?`, user)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	prefix := strings.TrimSuffix(dir, "/") + "/"
	var dirs, files []string
	for rows.Next() {
		var p string
		var d int
		rows.Scan(&p, &d)
		rest := strings.TrimPrefix(p, prefix)
		if rest == "" || p == dir || strings.Contains(rest, "/") {
			continue
		}
		if d == 1 {
			dirs = append(dirs, rest+"/")
		} else {
			files = append(files, rest)
		}
	}
	if len(dirs)+len(files) == 0 {
		return "(空目录)"
	}
	sort.Strings(dirs)
	sort.Strings(files)
	return strings.Join(append(dirs, files...), "  ")
}

func resolve(cwd, p string) string {
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}
	return path.Clean(p)
}

func roleOf(db *store.DB, user string) string {
	var role string
	db.QueryRow(`SELECT role FROM users WHERE username=?`, user).Scan(&role)
	return role
}

func splitQuoted(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func jsonDecode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func ok() map[string]string { return map[string]string{"ok": "1"} }
