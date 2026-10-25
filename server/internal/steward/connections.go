// connections.go — 平台账号连接（登录方式之二，v0.0.32 起）：直接登录
// GitLab / GitHub 账号，浏览与管理账号下全部仓库；模式一仍是项目级单仓注册
// （Repo，见 types.go）。凭据只存服务端 state.json（API 输出一律经 ConnView
// 掩码，绝不回传浏览器）：GitLab 支持账密（OAuth password grant，access_token
// 内存缓存临期重取）与 PAT；GitHub 仅 PAT（平台已停用账密授权）。
//
// GitLab 连接含「活动」概念（同网关多实例的活动实例惯例）：流水线工作面
// （/api/steward/pipe/*）与转修桥以活动 GitLab 连接为数据源（请求期实时读，
// 切换即生效），无连接时回落环境变量 OPENOS_GITLAB_*（GRF_OS_GITLAB_* 宿主兼容兜底）——env 首启自动种子为
// 连接（api 层 seedEnvConnections，存量部署无感升级）。仓库的推送凭据解析链：
// repo.connection_id → 连接当前令牌（gitlab oauth2: / github x-access-token:）
// → 仓级 GitUser/GitToken（见 repoAuth）。
package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 平台类型
const (
	PlatformGitLab = "gitlab"
	PlatformGitHub = "github"
)

const (
	connReposTTL  = 30 * time.Second
	connReposPage = 50
	connReposMax  = 200 // 单次浏览硬上限（4 页）
)

// Connection 平台账号连接（凭据仅服务端可见）。
type Connection struct {
	ID        string    `json:"id"`
	Platform  string    `json:"platform"` // gitlab | github
	BaseURL   string    `json:"base_url"` // GitLab 实例根（github 固定 https://github.com）
	Login     string    `json:"login"`    // 验证时回填的账号名
	AvatarURL string    `json:"avatar_url,omitempty"`
	Name      string    `json:"name,omitempty"` // 展示名（空 = login@host）
	PAT       string    `json:"pat,omitempty"`  // Personal Access Token（两平台通用）
	User      string    `json:"user,omitempty"` // GitLab 账密模式用户名
	Password  string    `json:"password,omitempty"`
	Active    bool      `json:"active,omitempty"` // GitLab：流水线数据源（平台内至多一个）
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ConnInput 新建连接入参（PAT 与 GitLab 账密二选一）。
type ConnInput struct {
	Platform string
	BaseURL  string
	PAT      string
	User     string
	Password string
}

// ConnView 连接的对外投影（凭据全掩码）。
type ConnView struct {
	ID          string    `json:"id"`
	Platform    string    `json:"platform"`
	BaseURL     string    `json:"base_url"`
	Login       string    `json:"login"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Name        string    `json:"name,omitempty"`
	PATSet      bool      `json:"pat_set"`
	PasswordSet bool      `json:"password_set"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toConnView(c *Connection) ConnView {
	return ConnView{
		ID: c.ID, Platform: c.Platform, BaseURL: c.BaseURL,
		Login: c.Login, AvatarURL: c.AvatarURL, Name: c.Name,
		PATSet: c.PAT != "", PasswordSet: c.Password != "",
		Active: c.Active, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// ConnRepo 平台仓库浏览的归一化投影（两平台字段对齐；taken_over 由引擎按
// CloneURL 对照已接管仓库回填）。
type ConnRepo struct {
	ExtID         string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Visibility    string `json:"visibility"` // private | internal | public
	WebURL        string `json:"web_url"`
	Archived      bool   `json:"archived,omitempty"`
	LastActivity  string `json:"last_activity,omitempty"`
	TakenOver     bool   `json:"taken_over"`
}

// CreateProjectInput 远程建仓入参。
type CreateProjectInput struct {
	Name        string
	Description string
	Visibility  string // private | internal | public（GitHub 无 internal，折算 private）
	AutoInit    bool   // 初始化 README
}

// GitLabCred 活动 GitLab 连接的已解析凭证（pipeline 客户端与转修桥的数据源）。
type GitLabCred struct {
	ConnID  string
	BaseURL string
	Token   string
	PAT     bool // true = PAT（稳定）；false = password-grant access_token（2h）
}

// ReposPage 一页浏览结果。
type ReposPage struct {
	Repos    []ConnRepo `json:"repos"`
	Page     int        `json:"page"`
	NextPage int        `json:"next_page"` // 0 = 没有更多
	Total    int        `json:"total"`     // 本页归一化后条数（近似总量见 has more）
}

// ---- 状态访问 ----

// ListConnections 连接清单（掩码；空表返回非 nil——JSON null 会被前端 grf 客户端
// 的整包回落误当对象迭代）。
func (e *Engine) ListConnections() []ConnView {
	out := make([]ConnView, 0)
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			out = append(out, toConnView(c))
		}
	})
	return out
}

// HasActiveGitLab 是否存在活动 GitLab 连接（纯状态查询，不触网——pipeline
// configured() 判定用）。
func (e *Engine) HasActiveGitLab() bool {
	var ok bool
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			if c.Platform == PlatformGitLab && c.Active {
				ok = true
				break
			}
		}
	})
	return ok
}

// ActiveGitLab 解析活动 GitLab 连接凭证（无连接返回 nil, nil；令牌解析失败
// 返回错误——password grant 兑换不了应让调用方看到原因）。
func (e *Engine) ActiveGitLab(ctx context.Context) (*GitLabCred, error) {
	var snap *Connection
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			if c.Platform == PlatformGitLab && c.Active {
				cp := *c
				snap = &cp
				break
			}
		}
	})
	if snap == nil {
		return nil, nil
	}
	tok, err := e.connBearer(ctx, snap)
	if err != nil {
		return nil, err
	}
	return &GitLabCred{ConnID: snap.ID, BaseURL: snap.BaseURL, Token: tok, PAT: snap.PAT != ""}, nil
}

// InvalidateActiveGitLabToken 作废活动 GitLab 连接的 password-grant 令牌缓存
// （pipeline 401 重兑入口；PAT 模式无缓存，no-op）。
func (e *Engine) InvalidateActiveGitLabToken() {
	var snap *Connection
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			if c.Platform == PlatformGitLab && c.Active {
				cp := *c
				snap = &cp
				break
			}
		}
	})
	if snap == nil {
		return
	}
	e.clearConnToken(connTokenKey(snap))
}

// GitHubPAT 首个 GitHub 连接的 PAT（issue 同步的连接优先凭据；无则空）。
func (e *Engine) GitHubPAT() string {
	var pat string
	e.st.View(func(s *State) {
		for _, c := range s.Connections {
			if c.Platform == PlatformGitHub && c.PAT != "" {
				pat = c.PAT
				break
			}
		}
	})
	return pat
}

// ---- 连接管理 ----

// AddConnection 新建连接：先验凭据（平台 /user 回填 login/avatar），失败给
// 可读错误不入库；GitLab 首个连接自动置为活动。
func (e *Engine) AddConnection(ctx context.Context, in ConnInput) (ConnView, error) {
	in.PAT = strings.TrimSpace(in.PAT)
	in.User = strings.TrimSpace(in.User)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	switch in.Platform {
	case PlatformGitLab:
		if in.BaseURL == "" {
			in.BaseURL = "https://git.gorun.fun"
		}
		if in.PAT == "" && (in.User == "" || in.Password == "") {
			return ConnView{}, errors.New("GitLab 连接需要 PAT 或「用户名 + 密码」二选一")
		}
	case PlatformGitHub:
		if in.BaseURL == "" {
			in.BaseURL = "https://github.com" // 缺省 github.com；自定义地址按 GHES 解释（API 根 /api/v3）
		}
		if in.PAT == "" {
			return ConnView{}, errors.New("GitHub 连接需要 Personal Access Token（平台已停用账密授权）")
		}
	default:
		return ConnView{}, fmt.Errorf("不支持的平台：%q（gitlab / github）", in.Platform)
	}
	if u, err := url.Parse(in.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ConnView{}, errors.New("实例地址必须是 http(s) URL")
	}
	c := &Connection{
		Platform: in.Platform, BaseURL: in.BaseURL,
		PAT: in.PAT, User: in.User, Password: in.Password,
	}
	// 同键旧缓存可能对应旧密码（换凭据重连），先作废再验证
	e.clearConnToken(connTokenKey(c))
	if err := e.verifyConn(ctx, c); err != nil {
		return ConnView{}, err
	}
	now := time.Now()
	var out ConnView
	err := e.st.Mutate(func(s *State) error {
		c.ID = newConnID(s)
		c.CreatedAt, c.UpdatedAt = now, now
		if c.Platform == PlatformGitLab {
			c.Active = true // 首个 gitlab 连接即数据源；此后切换走 activate
			for _, o := range s.Connections {
				if o.ID != c.ID && o.Platform == PlatformGitLab {
					c.Active = false
					break
				}
			}
		}
		s.Connections = append(s.Connections, c)
		out = toConnView(c)
		return nil
	})
	return out, err
}

// TestConnection 重验连接（刷新 login/avatar；凭据无效给可读错误）。
func (e *Engine) TestConnection(ctx context.Context, id string) (ConnView, error) {
	snap, ok := e.connByID(id)
	if !ok {
		return ConnView{}, ErrConnNotFound
	}
	e.clearConnToken(connTokenKey(&snap)) // 强制重兑（验证码类过期最常见）
	if err := e.verifyConn(ctx, &snap); err != nil {
		return ConnView{}, err
	}
	var out ConnView
	err := e.st.Mutate(func(s *State) error {
		c := s.conn(id)
		if c == nil {
			return ErrConnNotFound
		}
		c.Login, c.AvatarURL, c.UpdatedAt = snap.Login, snap.AvatarURL, time.Now()
		out = toConnView(c)
		return nil
	})
	return out, err
}

// DeleteConnection 删除连接：引用它的仓库回落仓级凭据（connection_id 置空）；
// 删的是活动 GitLab 连接时，剩余首个 GitLab 连接接任（无则回落 env）。
func (e *Engine) DeleteConnection(id string) error {
	snap, ok := e.connByID(id)
	if !ok {
		return ErrConnNotFound
	}
	return e.st.Mutate(func(s *State) error {
		conns := make([]*Connection, 0, len(s.Connections))
		for _, c := range s.Connections {
			if c.ID != id {
				conns = append(conns, c)
			}
		}
		s.Connections = conns
		for _, r := range s.Repos {
			if r.ConnectionID == id {
				r.ConnectionID, r.Platform = "", "" // 回落仓级凭据
			}
		}
		if snap.Platform == PlatformGitLab && snap.Active {
			for _, c := range s.Connections { // 剩余首个 gitlab 接任
				if c.Platform == PlatformGitLab {
					c.Active = true
					break
				}
			}
		}
		return nil
	})
}

// ActivateConnection 切换活动 GitLab 连接（流水线数据源；请求期实时读，
// 切换即生效——调用方负责失效 pipeline 聚合缓存）。
func (e *Engine) ActivateConnection(id string) error {
	var platform string
	err := e.st.Mutate(func(s *State) error {
		c := s.conn(id)
		if c == nil {
			return ErrConnNotFound
		}
		platform = c.Platform
		if c.Platform != PlatformGitLab {
			return errors.New("仅 GitLab 连接需要激活（GitHub 连接无需切换）")
		}
		for _, o := range s.Connections {
			if o.Platform == PlatformGitLab {
				o.Active = o.ID == id
			}
		}
		return nil
	})
	if err == nil && platform == PlatformGitLab {
		e.InvalidateActiveGitLabToken()
	}
	return err
}

// ConnectRepo 把仓库挂到连接（凭据动态解析，清空仓级拷贝凭据）——账号浏览
// 一键接管与转修桥的连接模式注册用。挂接后异步触发一轮 issue 首同步。
func (e *Engine) ConnectRepo(repoID, connID, platform string) error {
	err := e.st.Mutate(func(s *State) error {
		r := s.repo(repoID)
		if r == nil {
			return ErrRepoNotFound
		}
		if s.conn(connID) == nil {
			return ErrConnNotFound
		}
		r.ConnectionID, r.Platform = connID, platform
		r.GitUser, r.GitToken = "", "" // 连接令牌运行期解析，不留拷贝
		return nil
	})
	if err == nil {
		e.kickFirstSync(repoID)
	}
	return err
}

// ---- 凭据解析（git 推送链）----

// repoAuth 仓库推送凭据：connection_id → 连接当前令牌（gitlab `oauth2:` /
// github `x-access-token:` 作用户名）→ 仓级 GitUser/GitToken。连接令牌解析
// 失败（如 password grant 兑换失败）回落仓级凭据，不阻断取回令牌前的操作。
func (e *Engine) repoAuth(ctx context.Context, r *Repo) (user, token string) {
	if r.ConnectionID != "" {
		if snap, ok := e.connByID(r.ConnectionID); ok {
			if tok, err := e.connBearer(ctx, &snap); err == nil && tok != "" {
				if snap.Platform == PlatformGitHub {
					return "x-access-token", tok
				}
				return "oauth2", tok
			}
		}
	}
	return r.GitUser, r.GitToken
}

func (e *Engine) connByID(id string) (Connection, bool) {
	var snap Connection
	var ok bool
	e.st.View(func(s *State) {
		if c := s.conn(id); c != nil {
			snap, ok = *c, true
		}
	})
	return snap, ok
}

// connBearer 解析连接 Bearer：PAT 直用；GitLab 账密走 password grant
// （共享 GlTokenCache 兑换与缓存，临期前重取；GitHub 无此模式）。
func (e *Engine) connBearer(ctx context.Context, c *Connection) (string, error) {
	if c.PAT != "" {
		return c.PAT, nil
	}
	if c.Platform != PlatformGitLab || c.User == "" || c.Password == "" {
		return "", errors.New("连接缺少可用凭据（PAT 或 GitLab 账密）")
	}
	return e.glTokens.Exchange(ctx, connTokenKey(c), c.BaseURL, c.User, c.Password)
}

func (e *Engine) clearConnToken(key string) {
	e.glTokens.Invalidate(key)
}

// clearConnRepoCache 仓库增删后失效账号浏览缓存（taken_over 投影随之变化）。
func (e *Engine) clearConnRepoCache() {
	e.connRepoCache.Clear()
}

// connTokenKey password-grant 缓存键（同一账号换密码后旧令牌可精确作废）。
func connTokenKey(c *Connection) string {
	return c.Platform + "|" + c.User + "|" + c.BaseURL
}

// ---- 平台 REST（浏览 / 建仓 / 验证）----

// verifyConn 验证凭据并回填账号信息（GitLab GET /user；GitHub GET /user）。
func (e *Engine) verifyConn(ctx context.Context, c *Connection) error {
	var u struct {
		Login     string `json:"login"`    // github
		Username  string `json:"username"` // gitlab
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	st, _, _, err := e.connRequest(ctx, c, http.MethodGet, "/user", nil, &u)
	if err != nil {
		return err
	}
	if st == http.StatusUnauthorized || st == http.StatusForbidden {
		return errors.New("凭据无效或已过期（检查 PAT 权限 / 账号密码）")
	}
	if st >= 300 {
		return fmt.Errorf("验证失败（%d）", st)
	}
	login := firstNonEmpty(u.Username, u.Login)
	if login == "" {
		return errors.New("验证响应异常（无账号名）")
	}
	c.Login, c.AvatarURL = login, u.AvatarURL
	if c.Name == "" {
		c.Name = firstNonEmpty(u.Name, login)
	}
	return nil
}

// BrowseConnectionRepos 浏览账号下的仓库（30s 缓存；q 服务端搜索——GitLab
// search 参数 / GitHub 本页过滤）。
func (e *Engine) BrowseConnectionRepos(ctx context.Context, id, q string, page int) (ReposPage, error) {
	snap, ok := e.connByID(id)
	if !ok {
		return ReposPage{}, ErrConnNotFound
	}
	if page < 1 {
		page = 1
	}
	if page > connReposMax/connReposPage {
		return ReposPage{}, fmt.Errorf("最多浏览 %d 个仓库（翻页上限）", connReposMax)
	}
	q = strings.TrimSpace(q)
	cacheKey := fmt.Sprintf("%s|%q|%d", id, q, page)
	if page, ok := e.connRepoCache.Get(cacheKey); ok {
		return page, nil
	}
	out, err := e.fetchConnRepos(ctx, &snap, q, page)
	if err != nil {
		return ReposPage{}, err
	}
	e.connRepoCache.Put(cacheKey, out)
	return out, nil
}

func (e *Engine) fetchConnRepos(ctx context.Context, c *Connection, q string, page int) (ReposPage, error) {
	if c.Platform == PlatformGitLab {
		path := fmt.Sprintf("/projects?membership=true&simple=true&order_by=last_activity_at&sort=desc&per_page=%d&page=%d", connReposPage, page)
		if q != "" {
			path += "&search=" + url.QueryEscape(q)
		}
		var rows []struct {
			ID                int    `json:"id"`
			Name              string `json:"name"`
			PathWithNamespace string `json:"path_with_namespace"`
			WebURL            string `json:"web_url"`
			DefaultBranch     string `json:"default_branch"`
			Visibility        string `json:"visibility"`
			Archived          bool   `json:"archived"`
			EmptyRepo         bool   `json:"empty_repo"`
			LastActivityAt    string `json:"last_activity_at"`
		}
		st, hdr, _, err := e.connRequest(ctx, c, http.MethodGet, path, nil, &rows)
		if err != nil {
			return ReposPage{}, err
		}
		if st >= 300 {
			return ReposPage{}, fmt.Errorf("GitLab 项目列表 → %d", st)
		}
		out := ReposPage{Page: page, Repos: []ConnRepo{}}
		if next := hdr.Get("X-Next-Page"); next != "" {
			if n, err := strconv.Atoi(next); err == nil {
				out.NextPage = n
			}
		}
		taken := e.takenOverSet()
		for _, r := range rows {
			if r.EmptyRepo {
				continue
			}
			clone := strings.TrimRight(r.WebURL, "/") + ".git" // simple 投影无克隆地址，按 web_url 推导
			out.Repos = append(out.Repos, ConnRepo{
				ExtID: strconv.Itoa(r.ID), Name: r.Name, Path: r.PathWithNamespace,
				CloneURL: clone, DefaultBranch: r.DefaultBranch, Visibility: r.Visibility,
				WebURL: r.WebURL, Archived: r.Archived, LastActivity: r.LastActivityAt,
				TakenOver: taken[clone],
			})
		}
		out.Total = len(out.Repos)
		return out, nil
	}

	// GitHub：/user/repos 无 search 参数，q 本页过滤
	path := fmt.Sprintf("/user/repos?visibility=all&affiliation=owner&sort=pushed&direction=desc&per_page=%d&page=%d", connReposPage, page)
	var rows []struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		HTMLURL       string `json:"html_url"`
		Archived      bool   `json:"archived"`
		PushedAt      string `json:"pushed_at"`
	}
	st, hdr, _, err := e.connRequest(ctx, c, http.MethodGet, path, nil, &rows)
	if err != nil {
		return ReposPage{}, err
	}
	if st >= 300 {
		return ReposPage{}, fmt.Errorf("GitHub 仓库列表 → %d", st)
	}
	out := ReposPage{Page: page, Repos: []ConnRepo{}}
	if link := hdr.Get("Link"); strings.Contains(link, `rel="next"`) {
		out.NextPage = page + 1
	}
	taken := e.takenOverSet()
	for _, r := range rows {
		if q != "" && !strings.Contains(strings.ToLower(r.FullName), strings.ToLower(q)) {
			continue
		}
		vis := "public"
		if r.Private {
			vis = "private"
		}
		out.Repos = append(out.Repos, ConnRepo{
			ExtID: strconv.Itoa(r.ID), Name: r.Name, Path: r.FullName,
			CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, Visibility: vis,
			WebURL: r.HTMLURL, Archived: r.Archived, LastActivity: r.PushedAt,
			TakenOver: taken[r.CloneURL],
		})
	}
	out.Total = len(out.Repos)
	return out, nil
}

// CreateConnectionProject 在连接账号下远程建仓（GitLab POST /projects /
// GitHub POST /user/repos），返回归一化仓库（可直接接管）。
func (e *Engine) CreateConnectionProject(ctx context.Context, id string, in CreateProjectInput) (ConnRepo, error) {
	snap, ok := e.connByID(id)
	if !ok {
		return ConnRepo{}, ErrConnNotFound
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return ConnRepo{}, ErrProjectNameRequired
	}
	in.Visibility = strings.TrimSpace(in.Visibility)
	if in.Visibility != "private" && in.Visibility != "internal" && in.Visibility != "public" {
		in.Visibility = "private"
	}
	if snap.Platform == PlatformGitLab {
		body := map[string]any{
			"name": in.Name, "visibility": in.Visibility,
			"description": in.Description, "initialize_with_readme": in.AutoInit,
		}
		var r struct {
			ID                int    `json:"id"`
			Name              string `json:"name"`
			PathWithNamespace string `json:"path_with_namespace"`
			WebURL            string `json:"web_url"`
			DefaultBranch     string `json:"default_branch"`
			Visibility        string `json:"visibility"`
		}
		st, _, raw, err := e.connRequest(ctx, &snap, http.MethodPost, "/projects", body, &r)
		if err != nil {
			return ConnRepo{}, err
		}
		if st >= 300 {
			return ConnRepo{}, fmt.Errorf("GitLab 建仓失败（%d）：%s", st, ErrMessage(raw))
		}
		if r.DefaultBranch == "" {
			r.DefaultBranch = "main"
		}
		return ConnRepo{
			ExtID: strconv.Itoa(r.ID), Name: r.Name, Path: r.PathWithNamespace,
			CloneURL:      strings.TrimRight(r.WebURL, "/") + ".git",
			DefaultBranch: r.DefaultBranch, Visibility: r.Visibility, WebURL: r.WebURL,
		}, nil
	}
	body := map[string]any{
		"name": in.Name, "private": in.Visibility != "public",
		"description": in.Description, "auto_init": in.AutoInit,
	}
	var r struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		HTMLURL       string `json:"html_url"`
	}
	st, _, raw, err := e.connRequest(ctx, &snap, http.MethodPost, "/user/repos", body, &r)
	if err != nil {
		return ConnRepo{}, err
	}
	if st >= 300 {
		return ConnRepo{}, fmt.Errorf("GitHub 建仓失败（%d）：%s", st, ErrMessage(raw))
	}
	vis := "public"
	if r.Private {
		vis = "private"
	}
	if r.DefaultBranch == "" {
		r.DefaultBranch = "main"
	}
	return ConnRepo{
		ExtID: strconv.Itoa(r.ID), Name: r.Name, Path: r.FullName,
		CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch,
		Visibility: vis, WebURL: r.HTMLURL,
	}, nil
}

// connRequest 平台 REST 通用请求：GitLab {base}/api/v4/*（Bearer connBearer）；
// GitHub https://api.github.com/*（Bearer PAT）。不因 HTTP 状态报错（调用方
// 自判），返回 状态码 / 响应头 / 原始响应体（≥300 时经 connErrMessage 提取
// 可读信息）。
func (e *Engine) connRequest(ctx context.Context, c *Connection, method, path string, body any, out any) (int, http.Header, []byte, error) {
	tok, err := e.connBearer(ctx, c)
	if err != nil {
		return 0, nil, nil, err
	}
	apiBase := "https://api.github.com"
	switch c.Platform {
	case PlatformGitLab:
		apiBase = strings.TrimRight(c.BaseURL, "/") + "/api/v4"
	case PlatformGitHub:
		if c.BaseURL != "" && c.BaseURL != "https://github.com" {
			apiBase = strings.TrimRight(c.BaseURL, "/") + "/api/v3" // GitHub Enterprise Server
		}
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("平台 API 不可达: %s", ErrShort(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if out != nil && len(raw) > 0 && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, resp.Header, raw, fmt.Errorf("平台响应解析失败: %w", err)
		}
	}
	return resp.StatusCode, resp.Header, raw, nil
}

// takenOverSet 已接管仓库的 CloneURL 集（浏览投影回填 taken_over）。
func (e *Engine) takenOverSet() map[string]bool {
	out := map[string]bool{}
	e.st.View(func(s *State) {
		for _, r := range s.Repos {
			out[r.CloneURL] = true
		}
	})
	return out
}

// ---- env 种子标记 ----

// EnvSeedDone 是否已做过 env→连接 的首次种子（防用户删除后重启复活）。
func (e *Engine) EnvSeedDone() bool {
	var done bool
	e.st.View(func(s *State) { done = s.Settings.EnvSeedDone })
	return done
}

// MarkEnvSeedDone 记录种子已完成。
func (e *Engine) MarkEnvSeedDone() {
	_ = e.st.Mutate(func(s *State) error { s.Settings.EnvSeedDone = true; return nil })
}

// newConnID 连接 ID 生成（state 内查重）。
func newConnID(s *State) string {
	return uniqID("c", func(id string) bool { return s.conn(id) != nil })
}
