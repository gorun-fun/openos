// github.go — GitHub issue 周期同步：开源到 GitHub 的仓库自动抓取 open issue
// 转为待修复问题。滤掉 PR；labels 配置非空时只收命中的；按 gh:<owner>/<repo>#<n>
// 去重。匿名限额 60 次/小时，多仓库轮询建议配 Token（环境变量或设置页）。
package steward

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const githubAPI = "https://api.github.com"

type ghIssue struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// syncRepoIssues 同步单个仓库的 open issue，返回本轮新增问题数。
func (e *Engine) syncRepoIssues(ctx context.Context, repoID string) (int, error) {
	var snap Repo
	e.st.View(func(s *State) {
		if r := s.repo(repoID); r != nil {
			snap = *r
		}
	})
	if snap.ID == "" {
		return 0, ErrRepoNotFound
	}
	if snap.GHOwner == "" || snap.GHRepo == "" {
		return 0, fmt.Errorf("未配置 GitHub 同步（owner/repo）")
	}
	issues, err := e.fetchGitHubIssues(ctx, snap.GHOwner, snap.GHRepo)
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
			if is.PullRequest != nil { // issues API 会把 PR 一并返回
				continue
			}
			if len(snap.GHLabels) > 0 && !labelHit(is, snap.GHLabels) {
				continue
			}
			extKey := fmt.Sprintf("gh:%s/%s#%d", snap.GHOwner, snap.GHRepo, is.Number)
			if problemExists(s, repoID, extKey) {
				continue
			}
			p := &Problem{
				ID:        newProblemID(s),
				RepoID:    repoID,
				Source:    SrcGitHub,
				ExtKey:    extKey,
				ExtURL:    is.HTMLURL,
				Title:     is.Title,
				Body:      is.Body,
				Reporter:  is.User.Login,
				Labels:    issueLabels(is),
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

// ghIssuePagesMax 翻页上限（每页 100，与 GitLab 同步同惯例，防异常大仓拖垮一轮）。
const ghIssuePagesMax = 5

func (e *Engine) fetchGitHubIssues(ctx context.Context, owner, repo string) ([]ghIssue, error) {
	api := e.ghAPI
	if api == "" {
		api = githubAPI // 测试经引擎字段注入假地址
	}
	var out []ghIssue
	for page := 1; page <= ghIssuePagesMax; page++ {
		u := fmt.Sprintf("%s/repos/%s/%s/issues?state=open&per_page=100&page=%d", api, owner, repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := e.githubToken(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		link := resp.Header.Get("Link") // GitHub 以 Link rel="next" 标识续页
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("GitHub 仓库 %s/%s 不存在或无权限", owner, repo)
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("GitHub API 拒绝（status %d，匿名限额或 Token 无效）", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub API → %d", resp.StatusCode)
		}
		var batch []ghIssue
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if !strings.Contains(link, `rel="next"`) || len(batch) == 0 {
			break
		}
	}
	return out, nil
}

func labelHit(is ghIssue, want []string) bool {
	names := make([]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		names = append(names, l.Name)
	}
	return labelHitAny(names, want)
}

// labelHitAny issue label 过滤（GitHub/GitLab 同步共用）：labels 非空且命中
// 任一（大小写不敏感）才收。
func labelHitAny(labels, want []string) bool {
	for _, l := range labels {
		for _, w := range want {
			if strings.EqualFold(strings.TrimSpace(l), strings.TrimSpace(w)) {
				return true
			}
		}
	}
	return false
}

func issueLabels(is ghIssue) []string {
	out := make([]string, 0, len(is.Labels))
	for _, l := range is.Labels {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

func problemExists(s *State, repoID, extKey string) bool {
	for _, p := range s.Problems {
		if p.RepoID == repoID && p.ExtKey == extKey {
			return true
		}
	}
	return false
}

// initialState 仓库模式决定新问题落点：manual → new（待确认），auto → confirmed
// （直接入队修复）。
func initialState(mode string) string {
	if mode == ModeAuto {
		return PConfirmed
	}
	return PNew
}
