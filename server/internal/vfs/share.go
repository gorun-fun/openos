// 文件分享链接：把 home 内的单个文件生成限时分享（vfs 内的跨用户共享通道）。
//
// 模型：128bit 随机 id 即能力（不可枚举，同附件服务），消费端点在 authed mux 上
// （分享 id 是授权凭据、登录态是身份门槛，二者缺一不可——不采用 query token，
// 前端经 #share=<id> 片段路由拉起接收弹窗）。下载恒 attachment + nosniff（分享
// 服务不可变成 HTML 注入源）；「存入我的文档」走 SaveToDocs（配额/幂等/fs:changed
// 广播全部继承）。到期由 scheduleRetention 清理；底稿文件被删/改名后 info 返回
// 404，链接自然失聪并在到期时回收。
package vfs

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"openos/server/internal/auth"
)

const (
	shareDefaultDays = 7
	shareMaxDays     = 30
	// SQLite datetime('now') 的文本格式（UTC）：expires_at 全链路统一用它，
	// 与 SQL 内的字符串比较保持同一语义
	sqliteTime = "2006-01-02 15:04:05"
)

// CreateShare POST /api/fs/shares {path, days?} —— 为单个文件创建分享链接。
func (f *FS) CreateShare(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	var req struct {
		Path string `json:"path"`
		Days int    `json:"days"`
	}
	if err := jsonDecode(r, &req); err != nil || req.Path == "" {
		http.Error(w, "参数错误", 400)
		return
	}
	p, err := clean(user, req.Path)
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	var isDir, isBin int
	var content string
	if err := f.db.QueryRow(`SELECT is_dir,is_binary,content FROM files WHERE owner=? AND path=?`, user, p).Scan(&isDir, &isBin, &content); err != nil {
		http.Error(w, "文件不存在", 404)
		return
	}
	if isDir == 1 {
		http.Error(w, "目录不能分享", 400)
		return
	}
	size := int64(len(content))
	if isBin == 1 {
		if decoded, err := base64.StdEncoding.DecodeString(content); err == nil {
			size = int64(len(decoded))
		}
	}
	if req.Days <= 0 {
		req.Days = shareDefaultDays
	}
	if req.Days > shareMaxDays {
		req.Days = shareMaxDays
	}
	buf := make([]byte, 16)
	rand.Read(buf)
	id := hex.EncodeToString(buf)
	// 与 datetime('now') 同格式（空格分隔）：字符串比较才与时间语义一致
	expires := time.Now().UTC().AddDate(0, 0, req.Days).Format(sqliteTime)
	if _, err := f.db.Exec(`INSERT INTO shares(id,owner,path,name,size,created_at,expires_at) VALUES(?,?,?,?,?,datetime('now'),?)`,
		id, user, p, path.Base(p), size, expires); err != nil {
		http.Error(w, "创建分享失败", 500)
		return
	}
	f.db.Audit(user, "fs.share", p, "")
	writeJSON(w, map[string]any{"id": id, "url": "/#share=" + id, "expiresAt": expires, "days": req.Days})
}

// ListShares GET /api/fs/shares —— 我创建的、仍在有效期内的分享。
func (f *FS) ListShares(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	rows, err := f.db.Query(`SELECT id,path,name,size,created_at,expires_at FROM shares WHERE owner=? AND expires_at>datetime('now') ORDER BY created_at DESC`, user)
	if err != nil {
		http.Error(w, "读取分享失败", 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, p, name, created, expires string
		var size int64
		if err := rows.Scan(&id, &p, &name, &size, &created, &expires); err != nil {
			continue
		}
		out = append(out, map[string]any{"id": id, "path": p, "name": name, "size": size, "createdAt": created, "expiresAt": expires})
	}
	writeJSON(w, out) // 恒为数组（前端按数组消费）
}

// RevokeShare DELETE /api/fs/shares/{id} —— 撤销自己的分享（仅 owner）。
func (f *FS) RevokeShare(w http.ResponseWriter, r *http.Request) {
	user := auth.FromContext(r).Username
	id := r.PathValue("id")
	var owner string
	if err := f.db.QueryRow(`SELECT owner FROM shares WHERE id=?`, id).Scan(&owner); err != nil {
		http.Error(w, "分享不存在", 404)
		return
	}
	if owner != user {
		http.Error(w, "只能撤销自己的分享", 403)
		return
	}
	f.db.Exec(`DELETE FROM shares WHERE id=?`, id)
	f.db.Audit(user, "fs.share-revoke", id, "")
	writeJSON(w, ok())
}

// loadShare 取有效期内的分享并核对底稿文件仍在（被删/改名即失效）。
func (f *FS) loadShare(id string) (owner, p string, size int64, err error) {
	var name string
	var expires string
	if err = f.db.QueryRow(`SELECT owner,path,name,size,expires_at FROM shares WHERE id=?`, id).Scan(&owner, &p, &name, &size, &expires); err != nil {
		return "", "", 0, fmt.Errorf("分享不存在或已失效")
	}
	if exp, e := time.Parse(sqliteTime, expires); e != nil || time.Now().After(exp) {
		return "", "", 0, fmt.Errorf("分享已过期")
	}
	var isDir int
	if e := f.db.QueryRow(`SELECT is_dir FROM files WHERE owner=? AND path=?`, owner, p).Scan(&isDir); e != nil || isDir == 1 {
		return "", "", 0, fmt.Errorf("原文件已被删除或移动")
	}
	return owner, p, size, nil
}

// ShareInfo GET /api/share/{id} —— 接收弹窗展示元信息（登录即可，分享 id 即能力）。
func (f *FS) ShareInfo(w http.ResponseWriter, r *http.Request) {
	owner, p, size, err := f.loadShare(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	writeJSON(w, map[string]any{"path": p, "name": path.Base(p), "owner": owner, "size": size})
}

// ShareDownload GET /api/share/{id} —— 下载内容。恒 attachment + nosniff：
// 分享链接可能进入聊天/外部场景，绝不内联渲染（防 HTML/SVG 注入）。
func (f *FS) ShareDownload(w http.ResponseWriter, r *http.Request) {
	owner, p, _, err := f.loadShare(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	data, ok := f.readShareFile(owner, p)
	if !ok {
		http.Error(w, "原文件已被删除或移动", 404)
		return
	}
	name := path.Base(p)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", asciiFilename(name), url.PathEscape(name)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// ShareSave POST /api/share/{id} —— 存入我的「文档」（SaveToDocs：配额/幂等/广播）。
func (f *FS) ShareSave(w http.ResponseWriter, r *http.Request) {
	me := auth.FromContext(r).Username
	owner, p, _, err := f.loadShare(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	data, ok := f.readShareFile(owner, p)
	if !ok {
		http.Error(w, "原文件已被删除或移动", 404)
		return
	}
	target, err := f.SaveToDocs(me, path.Base(p), data)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"path": target})
}

// readShareFile 读底稿文件字节（二进制做 base64 还原）。
func (f *FS) readShareFile(owner, p string) ([]byte, bool) {
	var content string
	var isBin int
	if err := f.db.QueryRow(`SELECT content,is_binary FROM files WHERE owner=? AND path=?`, owner, p).Scan(&content, &isBin); err != nil {
		return nil, false
	}
	data := []byte(content)
	if isBin == 1 {
		if decoded, err := base64.StdEncoding.DecodeString(content); err == nil {
			data = decoded
		}
	}
	return data, true
}

// CleanupExpiredShares 到期分享清理（scheduleRetention 周期调用）。
func (f *FS) CleanupExpiredShares() {
	_, _ = f.db.Exec(`DELETE FROM shares WHERE expires_at<=datetime('now')`)
}

// asciiFilename Content-Disposition filename= 回退段只留 ASCII 安全字符（同 main.go 附件）。
func asciiFilename(name string) string {
	var b strings.Builder
	for _, c := range name {
		if c >= 0x20 && c < 0x7f && c != '"' && c != '\\' {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return "file"
	}
	return b.String()
}
