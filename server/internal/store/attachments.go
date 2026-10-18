// attachments.go — 系统附件元数据（attach 包：文件本体在磁盘，行在这里）。
package store

import "time"

// InsertAttachment 附件元数据入库（文件已落盘；入库失败由调用方清理文件）。
func (db *DB) InsertAttachment(id, name, mime string, size int64, uploader string) error {
	_, err := db.Exec(`INSERT INTO attachments(id,name,mime,size,uploader) VALUES(?,?,?,?,?)`,
		id, name, mime, size, uploader)
	return err
}

// Attachment 附件元数据行（attach 包以 Meta 别名复用）。
type Attachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Size      int64  `json:"size"`
	Uploader  string `json:"uploader"`
	CreatedAt string `json:"createdAt"`
}

// AttachmentByID 按 id 查附件元数据。
func (db *DB) AttachmentByID(id string) (Attachment, bool) {
	var m Attachment
	var created string
	err := db.QueryRow(`SELECT id,name,mime,size,uploader,created_at FROM attachments WHERE id=?`, id).
		Scan(&m.ID, &m.Name, &m.MIME, &m.Size, &m.Uploader, &created)
	if err != nil {
		return m, false
	}
	m.CreatedAt = created
	return m, true
}

// PruneAttachmentsBefore 删除截止时间前的附件行，返回被删 id（文件删除由 attach 层完成）。
func (db *DB) PruneAttachmentsBefore(cutoff time.Time) ([]string, error) {
	rows, err := db.Query(`SELECT id FROM attachments WHERE created_at < ?`, cutoff.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := db.Exec(`DELETE FROM attachments WHERE id=?`, id); err != nil {
			return ids, err // 已收集的照常返回，文件层尽力删
		}
	}
	return ids, nil
}
