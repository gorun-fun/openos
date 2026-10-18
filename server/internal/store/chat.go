// chat.go — goder 对话历史（chat_messages）与聊天室消息（chat_room_messages）。
package store

import "time"

// ChatMsg goder 对话历史。
type ChatMsg struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"createdAt"`
}

// AddChatMsg 追加一条对话消息。
func (db *DB) AddChatMsg(username, role, content string) {
	db.Exec(`INSERT INTO chat_messages(username,role,content) VALUES(?,?,?)`, username, role, content)
}

// ListChatMsgs 用户最近 n 条消息（时间正序）。
func (db *DB) ListChatMsgs(username string, n int) ([]ChatMsg, error) {
	rows, err := db.Query(`SELECT role,content,created_at FROM (
		SELECT id,role,content,created_at FROM chat_messages WHERE username=? ORDER BY id DESC LIMIT ?)
		ORDER BY id ASC`, username, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatMsg{}
	for rows.Next() {
		var m ChatMsg
		if err := rows.Scan(&m.Role, &m.Content, &m.CreatedAt); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ClearChatMsgs 清空用户对话历史。
func (db *DB) ClearChatMsgs(username string) {
	db.Exec(`DELETE FROM chat_messages WHERE username=?`, username)
}

// PruneChatBefore 删除 cutoff 之前的 AI 对话历史（含终端伪用户 __term__ 的孤儿数据），返回删除行数。
func (db *DB) PruneChatBefore(cutoff time.Time) (int64, error) {
	return db.pruneBefore("chat_messages", cutoff)
}

// ChatRoomMessage 聊天室历史消息行（正序返回给前端）。
type ChatRoomMessage struct {
	ID     int64          `json:"id"`
	From   string         `json:"from"`
	Text   string         `json:"text"`
	Attach *AttachmentRef `json:"attach,omitempty"`
	TS     int64          `json:"ts"`
}

// AttachmentRef 消息内嵌的附件快照（附件本体可能先于消息被保留期清理，
// 按快照渲染、点击时 404 由前端兜底提示）。
type AttachmentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
}

// InsertChatRoomMessage 聊天消息落库（from/ts 由服务端总线注入，调用方已校验）。
func (db *DB) InsertChatRoomMessage(m ChatRoomMessage) error {
	var aID, aName, aMIME string
	var aSize int64
	if m.Attach != nil {
		aID, aName, aMIME, aSize = m.Attach.ID, m.Attach.Name, m.Attach.MIME, m.Attach.Size
	}
	_, err := db.Exec(`INSERT INTO chat_room_messages(sender,text,attach_id,attach_name,attach_mime,attach_size,ts)
		VALUES(?,?,?,?,?,?,?)`, m.From, m.Text, aID, aName, aMIME, aSize, m.TS)
	return err
}

// ChatRoomHistory 分页查询历史：before=0 取最新一页，否则取 id < before 的更早一页；
// 倒序取 limit 行后反转为正序返回。
func (db *DB) ChatRoomHistory(before int64, limit int) ([]ChatRoomMessage, error) {
	rows, err := db.Query(`SELECT id,sender,text,attach_id,attach_name,attach_mime,attach_size,ts
		FROM chat_room_messages WHERE (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChatRoomMessage{}
	for rows.Next() {
		var m ChatRoomMessage
		var aID, aName, aMIME string
		var aSize int64
		if err := rows.Scan(&m.ID, &m.From, &m.Text, &aID, &aName, &aMIME, &aSize, &m.TS); err != nil {
			return nil, err
		}
		if aID != "" {
			m.Attach = &AttachmentRef{ID: aID, Name: aName, MIME: aMIME, Size: aSize}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 反转为正序（旧 → 新）
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// PruneChatRoomBefore 删除截止时间前的聊天室消息。
func (db *DB) PruneChatRoomBefore(cutoff time.Time) (int64, error) {
	return db.pruneBefore("chat_room_messages", cutoff)
}
