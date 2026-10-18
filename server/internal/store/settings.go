// settings.go — 每用户界面设置（user_settings）与系统级键值（system_kv）。
// 系统级键值与用户设置分离：清理用户设置不应波及系统配置。
package store

import "database/sql"

// GetUserSetting 读取用户级设置，不存在返回空串。
func (db *DB) GetUserSetting(username, key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM user_settings WHERE username=? AND key=?`, username, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetUserSetting 写入用户级设置。
func (db *DB) SetUserSetting(username, key, value string) error {
	_, err := db.Exec(`INSERT INTO user_settings(username,key,value) VALUES(?,?,?)
		ON CONFLICT(username,key) DO UPDATE SET value=excluded.value`, username, key, value)
	return err
}

// GetSystemKV 读取系统级键值，不存在返回空串。
func (db *DB) GetSystemKV(key string) (string, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM system_kv WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSystemKV 写入系统级键值。
func (db *DB) SetSystemKV(key, value string) error {
	_, err := db.Exec(`INSERT INTO system_kv(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
