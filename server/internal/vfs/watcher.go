package vfs

import (
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// 统一 home 磁盘变更监控（轮询指纹）：fs:changed 只在 REST 写操作时广播，
// goder 子进程与真实终端直接写磁盘、绕过 REST，文件应用停在目录里就收不到
// 任何通知。这里每轮全量扫描 homes/<user> 目录树建立 path→指纹（mtime/size/
// 类型）基线，与上一轮对比，变更按用户合并成一条 fs:changed（path 取变更项的
// 最深公共父目录），FilesApp/Dock 现有订阅即刻刷新。
//
// 事件与 REST 写操作自发的事件可能重复到达（前端刷新幂等），无需去重。
// 扫描失败（权限等）保留旧基线整轮跳过——部分扫描会产生假删除。
// 大目录自适应降频：下一轮等待时间不低于扫描耗时的两倍，防止巨型 goder
// 项目（node_modules 级）把轮询吃成满载 CPU。

const fsWatchInterval = 3 * time.Second

// fileFinger 单条目指纹：目录只记 mtime（size 平台语义不定），文件记 mtime+size。
type fileFinger struct {
	mod  int64 // ModTime().UnixNano()
	size int64
	dir  bool
}

// homeWatcher 统一 home 轮询监控器。publish 注入以便测试替换。
type homeWatcher struct {
	homesDir string
	publish  func(user, topic string, payload any)
	interval time.Duration
	baseline map[string]map[string]fileFinger // 用户 → vpath → 指纹
}

// WatchHomes 启动统一 home 磁盘变更监控（阻塞运行，调用方应以 goroutine 拉起）。
// OPENOS_VFS=off 或 OPENOS_FS_WATCH=off 时不启动。
func (f *FS) WatchHomes() {
	if f.disabled || os.Getenv("OPENOS_FS_WATCH") == "off" {
		return
	}
	w := &homeWatcher{
		homesDir: f.homesDir,
		publish:  f.hub.PublishTo,
		interval: fsWatchInterval,
		baseline: map[string]map[string]fileFinger{},
	}
	w.run()
}

func (w *homeWatcher) run() {
	for {
		start := time.Now()
		w.scanOnce()
		// 自适应降频：大目录扫描耗时两倍以上时按扫描耗时等待
		wait := w.interval
		if d := 2 * time.Since(start); d > wait {
			wait = d
		}
		time.Sleep(wait)
	}
}

// scanOnce 扫一轮全部用户目录：新用户只建基线，目录已消失的用户丢弃基线。
func (w *homeWatcher) scanOnce() {
	dirs, err := os.ReadDir(w.homesDir)
	if err != nil {
		return // 数据目录尚未就绪，静默等下一轮
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		seen[d.Name()] = true
		w.scanUser(d.Name())
	}
	for u := range w.baseline {
		if !seen[u] {
			delete(w.baseline, u)
		}
	}
}

// scanUser 扫描单个用户目录树并与基线对比，有变更时合并广播一条事件。
func (w *homeWatcher) scanUser(user string) {
	root := filepath.Join(w.homesDir, user)
	cur := map[string]fileFinger{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		cur[path.Join(homeOf(user), filepath.ToSlash(rel))] = fileFinger{
			mod:  info.ModTime().UnixNano(),
			size: info.Size(),
			dir:  d.IsDir(),
		}
		return nil
	})
	if err != nil {
		log.Printf("[fs-watch] 扫描 %s 失败，本轮跳过: %v", root, err)
		return
	}
	old, existed := w.baseline[user]
	w.baseline[user] = cur
	if !existed {
		return // 首轮只建基线：启动时磁盘上已有的不是「变更」
	}

	var changed []string
	for p, nf := range cur {
		of, ok := old[p]
		switch {
		case !ok:
			changed = append(changed, p) // 新增（文件或空目录）
		case nf.dir != of.dir:
			changed = append(changed, p) // 同名条目文件↔目录翻转
		case !nf.dir && nf != of:
			changed = append(changed, p) // 文件内容/属性变化
		}
		// 目录仅 mtime 变化是子项增删的副作用，子项已单独进 changed，跳过
	}
	for p := range old {
		if _, ok := cur[p]; !ok {
			changed = append(changed, p) // 删除（含其子项逐条体现）
		}
	}
	if len(changed) == 0 {
		return
	}
	w.publish(user, "fs:changed", map[string]any{
		"path": commonParentDir(changed),
		"kind": "watch",
	})
}

// commonParentDir 取一批虚拟路径的最深公共父目录：
// ["/home/u/a/f1", "/home/u/a/f2"] → "/home/u/a"，跨顶层则收敛到 home 根。
func commonParentDir(paths []string) string {
	var segs []string
	for _, p := range paths {
		s := strings.Split(path.Dir(p), "/")
		if segs == nil {
			segs = s
			continue
		}
		n := len(segs)
		if len(s) < n {
			n = len(s)
		}
		i := 0
		for i < n && segs[i] == s[i] {
			i++
		}
		segs = segs[:i]
	}
	return path.Clean(strings.Join(segs, "/"))
}
