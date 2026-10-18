// feedback.go — 用户反馈（反馈应用：全员可提交，管理员处理）。
package store

// Feedback 反馈行（category/status 白名单校验在 main 路由层）。
type Feedback struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Category  string `json:"category"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	Reply     string `json:"reply"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// InsertFeedback 提交反馈（状态恒为 pending，入库时间由默认值生成）。
func (db *DB) InsertFeedback(username, category, content string) (int64, error) {
	res, err := db.Exec(`INSERT INTO feedback(username,category,content) VALUES(?,?,?)`,
		username, category, content)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFeedback 反馈列表（倒序）：all=false 只看本人，all=true 全部（admin 视角）。
func (db *DB) ListFeedback(username string, all bool, limit int) ([]Feedback, error) {
	q := `SELECT id,username,category,content,status,reply,created_at,updated_at FROM feedback`
	args := []any{}
	if !all {
		q += ` WHERE username=?`
		args = append(args, username)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Feedback{}
	for rows.Next() {
		var f Feedback
		if err := rows.Scan(&f.ID, &f.Username, &f.Category, &f.Content, &f.Status, &f.Reply, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FeedbackByID 按 id 取反馈（管理员处理时回读提交者，用于定向通知）。
func (db *DB) FeedbackByID(id int64) (Feedback, bool) {
	var f Feedback
	err := db.QueryRow(`SELECT id,username,category,content,status,reply,created_at,updated_at
		FROM feedback WHERE id=?`, id).
		Scan(&f.ID, &f.Username, &f.Category, &f.Content, &f.Status, &f.Reply, &f.CreatedAt, &f.UpdatedAt)
	return f, err == nil
}

// UpdateFeedback 管理员处理反馈：更新状态与回复（updated_at 一并刷新）。
func (db *DB) UpdateFeedback(id int64, status, reply string) (bool, error) {
	res, err := db.Exec(`UPDATE feedback SET status=?, reply=?, updated_at=datetime('now') WHERE id=?`,
		status, reply, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AdminUsernames 全部管理员用户名（新反馈通知接收方；不含禁用账号）。
func (db *DB) AdminUsernames() []string {
	rows, err := db.Query(`SELECT username FROM users WHERE role='admin' AND disabled=0`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil {
			out = append(out, u)
		}
	}
	return out
}
