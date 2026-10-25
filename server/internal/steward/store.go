// store.go — JSON 状态文件（state.json）持久化：RWMutex + 临时文件原子替换。
// 领域操作一律经 View/Mutate 进入（Mutate 内改指针、返回即落盘），绝不长期
// 持有 State 内指针——append 扩容会让旧指针指向过期底层数组。
package steward

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"openos/server/internal/atomicfile"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store 状态存储。
type Store struct {
	mu   sync.RWMutex
	path string
	st   State
}

// Open 加载（或初始化）<dir>/state.json；上报 Key 缺失时自动生成。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	if raw, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(raw, &s.st); err != nil {
			return nil, fmt.Errorf("解析 %s: %w", s.path, err)
		}
	}
	if s.st.Settings == nil {
		s.st.Settings = &Settings{}
	}
	if s.st.Settings.ReportKey == "" {
		s.st.Settings.ReportKey = randHex(24)
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// migrateLegacyDataDir 旧数据目录 <parent>/fixer 改名迁移（v0.0.32 前 App 名
// fixer「自动修复」，数据目录随旧名）：目标不存在（或为空目录）且旧目录有
// state.json 时整体 rename；失败仅告警（继续以新目录启动，不阻断宿主）。
func migrateLegacyDataDir(dir string, logf func(format string, args ...any)) {
	if dir == "" {
		return
	}
	legacy := filepath.Join(filepath.Dir(dir), "fixer")
	if legacy == dir {
		return
	}
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return
		}
		// 目标已存在：仅当完全为空时让位给旧目录（如上次启动建了一半）
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		_ = os.Remove(dir)
	}
	if _, err := os.Stat(filepath.Join(legacy, "state.json")); err != nil {
		return // 旧目录无状态文件：无迁移必要
	}
	if err := os.Rename(legacy, dir); err != nil {
		if logf != nil {
			logf("[steward] 旧数据目录迁移失败（将以新目录启动）: %v", err)
		}
		return
	}
	if logf != nil {
		logf("[steward] 已迁移旧数据目录 %s → %s", legacy, dir)
	}
}

// View 只读访问（共享锁内执行 fn，勿留存指针）。
func (s *Store) View(fn func(st *State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.st)
}

// Mutate 读写访问：fn 修改状态，正常返回后原子落盘；落盘失败返回错误
// （内存态已更新，下次成功保存会追平，调用方按业务取舍）。
func (s *Store) Mutate(fn func(st *State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.st); err != nil {
		return err
	}
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	return atomicfile.WriteJSON(s.path, &s.st, 0o600)
}

// randHex 密钥/ID 随机源。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败极罕见（系统熵池），退化用时间兜底，仅影响可猜性
		now := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(now >> (uint(i%8) * 8))
		}
	}
	return hex.EncodeToString(b)
}
