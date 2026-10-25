// repo.go — 仓库接管 CRUD 与 issue 同步入口（自 steward.go 按域拆分）。
package steward

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---- 仓库 ----

// ListRepos 全量仓库（GitToken 已清空，调用方另给掩码标记）。
func (e *Engine) ListRepos() []Repo {
	var out []Repo
	e.st.View(func(s *State) {
		for _, r := range s.Repos {
			c := *r
			c.GitToken = ""
			out = append(out, c)
		}
	})
	return out
}

func (e *Engine) GetRepo(id string) (Repo, bool) {
	var out Repo
	var ok bool
	e.st.View(func(s *State) {
		if r := s.repo(id); r != nil {
			c := *r
			c.GitToken = ""
			out, ok = c, true
		}
	})
	return out, ok
}

// UpsertRepo 新增/更新仓库。GitToken 留空 = 沿用旧值；CloneURL 内嵌凭据会
// 自动拆到 GitUser/GitToken 并清洗 URL。
func (e *Engine) UpsertRepo(in *Repo) (Repo, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.CloneURL = strings.TrimSpace(in.CloneURL)
	if in.Name == "" || in.CloneURL == "" {
		return Repo{}, errors.New("名称与 clone 地址必填")
	}
	if in.Mode != ModeAuto {
		in.Mode = ModeManual
	}
	if u, err := url.Parse(in.CloneURL); err == nil && u.User != nil {
		// 内嵌凭据拆出并清洗 URL（https://user:token@host/… 或 https://token@host/…）
		if pw, hasPW := u.User.Password(); hasPW && pw != "" {
			in.GitToken = pw
			if un := u.User.Username(); un != "" {
				in.GitUser = un
			}
		} else if un := u.User.Username(); un != "" && in.GitToken == "" {
			in.GitToken = un
		}
		u.User = nil
		in.CloneURL = u.String()
	}
	in.GHOwner = strings.TrimSpace(in.GHOwner)
	in.GHRepo = strings.TrimSpace(in.GHRepo)
	if in.Platform != PlatformGitLab && in.Platform != PlatformGitHub {
		in.Platform = e.detectPlatform(in.CloneURL) // 空/非法 → 按 clone host 识别（手动注册与存量仓库补标）
	}
	now := time.Now()
	created := in.ID == ""
	var out Repo
	err := e.st.Mutate(func(s *State) error {
		if in.ID != "" {
			old := s.repo(in.ID)
			if old == nil {
				return ErrRepoNotFound
			}
			if in.GitToken == "" {
				in.GitToken = old.GitToken // 空 = 不改
			}
			if in.ConnectionID == "" {
				in.ConnectionID = old.ConnectionID // 挂接的连接不由普通表单清空
				if in.Platform == "" {
					in.Platform = old.Platform // 平台随旧值；上游已按 clone host 补识别
				}
			}
			// Workspace 由引擎在修复时落值（改名不改目录归属），输入不覆盖
			in.CreatedAt, in.DefBranch, in.Workspace, in.LastSync = old.CreatedAt, old.DefBranch, old.Workspace, old.LastSync
			*old = *in
			old.UpdatedAt = now
			out = *old
			return nil
		}
		if repoNameTaken(s, in.Name) {
			return errors.New("同名仓库已存在")
		}
		in.ID = newRepoID(s)
		in.CreatedAt, in.UpdatedAt = now, now
		cp := *in
		s.Repos = append(s.Repos, &cp)
		out = cp
		return nil
	})
	if err != nil {
		return Repo{}, err
	}
	e.clearConnRepoCache() // taken_over 投影变了：账号浏览缓存失效
	if created {
		e.kickFirstSync(out.ID) // 接管/注册即拉一轮 issue，不等 5 分钟周期
	}
	out.GitToken = ""
	return out, nil
}

// detectPlatform 按 clone 地址 host 识别平台：github.com → GitHub；命中任一
// GitLab 连接的 BaseURL host → GitLab；识别不出返回空（不影响修复，仅不收 issue）。
func (e *Engine) detectPlatform(cloneURL string) string {
	u, err := url.Parse(strings.TrimSpace(cloneURL))
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Host == "github.com" {
		return PlatformGitHub
	}
	match := false
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			if c.Platform != PlatformGitLab {
				continue
			}
			if b, err := url.Parse(strings.TrimSpace(c.BaseURL)); err == nil && strings.EqualFold(b.Host, u.Host) {
				match = true
				break
			}
		}
	})
	if match {
		return PlatformGitLab
	}
	return ""
}

// DeleteRepo 删除仓库配置与工作区（有进行中的问题/修复时拒绝）。工作区在
// 操作者 home 下时一并清理（按目录名扫各用户「代码管家」与旧名「自动修复」
// 目录 + 记录的最近路径）。
func (e *Engine) DeleteRepo(id string) error {
	var dirName, lastWS string
	err := e.st.Mutate(func(s *State) error {
		r := s.repo(id)
		if r == nil {
			return ErrRepoNotFound
		}
		for _, p := range s.Problems {
			if p.RepoID == id && (p.State == PRunning || p.State == PConfirmed) {
				return errors.New("该仓库仍有进行中/排队中的问题，先取消再删除")
			}
		}
		dirName, lastWS = sanitizeDirName(r.Name, r.ID), r.Workspace
		repos := make([]*Repo, 0, len(s.Repos))
		for _, x := range s.Repos {
			if x.ID != id {
				repos = append(repos, x)
			}
		}
		s.Repos = repos
		return nil
	})
	if err != nil {
		return err
	}
	e.clearConnRepoCache()            // taken_over 投影变了：账号浏览缓存失效
	_ = os.RemoveAll(e.workspace(id)) // 配置已删，清工作区（失败不影响结果）
	if e.opts.HomesDir != "" {
		for _, group := range []string{"代码管家", "自动修复"} { // 旧分组目录一并扫清
			if hits, _ := filepath.Glob(filepath.Join(e.opts.HomesDir, "*", group, dirName)); len(hits) > 0 {
				for _, p := range hits {
					_ = os.RemoveAll(p)
				}
			}
		}
	}
	if lastWS != "" {
		_ = os.RemoveAll(lastWS) // 改过名/非常规路径兜底
	}
	return nil
}

// SyncIssuesNow 手动触发一次 issue 同步（按仓库平台路由）。
func (e *Engine) SyncIssuesNow(ctx context.Context, repoID string) (int, error) {
	return e.syncRepo(ctx, repoID)
}

// SyncAllIssuesNow 手动触发全部可同步仓库的 issue 同步（问题页「同步 issue」
// 按钮）：返回总新增数与失败清单（单仓失败不阻断其余；空清单非 nil——JSON
// null 会被前端整包回落误迭代）。
func (e *Engine) SyncAllIssuesNow(ctx context.Context) (int, []string) {
	total := 0
	errs := []string{}
	for _, id := range e.syncEligibleIDs() {
		if ctx.Err() != nil {
			break
		}
		n, err := e.syncRepo(ctx, id)
		if err != nil {
			var name string
			e.st.View(func(s *State) {
				if r := s.repo(id); r != nil {
					name = r.Name
				}
			})
			errs = append(errs, fmt.Sprintf("%s: %v", firstNonEmpty(name, id), err))
			continue
		}
		total += n
	}
	return total, errs
}

// RepoNames 仓库 ID → 名称映射（含已删仓库问题的回显兜底由调用方处理）。
func (e *Engine) RepoNames() map[string]string {
	out := map[string]string{}
	e.st.View(func(s *State) {
		for _, r := range s.Repos {
			out[r.ID] = r.Name
		}
	})
	return out
}

// HasGitToken 仓库是否配置了推送凭据（API 掩码标记用）。
func (e *Engine) HasGitToken(id string) bool {
	var ok bool
	e.st.View(func(s *State) {
		if r := s.repo(id); r != nil && r.GitToken != "" {
			ok = true
		}
	})
	return ok
}
