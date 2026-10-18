// Package vfs 提供挂在 <data>/homes/<user> 上的真实文件系统（REST，虚拟路径
// /home/<user> 对外呈现）与受控执行的真实终端。文件管理、真实终端、goder 项目
// 共用同一份磁盘 home（统一 home，2026-10 起替代 SQLite files 表虚拟存储）。
package vfs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"openos/server/internal/webres"
)

// 单文件上限（上传与写入统一校验）。
const maxFileSize = 8 << 20

// maxFileRows 每用户文件数上限：touch/mkdir 曾不做配额检查，可无限建条目
// 绕过 100MB 配额（inode 膨胀 DoS，安全审计 2026-10）。条数与配额双重设限。
const maxFileRows = 20000

// 按扩展名判定是否按文本处理（其余一律按二进制：Read 时 base64 编码返回）。
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

// vtime 时间展示格式：沿用旧 SQLite datetime('now') 文本形态（UTC、空格分隔），
// 前端对 updatedAt 只做字符串展示与字典序排序，字典序与该格式的时间序一致。
func vtime(t time.Time) string { return t.UTC().Format(sqliteTime) }

// File 虚拟文件系统中的条目（REST 展示结构）。存储已在统一 home 磁盘上，
// 此 DTO 仅服务 JSON 契约——原住 store 包，VFS 磁盘化后随语义迁入本包。
type File struct {
	Path      string `json:"path"`
	IsDir     bool   `json:"isDir"`
	IsBinary  bool   `json:"isBinary"`
	Content   string `json:"content,omitempty"`
	Size      int    `json:"size"`
	UpdatedAt string `json:"updatedAt"`
}

// FS 文件系统服务。
type FS struct {
	db  *store.DB
	hub *bus.Hub

	// 真实终端（受控执行：sh -c，10s 超时，输出截断，工作目录限定个人沙箱）
	realShell     bool
	realShellFull bool // OPENOS_REAL_SHELL=full：解除目录限制（New 时一次算好，勿在命令路径重读环境变量）
	realMu        sync.Mutex
	realCwd       map[string]string
	homesDir      string

	// disabled：OPENOS_VFS=off 时整体关闭（宿主模式——业务文件交由集成方管理，
	// OpenOS 只保留演示数据与界面设置）；seedDemo：OPENOS_SEED_DEMO=0 时不种示例文件
	disabled bool
	seedDemo bool
}

// New 创建服务。dataDir 为统一 home 根（<dataDir>/homes/<user>）：
// 文件管理、真实终端与 goder 项目目录共用同一份磁盘文件树。
func New(dataDir string, db *store.DB, hub *bus.Hub) *FS {
	// 真实 shell 默认关闭（安全审计 2026-10：sh -c 以服务进程权限执行任意命令，
	// 曾默认开启且继承全部环境变量——秘密可被读取自我提权）。on=受限沙箱执行、
	// full=解除目录限制；独立演示部署在 compose/run.sh 显式开 on。
	v := os.Getenv("OPENOS_REAL_SHELL")
	realShell := v == "on" || v == "full"
	fs := &FS{
		db: db, hub: hub,
		realCwd: map[string]string{},
		realShell: realShell, realShellFull: v == "full",
		homesDir: filepath.Join(dataDir, "homes"),
		disabled: os.Getenv("OPENOS_VFS") == "off",
		seedDemo: os.Getenv("OPENOS_SEED_DEMO") != "0",
	}
	if !fs.disabled {
		hub.Handle("term:*", fs.onTerminal)
		fs.migrateFromDB()
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

// DeleteHome 删除用户的统一 home 目录（删用户时级联清理，含全部文件与 goder 项目）。
func (f *FS) DeleteHome(user string) {
	_ = os.RemoveAll(filepath.Join(f.homesDir, filepath.Base(user)))
}

// ---- 虚拟路径 ↔ 真实磁盘路径 ----

func homeOf(user string) string { return "/home/" + user }

// clean 规范化并校验路径必须位于用户 home 内。
func clean(user, p string) (string, error) {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if p == "/" {
		return "/", nil
	}
	home := homeOf(user)
	// 前缀比较必须带 / 边界：user=adm 时 /home/admin/x 不允许通过
	//（SQL 时代即有此防御，磁盘化后是唯一的越权闸门）
	if p != home && !strings.HasPrefix(p, home+"/") {
		return "", fmt.Errorf("权限不足：只能访问 %s 下的文件", home)
	}
	return p, nil
}

// realHome 用户统一 home 的真实磁盘绝对路径。
func (f *FS) realHome(user string) string {
	return filepath.Join(f.homesDir, filepath.Base(user))
}

// realPath 虚拟路径 → 真实磁盘路径（不做存在性与符号链接校验，调用方须经 resolveReal）。
func (f *FS) realPath(user, p string) string {
	rel := strings.TrimPrefix(p, homeOf(user)) // "" 或 "/x/y"
	return filepath.Join(f.realHome(user), filepath.FromSlash(rel))
}

// resolveReal 校验虚拟路径映射的真实路径并解析符号链接，拒绝越出个人 home
// （终端可 ln -s 指向 home 外，REST 读写若直接跟随会变成任意文件读写通道）。
// 路径不存在时解析最深存在的父链后拼回余段，返回校验通过的路径（不保证存在）。
func (f *FS) resolveReal(user, p string) (string, error) {
	home := f.realHome(user)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	real := f.realPath(user, p)
	resolved := real
	if r, err := filepath.EvalSymlinks(real); err == nil {
		resolved = r
	} else {
		// 路径（或中间目录）尚不存在：逐级上溯解析最深存在的祖先——home 本身
		// 已确保存在必然命中——再拼回余段；存在段含越界符号链接会在解析处暴露
		dir, rest := filepath.Dir(real), filepath.Base(real)
		for {
			if r, err := filepath.EvalSymlinks(dir); err == nil {
				resolved = filepath.Join(r, rest)
				break
			}
			rest = filepath.Join(filepath.Base(dir), rest)
			parent := filepath.Dir(dir)
			if parent == dir {
				return "", err // 理论不可达（home 已建）
			}
			dir = parent
		}
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("非法路径（符号链接越界）")
	}
	return resolved, nil
}

// dirEmptyExcept 目录不存在或除排除名外无任何条目（种子判定用，.Trash 每次登录补齐不算内容）。
func dirEmptyExcept(dir, except string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Name() != except {
			return false
		}
	}
	return true
}

// duOf 遍历用户 home 统计字节总量与文件条数（配额与行数预算共用；不跟随符号链接）。
func (f *FS) duOf(user string) (int64, int) {
	var size int64
	var files int
	_ = filepath.WalkDir(f.realHome(user), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			size += info.Size()
		}
		files++
		return nil
	})
	return size, files
}

func (f *FS) quotaUsed(user string) int64 {
	size, _ := f.duOf(user)
	return size
}

// rowBudget 该用户文件条数是否还有余量（touch/mkdir 防条目炸弹）。
func (f *FS) rowBudget(user string) bool {
	_, files := f.duOf(user)
	return files < maxFileRows
}

// DiskStats 系统统计：全部用户 home 的文件总数与字节总量（管理端 /api/stats 用，
// 与配额同源的 duOf 口径——同一遍历实现，避免统计与配额两套算法漂移）。
func (f *FS) DiskStats() (int, int64) {
	users, err := os.ReadDir(f.homesDir)
	if err != nil {
		return 0, 0
	}
	var files int
	var size int64
	for _, u := range users {
		if !u.IsDir() {
			continue
		}
		s, n := f.duOf(u.Name())
		files += n
		size += s
	}
	return files, size
}

// ---- 存量迁移（SQLite files 表 → 磁盘 home，一次性） ----

// migrateFromDB 旧虚拟文件系统存储（SQLite files 表）迁移到统一 home 目录。
// 幂等可重放（照 migrateSystemKV「写成功才删行」先例）：逐用户判断，磁盘 home
// 除 .Trash 外已有内容则跳过（磁盘为权威）；全部写出成功后才删除该用户的表行。
// files 表 schema 保留但此后不再读写。OPENOS_VFS=off 跳过（宿主模式无此数据）。
func (f *FS) migrateFromDB() {
	rows, err := f.db.Query(`SELECT DISTINCT owner FROM files`)
	if err != nil {
		return
	}
	var owners []string
	for rows.Next() {
		var o string
		if rows.Scan(&o) == nil && o != "" {
			owners = append(owners, o)
		}
	}
	rows.Close()
	for _, owner := range owners {
		if !dirEmptyExcept(f.realHome(owner), ".Trash") {
			continue // 磁盘已有内容：保持现状，该用户表行留置（不再消费）
		}
		if f.migrateUser(owner) {
			f.db.Exec(`DELETE FROM files WHERE owner=?`, owner)
			f.db.Audit("system", "fs.migrate", "owner="+owner, "")
		}
	}
}

func (f *FS) migrateUser(owner string) bool {
	rows, err := f.db.Query(`SELECT path,is_dir,is_binary,content FROM files WHERE owner=? ORDER BY path`, owner)
	if err != nil {
		return false
	}
	type entry struct {
		p       string
		dir     bool
		content string
	}
	var list []entry
	for rows.Next() {
		var e entry
		var d, b int
		if rows.Scan(&e.p, &d, &b, &e.content) != nil {
			continue
		}
		e.dir = d == 1
		list = append(list, e)
	}
	rows.Close()
	for _, e := range list {
		// 防御：历史脏行（路径越出该用户 home）跳过，不落盘
		cp, err := clean(owner, e.p)
		if err != nil || cp == "/" {
			continue
		}
		real := f.realPath(owner, cp)
		if e.dir {
			if err := os.MkdirAll(real, 0o755); err != nil {
				return false
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			return false
		}
		data := []byte(e.content)
		if b := isTextExt(cp); !b && e.content != "" {
			if decoded, err := base64.StdEncoding.DecodeString(e.content); err == nil {
				data = decoded
			}
		}
		if err := os.WriteFile(real, data, 0o644); err != nil {
			return false
		}
	}
	return true
}

// ---- 系统侧写入 ----

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

	// 幂等：同路径同内容视为已保存
	target := path.Join(dir, name)
	if real, err := f.resolveReal(user, target); err == nil {
		if exist, _ := os.ReadFile(real); string(exist) == string(data) {
			return target, nil
		}
	}
	// 同名不同内容：名 (2).ext 递增
	for i := 2; ; i++ {
		if real, err := f.resolveReal(user, target); err == nil {
			if _, err := os.Lstat(real); os.IsNotExist(err) {
				break
			}
		}
		ext := path.Ext(name)
		target = path.Join(dir, strings.TrimSuffix(name, ext)+" ("+strconv.Itoa(i)+")"+ext)
	}

	if f.quotaUsed(user)+int64(len(data)) > store.StorageQuota {
		return "", errors.New(quotaFullMsg())
	}
	real, err := f.resolveReal(user, target)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(real, data, 0o644); err != nil {
		return "", err
	}
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": target, "kind": "save"})
	return target, nil
}

// EnsureHome 为用户初始化统一 home 目录与示例文件（文件系统整体关闭时跳过；
// OPENOS_SEED_DEMO=0 时只建目录结构，不种欢迎文档与示例表格）。
func (f *FS) EnsureHome(user string) {
	if f.disabled {
		return
	}
	home := f.realHome(user)
	// 废纸篓目录（点前缀：列表/搜索默认隐藏，仅侧栏与 Dock 以专用入口呈现）；
	// 幂等建在种子判定之前，保证老用户每次登录也补齐
	_ = os.MkdirAll(home, 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".Trash"), 0o755)
	if !dirEmptyExcept(home, ".Trash") {
		return
	}
	_ = os.MkdirAll(filepath.Join(home, "文档"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, "表格"), 0o755)
	if !f.seedDemo {
		return
	}
	_ = os.WriteFile(filepath.Join(home, "欢迎.md"),
		[]byte("# 欢迎使用 OpenOS\n\n这是一份示例文档，可由「文档」应用打开编辑。\n\n- 前后端分离：Go + Vite\n- 事件总线通信\n- 多用户多角色\n"), 0o644)
	_ = os.WriteFile(filepath.Join(home, "表格", "收支.csv"),
		[]byte("项目,收入,支出\n工资,12000,0\n房租,0,3500\n餐饮,0,1800\n结余,=SUM(B2:B3)-SUM(C2:C4),\n"), 0o644)
}

// CreateGoderProject 为 goder 创建项目目录（home 下 goder/<name>）并返回真实
// 绝对路径——该目录即 AI 会话的项目上下文（workspace），文件管理与真实终端
// 同目录可见。项目名限单段安全名（拒绝子路径与穿越），已存在返回 os.ErrExist。
func (f *FS) CreateGoderProject(user, name string) (string, error) {
	if f.disabled {
		return "", fmt.Errorf("文件系统未启用")
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > 100 ||
		path.Base(strings.ReplaceAll(name, "\\", "/")) != name || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("无效项目名")
	}
	dir := filepath.Join(f.realHome(user), "goder", name)
	if _, err := os.Lstat(dir); err == nil {
		return "", os.ErrExist
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": homeOf(user) + "/goder/" + name, "kind": "mkdir"})
	// 跨进程交付（gh server 的会话 workspace）必须绝对路径：OPENOS_DATA 可为
	// 相对值，相对路径依赖各进程 cwd 一致，脆弱（旧 goder 默认路径即踩此坑）
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir, nil
}

// HomePath 用户统一 home 的真实绝对路径（goder 裸会话 workspace 兜底等场景）。
func (f *FS) HomePath(user string) string {
	abs, _ := filepath.Abs(f.realHome(user))
	return abs
}

// ---- REST Handlers ----

// List GET /api/fs/list?path=
func (f *FS) List(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if p == "/" {
		// 虚拟根只含 /home 占位（旧 SQLite 行为保持；/home 本身不可进入）
		webres.WriteJSON(w, []File{{Path: "/home", IsDir: true, Size: 0, UpdatedAt: ""}})
		return
	}
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, "路径不存在", 404)
		return
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "文件不存在", 404)
		} else {
			http.Error(w, "这不是一个目录", 400)
		}
		return
	}
	out := []File{}
	for _, e := range entries {
		var size int
		updated := ""
		if info, ie := e.Info(); ie == nil {
			size = int(info.Size())
			updated = vtime(info.ModTime())
		}
		out = append(out, File{
			Path:      path.Join(p, e.Name()),
			IsDir:     e.IsDir(),
			IsBinary:  !e.IsDir() && !isTextExt(e.Name()),
			Size:      size,
			UpdatedAt: updated,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Path < out[j].Path
	})
	webres.WriteJSON(w, out)
}

// Read GET /api/fs/read?path=
func (f *FS) Read(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, "文件不存在", 404)
		return
	}
	data, err := os.ReadFile(real)
	if err != nil {
		http.Error(w, "文件不存在", 404)
		return
	}
	if st, err := os.Stat(real); err == nil && st.IsDir() {
		http.Error(w, "这是一个目录", 400)
		return
	}
	content := string(data)
	if !isTextExt(p) {
		content = base64.StdEncoding.EncodeToString(data)
	}
	webres.WriteJSON(w, map[string]any{"path": p, "content": content, "isBinary": !isTextExt(p)})
}

// Raw GET /api/fs/raw?path= 返回文件原始字节（下载 / 图片 / PDF 预览）。
func (f *FS) Raw(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, "文件不存在", 404)
		return
	}
	data, err := os.ReadFile(real)
	if err != nil {
		http.Error(w, "文件不存在", 404)
		return
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
	webres.WriteJSON(w, map[string]any{"used": f.quotaUsed(user), "quota": store.StorageQuota})
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
		if used+int64(len(data)) > store.StorageQuota {
			http.Error(w, quotaFullMsg(), 400)
			return
		}
		used += int64(len(data))
		real, err := f.resolveReal(user, cp)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(real, data, 0o644); err != nil {
			continue
		}
		saved = append(saved, cp)
		f.db.Audit(user, "fs.upload", cp, "")
		f.hub.PublishTo(user, "fs:changed", map[string]any{"path": cp, "kind": "upload"})
	}
	webres.WriteJSON(w, map[string]any{"ok": "1", "saved": saved})
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
	if len(req.Content) > maxFileSize {
		http.Error(w, "文件不能超过 8MB", 400)
		return
	}
	var oldLen int64
	if real, err := f.resolveReal(user, p); err == nil {
		if st, err := os.Stat(real); err == nil && !st.IsDir() {
			oldLen = st.Size()
		}
	}
	if f.quotaUsed(user)-oldLen+int64(len(req.Content)) > store.StorageQuota {
		http.Error(w, quotaFullMsg(), 400)
		return
	}
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(real, []byte(req.Content), 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.db.Audit(user, "fs.write", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "write"})
	webres.WriteJSON(w, ok())
}

// Mkdir POST /api/fs/mkdir {path}
func (f *FS) Mkdir(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ Path string }
	if err := jsonDecode(r, &req); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	p, err := clean(user, req.Path)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if !f.rowBudget(user) {
		http.Error(w, "文件数量已达上限（2 万条），请清理后再试", 400)
		return
	}
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if st, err := os.Lstat(real); err == nil {
		if st.IsDir() {
			webres.WriteJSON(w, ok()) // 幂等：已存在目录视为成功
			return
		}
		http.Error(w, "同名文件已存在", 400)
		return
	}
	if err := os.MkdirAll(real, 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.db.Audit(user, "fs.mkdir", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "mkdir"})
	webres.WriteJSON(w, ok())
}

// Touch POST /api/fs/touch {path}
func (f *FS) Touch(w http.ResponseWriter, r *http.Request) {
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
	if !f.rowBudget(user) {
		http.Error(w, "文件数量已达上限（2 万条），请清理后再试", 400)
		return
	}
	// 已存在不改内容（INSERT OR IGNORE 旧语义保持），配额按净增量校验
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if _, err := os.Lstat(real); err == nil {
		f.db.Audit(user, "fs.touch", p, "")
		f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "create"})
		webres.WriteJSON(w, ok())
		return
	}
	if f.quotaUsed(user)+int64(len(req.Content)) > store.StorageQuota {
		http.Error(w, quotaFullMsg(), 400)
		return
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(real, []byte(req.Content), 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.db.Audit(user, "fs.touch", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "create"})
	webres.WriteJSON(w, ok())
}

// Remove DELETE /api/fs/remove?path= （递归）
func (f *FS) Remove(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	p, err := clean(user, r.URL.Query().Get("path"))
	if err != nil || p == "/" {
		http.Error(w, "非法路径", 403)
		return
	}
	real, err := f.resolveReal(user, p)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	if err := os.RemoveAll(real); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.db.Audit(user, "fs.remove", p, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": p, "kind": "remove"})
	webres.WriteJSON(w, ok())
}

// Rename POST /api/fs/rename {from,to}
func (f *FS) Rename(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ From, To string }
	if err := jsonDecode(r, &req); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	from, err1 := clean(user, req.From)
	to, err2 := clean(user, req.To)
	if err1 != nil || err2 != nil {
		http.Error(w, "非法路径", 403)
		return
	}
	src, err := f.resolveReal(user, from)
	if err != nil {
		http.Error(w, "非法路径", 403)
		return
	}
	dst, err := f.resolveReal(user, to)
	if err != nil {
		http.Error(w, "非法路径", 403)
		return
	}
	if _, err := os.Lstat(src); err != nil {
		http.Error(w, "源文件不存在", 404)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.Rename(src, dst); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.db.Audit(user, "fs.rename", from+" → "+to, "")
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": to, "kind": "rename"})
	webres.WriteJSON(w, ok())
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
		webres.WriteJSON(w, []File{})
		return
	}
	lq := strings.ToLower(q)
	type hit struct {
		f    File
		rank int
	}
	hits := []hit{}
	_ = filepath.WalkDir(f.realHome(user), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir // 隐藏目录（含 .Trash）整棵不参与
			}
			return nil
		}
		lower := strings.ToLower(name)
		if lower != lq && !strings.Contains(lower, lq) {
			return nil
		}
		var size int
		updated := ""
		if info, ie := d.Info(); ie == nil {
			size = int(info.Size())
			updated = vtime(info.ModTime())
		}
		e := File{
			Path:      homeOf(user) + filepath.ToSlash(strings.TrimPrefix(p, f.realHome(user))),
			IsDir:     d.IsDir(),
			IsBinary:  !d.IsDir() && !isTextExt(name),
			Size:      size,
			UpdatedAt: updated,
		}
		rank := 2
		if strings.HasPrefix(lower, lq) {
			rank = 1
		}
		if lower == lq {
			rank = 0
		}
		hits = append(hits, hit{e, rank})
		return nil
	})
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
	out := []File{}
	for _, h := range hits {
		out = append(out, h.f)
		if len(out) >= limit {
			break
		}
	}
	webres.WriteJSON(w, out)
}

// Copy POST /api/fs/copy {from,to} 复制文件或目录（含子树）；目标已存在时返回 409。
func (f *FS) Copy(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct{ From, To string }
	if err := jsonDecode(r, &req); err != nil {
		http.Error(w, "参数错误", 400)
		return
	}
	from, err1 := clean(user, req.From)
	to, err2 := clean(user, req.To)
	if err1 != nil || err2 != nil || from == to {
		http.Error(w, "非法路径", 403)
		return
	}
	src, err := f.resolveReal(user, from)
	if err != nil {
		http.Error(w, "非法路径", 403)
		return
	}
	if _, err := os.Lstat(src); err != nil {
		http.Error(w, "源文件不存在", 404)
		return
	}
	dst, err := f.resolveReal(user, to)
	if err != nil {
		http.Error(w, "非法路径", 403)
		return
	}
	if _, err := os.Lstat(dst); err == nil {
		http.Error(w, "目标已存在", 409)
		return
	}
	// 配额：复制会增加全量子树内容
	var total int64
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	if f.quotaUsed(user)+total > store.StorageQuota {
		http.Error(w, quotaFullMsg(), 400)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := copyTree(src, dst); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.hub.PublishTo(user, "fs:changed", map[string]any{"path": to, "kind": "copy"})
	webres.WriteJSON(w, ok())
}

// copyTree 递归复制文件/目录（符号链接条目直接跳过——复制链接目标有越界风险）。
func copyTree(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if !st.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ---- 终端（事件总线 topic: term:<user>）----

func (f *FS) onTerminal(env bus.Envelope, reply func(string, any)) {
	if f.disabled {
		return // 防御：正常情况下 New 不会注册该处理器
	}
	user := strings.TrimPrefix(env.Topic, "term:")
	if user == "" || env.From != user {
		return
	}
	var cmd string
	if err := json.Unmarshal(env.Payload, &cmd); err != nil {
		return
	}
	f.execReal(user, cmd, reply)
}

// ---- 真实终端（受控执行） ----

// ensureRealHome 确保用户 home 存在并返回绝对路径（HOME 环境变量交付给 shell
// 子进程，`~` 展开按其拼接——相对值在 run.sh 形态（OPENOS_DATA=data）下会错位）。
func (f *FS) ensureRealHome(user string) string {
	home := f.realHome(user)
	os.MkdirAll(home, 0o755)
	if abs, err := filepath.Abs(home); err == nil {
		return abs
	}
	return home
}

// realCD 解析 cd 单命令的目标目录并做沙箱校验：成功返回新 cwd，不允许时返回
// 错误文案（cwd 原样退回）。与 resolveReal 的边界校验是两套独立实现，不合并：
// cd 面向 shell 会话语义（符号链接解析后按前缀校验），resolveReal 面向 REST
// 读写（最深存在父链逐级解析），两者的失败语义与调用时机都不同。
func (f *FS) realCD(home, cwd, arg string) (string, string) {
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
		return cwd, "cd: 受限模式仅允许在个人沙箱内移动：~（OPENOS_REAL_SHELL=full 可解除）"
	}
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		return cwd, "cd: 目录不存在: " + target
	}
	return target, ""
}

func (f *FS) execReal(user, line string, reply func(string, any)) {
	if !f.realShell {
		reply("term:"+user+":out", map[string]any{"cwd": "", "text": "真实终端未启用（OPENOS_REAL_SHELL=on 受限沙箱 / =full 解除目录限制）"})
		return
	}
	home := f.ensureRealHome(user)
	f.realMu.Lock()
	cwd := f.realCwd[user]
	f.realMu.Unlock()
	if cwd == "" || !strings.HasPrefix(cwd, home) {
		cwd = home
	}
	line = strings.TrimSpace(line)
	if line == "" {
		reply("term:"+user+":out", map[string]any{"cwd": cwd, "home": home, "text": ""})
		return
	}
	if line == "clear" {
		reply("term:"+user+":out", map[string]any{"cwd": cwd, "home": home, "text": "\x00CLEAR"})
		return
	}
	// cd 单命令：服务端跟踪工作目录（受限在个人沙箱内）
	if strings.HasPrefix(line, "cd ") || line == "cd" {
		arg := strings.TrimSpace(strings.TrimPrefix(line, "cd"))
		target, errText := f.realCD(home, cwd, arg)
		if errText != "" {
			reply("term:"+user+":out", map[string]any{"cwd": cwd, "home": home, "text": errText})
			return
		}
		f.realMu.Lock()
		f.realCwd[user] = target
		f.realMu.Unlock()
		reply("term:"+user+":out", map[string]any{"cwd": target, "home": home, "text": ""})
		return
	}

	// 沙箱护栏：工作目录不在沙箱时拉回（full 模式除外）
	full := f.realShellFull
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
	reply("term:"+user+":out", map[string]any{"cwd": cwd, "home": home, "text": out, "truncated": buf.truncated})
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

func jsonDecode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func ok() map[string]string { return map[string]string{"ok": "1"} }

// quotaFullMsg 配额超限的统一提示（动态跟随 StorageQuota 常量，改配额勿手抄文案）。
func quotaFullMsg() string {
	return fmt.Sprintf("个人空间已满（配额 %dMB），请清理后重试", store.StorageQuota>>20)
}
