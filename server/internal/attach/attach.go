// Package attach 系统附件存储：登录用户上传（multipart，单文件 ≤10MB）、
// 按随机 id 下载（id 为 128bit 随机不可枚举，登录即可取）、按保留期清理。
//
// 服务边界：这是平台级通用能力（聊天附件、bundle 应用文件分享等），不做
// 应用级授权绑定；下载鉴权 = 登录态（query token，同 /ws 先例——<img>/<a>
// 标签带不了 Authorization header）。防滥用：单文件大小硬限 + 全局 IP 限流
// + 保留期清理（OPENOS_ATTACH_RETENTION_DAYS，默认 30 天，0=永久）。
//
// XSS 防线：Content-Type 只对图片白名单（png/jpeg/gif/webp/bmp/avif）内联，
// 其余（含 text/html、image/svg+xml——SVG 可携带脚本）一律 application/
// octet-stream + attachment 下载，附件服务不可变成 HTML 注入源。
package attach

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openos/server/internal/store"
)

// MaxSize 单文件大小上限。
const MaxSize = 10 << 20

// inlineMimes 允许浏览器内联显示（<img> 直接渲染）的图片类型；
// SVG 可携带脚本，显式排除、一律按下载处理。
var inlineMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
	"image/avif": true,
}

// Inline 可内联显示（浏览器 <img> 安全）。
func Inline(mime string) bool { return inlineMimes[mime] }

// Meta 附件元数据（store.Attachment 的别名：上传响应与库行同构）。
type Meta = store.Attachment

// Store 附件存储（目录 + 元数据库）。
type Store struct {
	dir string
	db  *store.DB
}

// New 创建存储并确保目录存在。
func New(dir string, db *store.DB) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir, db: db}, nil
}

// FilePath 附件文件落盘路径（id 已限 hex 字符集）。
func (s *Store) FilePath(id string) string { return filepath.Join(s.dir, id) }

// DeleteByUploader 删除某用户的全部附件（元数据行 + 磁盘文件；删号级联用，
// main 注入的 onUserDeleted 钩子调用——auth 包的行级清理够不到磁盘文件）。
func (s *Store) DeleteByUploader(uploader string) {
	if uploader == "" {
		return
	}
	rows, err := s.db.Query(`SELECT id FROM attachments WHERE uploader=?`, uploader)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && id != "" {
			ids = append(ids, id)
		}
	}
	rows.Close()
	s.db.Exec(`DELETE FROM attachments WHERE uploader=?`, uploader)
	for _, id := range ids {
		if err := os.Remove(s.FilePath(id)); err != nil && !os.IsNotExist(err) {
			log.Printf("attach: 删号清理附件文件失败 %s: %v", id, err)
		}
	}
}

// Save 解析 multipart 上传（字段名 file，取第一个），单次流式读完成
// 大小限长、内容嗅探与落盘，成功后入库。请求体应先经 http.MaxBytesReader。
func (s *Store) Save(uploader string, r *http.Request) (Meta, error) {
	if err := r.ParseMultipartForm(32 << 10); err != nil {
		return Meta{}, fmt.Errorf("非 multipart 表单或超限: %w", err)
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return Meta{}, errors.New("缺少 file 字段")
	}
	defer f.Close()

	// 文件名清洗：去路径成分、限长（展示与下载名用，不参与存储路径）
	name := filepath.Base(hdr.Filename)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" || name == `\` {
		name = "file"
	}
	if len(name) > 200 {
		name = name[len(name)-200:]
	}

	id, err := newID()
	if err != nil {
		return Meta{}, err
	}
	tmp := s.FilePath(id) + ".tmp"
	dst, err := os.Create(tmp)
	if err != nil {
		return Meta{}, err
	}

	// 单次流式读：前 512B 嗅探 mime，边写边计大小，超限即中止清理
	var head []byte
	var size int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if len(head) < 512 {
				take := 512 - len(head)
				if take > n {
					take = n
				}
				head = append(head, buf[:take]...)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				dst.Close()
				os.Remove(tmp)
				return Meta{}, werr
			}
			size += int64(n)
			if size > MaxSize {
				dst.Close()
				os.Remove(tmp)
				return Meta{}, fmt.Errorf("文件超过 %dMB 限制", MaxSize>>20)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			dst.Close()
			os.Remove(tmp)
			return Meta{}, rerr
		}
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return Meta{}, err
	}
	if err := os.Rename(tmp, s.FilePath(id)); err != nil {
		os.Remove(tmp)
		return Meta{}, err
	}

	// mime 以内容嗅探为准（上传声明可伪造），嗅探不出回落声明值
	mime := http.DetectContentType(head)
	if mime == "application/octet-stream" {
		if ct := hdr.Header.Get("Content-Type"); ct != "" {
			mime = strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}

	m := Meta{ID: id, Name: name, MIME: mime, Size: size, Uploader: uploader}
	if err := s.db.InsertAttachment(m.ID, m.Name, m.MIME, m.Size, m.Uploader); err != nil {
		os.Remove(s.FilePath(id))
		return Meta{}, err
	}
	if got, ok := s.db.AttachmentByID(id); ok { // 回填库默认生成的时间戳
		m.CreatedAt = got.CreatedAt
	}
	return m, nil
}

// Get 按id取元数据。
func (s *Store) Get(id string) (Meta, bool) {
	return s.db.AttachmentByID(id)
}

// Prune 删除截止时间前的附件（行 + 文件），返回删除数。
func (s *Store) Prune(cutoff time.Time) int {
	ids, err := s.db.PruneAttachmentsBefore(cutoff)
	if err != nil {
		log.Printf("attach: 保留期清理查询失败: %v", err)
		return 0
	}
	for _, id := range ids {
		os.Remove(s.FilePath(id))
	}
	return len(ids)
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
