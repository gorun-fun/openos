// gitlab.go — GitLab issue 周期同步：接管（platform=gitlab）的仓库自动抓取
// open issue 转为待修复问题。项目路径取 clone 地址（grp/sub/proj.git →
// grp/sub/proj，不另设字段，接管即同步、存量仓库无感生效）；凭据 = 仓挂接的
// 连接 → 活动 GitLab 连接（复用 connBearer：PAT 直用 / 账密 password grant
// 自动兑；env 凭证首启已种子为连接，无需引擎侧 env 兜底）；labels 配置非空时
// 只收命中的；按 gl:<项目路径>/issue/<iid> 去重。issues 接口不含 MR，无需滤 PR。
package steward

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const glIssuePagesMax = 5 // 翻页上限（每页 100，防异常大仓拖垮一轮同步）

type glIssue struct {
	IID    int      `json:"iid"`
	Title  string   `json:"title"`
	Desc   string   `json:"description"`
	WebURL string   `json:"web_url"`
	Labels []string `json:"labels"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}

// syncGitLabIssues 同步单个 GitLab 仓库的 open issue，返回本轮新增问题数。
func (e *Engine) syncGitLabIssues(ctx context.Context, repoID string) (int, error) {
	var snap Repo
	e.st.View(func(s *State) {
		if r := s.repo(repoID); r != nil {
			snap = *r
		}
	})
	if snap.ID == "" {
		return 0, ErrRepoNotFound
	}
	if snap.Platform != PlatformGitLab {
		return 0, fmt.Errorf("非 GitLab 仓库")
	}
	project := glProjectPath(snap.CloneURL)
	if project == "" {
		return 0, fmt.Errorf("无法从 clone 地址解析 GitLab 项目路径")
	}
	conn, err := e.glSyncConn(snap)
	if err != nil {
		return 0, err
	}
	issues, err := e.fetchGitLabIssues(ctx, conn, project)
	if err != nil {
		return 0, err
	}
	added := 0
	err = e.st.Mutate(func(s *State) error {
		now := time.Now()
		if r := s.repo(repoID); r != nil {
			r.LastSync = &now
		}
		for _, is := range issues {
			if len(snap.GHLabels) > 0 && !labelHitAny(is.Labels, snap.GHLabels) {
				continue
			}
			extKey := fmt.Sprintf("gl:%s/issue/%d", project, is.IID)
			if problemExists(s, repoID, extKey) {
				continue
			}
			p := &Problem{
				ID:        newProblemID(s),
				RepoID:    repoID,
				Source:    SrcGitLab,
				ExtKey:    extKey,
				ExtURL:    is.WebURL,
				Title:     is.Title,
				Body:      is.Desc,
				Reporter:  is.Author.Username,
				Labels:    nonEmptyLabels(is.Labels),
				State:     initialState(snap.Mode),
				Release:   snap.AutoRelease,
				CreatedAt: now,
			}
			s.Problems = append(s.Problems, p)
			added++
		}
		return nil
	})
	if err != nil {
		return added, err
	}
	if added > 0 {
		e.Notify() // 自动模式的新问题可能直接入队
	}
	return added, nil
}

// glSyncConn issue 同步的 GitLab 连接：仓挂接的连接优先，否则活动 GitLab 连接
// ——但须与仓库 clone 地址同实例（此前不校验：仓库在别的 host 时拿活动连接的
// BaseURL 查错实例，每轮同步 404）。
func (e *Engine) glSyncConn(snap Repo) (*Connection, error) {
	if snap.ConnectionID != "" {
		if c, ok := e.connByID(snap.ConnectionID); ok && c.Platform == PlatformGitLab {
			return &c, nil
		}
	}
	var active *Connection
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			if c.Platform == PlatformGitLab && c.Active {
				cp := *c
				active = &cp
				break
			}
		}
	})
	if active == nil {
		return nil, fmt.Errorf("无可用 GitLab 凭据——请先在「仓库」页连接 GitLab 账号")
	}
	if !sameHost(active.BaseURL, snap.CloneURL) {
		return nil, fmt.Errorf("仓库与活动 GitLab 连接不在同一实例（clone 地址 host ≠ 连接实例），请为仓库挂接同实例连接")
	}
	return active, nil
}

// sameHost 两 URL 的 host（含端口）是否一致（大小写不敏感）。
func sameHost(a, b string) bool {
	ua, err1 := url.Parse(strings.TrimSpace(a))
	ub, err2 := url.Parse(strings.TrimSpace(b))
	return err1 == nil && err2 == nil && ua.Host != "" && strings.EqualFold(ua.Host, ub.Host)
}

func (e *Engine) fetchGitLabIssues(ctx context.Context, c *Connection, project string) ([]glIssue, error) {
	var out []glIssue
	for page := 1; page <= glIssuePagesMax; page++ {
		path := fmt.Sprintf("/projects/%s/issues?state=opened&order_by=created_at&sort=desc&per_page=100&page=%d",
			url.QueryEscape(project), page)
		var batch []glIssue
		code, hdr, raw, err := e.connRequest(ctx, c, http.MethodGet, path, nil, &batch)
		if err != nil {
			return nil, err
		}
		switch {
		case code == http.StatusNotFound:
			return nil, fmt.Errorf("GitLab 项目 %s 不存在或无权限", project)
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return nil, fmt.Errorf("GitLab API 拒绝（status %d，令牌无效或无权限）", code)
		case code >= 300:
			return nil, fmt.Errorf("GitLab API → %d: %s", code, ErrMessage(raw))
		}
		out = append(out, batch...)
		next, _ := strconv.Atoi(hdr.Get("X-Next-Page"))
		if next == 0 || len(batch) == 0 {
			break
		}
	}
	return out, nil
}

// glProjectPath 从 clone 地址推导 GitLab 项目路径；推导不出（无 host 或不足
// 命名空间/项目两层）返回空。
func glProjectPath(cloneURL string) string {
	u, err := url.Parse(strings.TrimSpace(cloneURL))
	if err != nil || u.Host == "" {
		return ""
	}
	p := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	if p == "" || !strings.Contains(p, "/") {
		return ""
	}
	return p
}

func nonEmptyLabels(in []string) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
