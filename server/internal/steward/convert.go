// convert.go — AI 转制上架（特色功能，v0.0.32）：把公开项目转制为 openos 应用
// 并发布到货架。复用修复引擎基建（Goder 会话 / git 操作 / 连接凭据），流程：
//
//	queued → cloning（活动 GitLab 连接下建目标项目 + 克隆源仓库到操作者 home
//	“代码管家/转制/<appid>/”）→ converting（引擎确定性脚手架 vite 单文件构建
//	工程 + .gitlab-ci.yml；goder 会话把源项目核心能力包装为 src/App.vue，
//	引擎提交并强推目标项目 main）→ building（轮询 CI 流水线至终态）→
//	publishing（下载 build-app job 工件 zip → 取 dist/<appid>.js → 写货架
//	files/<appid>/<版本>/ + 原子合并 catalog.json）→ done。
//
// 产物契约（与 openos remote.ts / tooling 一致）：入口 ESM 单文件，default 导出
// Vue 组件；裸名（vue/@openos*/pinia/echarts）一律外部化（宿主 importmap 解析，
// 单实例铁律）；SFC 样式经构建插件内联进 JS。构建放 GitLab CI（运行镜像无
// node）；货架目录 OPENOS_STEWARD_MARKET_DIR 未配置时创建即拒绝。catalog 条目
// 不带 type=host——经应用商店（OPENOS_MARKET_URL 含中央货架）安装到桌面。
// worker 与修复共用单并发与当前运行注册（取消互认）。
package steward

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"openos/server/internal/atomicfile"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 转制状态机
const (
	CQueued     = "queued"
	CCloning    = "cloning"
	CConverting = "converting"
	CBuilding   = "building"
	CPublishing = "publishing"
	CDone       = "done"
	CFailed     = "failed"
	CCanceled   = "canceled"
)

const (
	convPollEvery    = 5 * time.Second
	convWaitPipeline = 90 * time.Second // 推送后等流水线出现的上限
	convArtifactJob  = "build-app"      // .gitlab-ci.yml 里的工件 job 名
	convEntryMax     = 20 << 20         // 入口 JS 大小上限
)

// Conversion 一次 AI 转制任务。
type Conversion struct {
	ID            string     `json:"id"`
	SourceURL     string     `json:"source_url"`
	AppID         string     `json:"app_id"`            // 货架 appId（slug）
	ConnID        string     `json:"conn_id,omitempty"` // 目标 GitLab 连接
	TargetProject int        `json:"target_project,omitempty"`
	TargetPath    string     `json:"target_path,omitempty"` // path_with_namespace
	TargetWeb     string     `json:"target_web,omitempty"`
	PipelineID    int        `json:"pipeline_id,omitempty"`
	Version       string     `json:"version,omitempty"` // 已发布版本
	State         string     `json:"state"`
	Operator      string     `json:"operator,omitempty"` // goder 会话归属
	By            string     `json:"by,omitempty"`       // 发起人显示名
	SessionID     string     `json:"session_id,omitempty"`
	RespID        string     `json:"resp_id,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	Error         string     `json:"error,omitempty"`
	Workspace     string     `json:"workspace,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
}

// ConvInput 发起转制入参。
type ConvInput struct {
	SourceURL string
	Operator  string // goder 会话归属（发起人 openos 用户名；空 = 引擎兜底）
	By        string // 发起人显示名
}

func convTerminal(state string) bool {
	switch state {
	case CDone, CFailed, CCanceled:
		return true
	}
	return false
}

// 预置 appId（宿主 5 应用 + openos 内置），转制产物不得占用。
var reservedAppIDs = map[string]bool{
	"tokenhub": true, "gateway": true, "log": true, "steward": true, "rooms": true,
	"goder": true, "settings": true, "store": true, "files": true, "terminal": true,
	"home": true, "fixer": true, "pipeline": true,
}

var appidRe = regexp.MustCompile(`[^a-z0-9-]+`)

// convAppID 源仓库 URL → 货架 appId（末段去 .git，清洗为小写字母数字连字符）。
func convAppID(sourceURL string) string {
	u, err := url.Parse(strings.TrimSpace(sourceURL))
	if err != nil {
		return ""
	}
	seg := strings.ToLower(pathLastSegment(u.Path))
	seg = appidRe.ReplaceAllString(seg, "-")
	seg = strings.Trim(seg, "-")
	if len(seg) > 30 {
		seg = seg[:30]
	}
	return strings.Trim(seg, "-")
}

// isLocalDir 本地目录（存在的绝对路径——git 可直接 clone）。
func isLocalDir(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func pathLastSegment(p string) string {
	p = strings.TrimSuffix(strings.TrimRight(p, "/"), ".git")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// ListConversions 转制任务清单（时间倒序）。
func (e *Engine) ListConversions() []Conversion {
	out := make([]Conversion, 0)
	e.st.View(func(s *State) {
		for i := len(s.Conversions) - 1; i >= 0; i-- {
			out = append(out, *s.Conversions[i])
		}
	})
	return out
}

func (e *Engine) GetConversion(id string) (Conversion, bool) {
	var out Conversion
	var ok bool
	e.st.View(func(s *State) {
		for _, c := range s.Conversions {
			if c.ID == id {
				out, ok = *c, true
				break
			}
		}
	})
	return out, ok
}

// CreateConversion 发起转制：校验源地址 / appId 可用 / 前置条件在场，入队。
func (e *Engine) CreateConversion(in ConvInput) (Conversion, error) {
	in.SourceURL = strings.TrimSpace(in.SourceURL)
	if in.SourceURL == "" {
		return Conversion{}, errors.New("源仓库地址必填")
	}
	// https URL（公开仓库），或存在的本地绝对路径（内网镜像 / 测试裸库）
	if u, err := url.Parse(in.SourceURL); err != nil || ((u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		if !isLocalDir(in.SourceURL) {
			return Conversion{}, errors.New("源仓库地址必须是 https URL（如 https://github.com/xxx/yyy）")
		}
	}
	appid := convAppID(in.SourceURL)
	if appid == "" {
		return Conversion{}, errors.New("无法从地址解析出应用 id（仓库路径末段）")
	}
	if reservedAppIDs[appid] {
		return Conversion{}, fmt.Errorf("应用 id %q 是平台预置应用，换个源仓库", appid)
	}
	if !e.HasActiveGitLab() {
		return Conversion{}, errors.New("转制需要活动 GitLab 连接（在「仓库」页连接并设为数据源）——目标项目与 CI 构建都落在 GitLab")
	}
	if e.opts.MarketDir == "" {
		return Conversion{}, errors.New("货架目录未配置（OPENOS_STEWARD_MARKET_DIR，生产为中央货架挂载路径），无法发布")
	}
	op := firstNonEmpty(in.Operator, e.opts.DefaultOperator)
	if op == "" {
		return Conversion{}, errors.New("未指定转制操作者（发起人 goder 归属），且未配置 OPENOS_STEWARD_GODER_UID")
	}
	var out Conversion
	err := e.st.Mutate(func(s *State) error {
		for _, c := range s.Conversions {
			if c.AppID == appid && !convTerminal(c.State) {
				return fmt.Errorf("应用 %q 已有进行中的转制任务（%s）", appid, c.ID)
			}
		}
		cv := &Conversion{
			ID: uniqID("cv", func(id string) bool {
				for _, c := range s.Conversions {
					if c.ID == id {
						return true
					}
				}
				return false
			}),
			SourceURL: in.SourceURL, AppID: appid,
			State: CQueued, Operator: op, By: in.By,
			StartedAt: time.Now(),
		}
		s.Conversions = append(s.Conversions, cv)
		out = *cv
		return nil
	})
	if err == nil {
		e.Notify()
	}
	return out, err
}

// CancelConversion 取消进行中的转制（与修复共用当前运行注册）。
func (e *Engine) CancelConversion(id string) bool { return e.CancelRun(id) }

// workOneConvert 取队执行一个 queued 转制；无可做返回 false（问题修复优先）。
func (e *Engine) workOneConvert(ctx context.Context) bool {
	var id string
	e.st.Mutate(func(s *State) error {
		for _, c := range s.Conversions {
			if c.State == CQueued {
				c.State = CCloning
				id = c.ID
				break
			}
		}
		return nil
	})
	if id == "" {
		return false
	}
	e.executeConvert(ctx, id)
	return true
}

func (e *Engine) setConv(id string, f func(c *Conversion)) {
	_ = e.st.Mutate(func(s *State) error {
		for _, c := range s.Conversions {
			if c.ID == id {
				f(c)
				break
			}
		}
		return nil
	})
}

// executeConvert 转制全流程。中断语义与修复一致（见 runner.go）：rctx 取消时
// 引擎级 ctx 已取消 = 宿主停机，否则 = 人工取消/超时，均归 canceled 可重新发起。
func (e *Engine) executeConvert(ctx context.Context, id string) {
	var conv Conversion
	e.st.View(func(s *State) {
		for _, c := range s.Conversions {
			if c.ID == id {
				conv = *c
				break
			}
		}
	})
	if conv.ID == "" {
		return
	}
	lg, err := e.openRunLog(id)
	if err != nil {
		e.logf("[steward] 打开转制日志失败 %s: %v", id, err)
		lg = nil
	}
	defer func() {
		if lg != nil {
			_ = lg.f.Sync()
			_ = lg.f.Close()
		}
	}()
	if lg != nil {
		lg.Printf("转制开始：%s → openos 应用 %s（操作者 %s）", conv.SourceURL, conv.AppID, conv.Operator)
	}
	finish := func(state, errMsg string) {
		now := time.Now()
		e.setConv(id, func(c *Conversion) { c.State, c.Error, c.EndedAt = state, errMsg, &now })
		if lg != nil && errMsg != "" {
			lg.Printf("结束（%s）：%s", state, errMsg)
		}
	}
	rctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()
	e.setCurrent(id, cancel)
	defer e.clearCurrent(id)

	interrupted := func() (bool, string) {
		if rctx.Err() == nil {
			return false, ""
		}
		if ctx.Err() != nil {
			return true, "宿主停机中断，可重新发起"
		}
		return true, "人工取消或超时，可重新发起"
	}

	// 1. cloning：目标项目 + 克隆源仓库。目标项目优先复用（本记录落档的，或
	//    同 appid 更早转制已建的）——重跑失败转制 / 已上架应用升级版本时再走
	//    CreateConnectionProject 会撞 GitLab 同名项目（name has already been
	//    taken）而永远失败
	cred, err := e.ActiveGitLab(rctx)
	if err != nil || cred == nil {
		finish(CFailed, "活动 GitLab 连接解析失败: "+firstNonEmpty(ErrShort(err), "无活动连接"))
		return
	}
	e.setConv(id, func(c *Conversion) { c.ConnID = cred.ConnID })
	connSnap, _ := e.connByID(cred.ConnID)
	target := e.reuseConvTarget(&conv)
	if target == nil {
		created, err := e.CreateConnectionProject(rctx, cred.ConnID, CreateProjectInput{
			Name: "openos-app-" + conv.AppID, Visibility: "private", AutoInit: true,
		})
		if err != nil {
			if ok, msg := interrupted(); ok {
				finish(CCanceled, msg)
				return
			}
			finish(CFailed, "目标项目创建失败: "+err.Error())
			return
		}
		target = &created
		if lg != nil {
			lg.Printf("目标项目已建：%s（#%s）", target.Path, target.ExtID)
		}
	} else if lg != nil {
		lg.Printf("复用既有目标项目：%s（#%s）", target.Path, target.ExtID)
	}
	e.setConv(id, func(c *Conversion) {
		c.TargetProject, c.TargetPath, c.TargetWeb = atoiOr(target.ExtID, 0), target.Path, target.WebURL
	})

	ws := e.conversionWorkspace(&conv)
	_ = os.RemoveAll(ws)
	if err := os.MkdirAll(filepath.Dir(ws), 0o755); err != nil {
		finish(CFailed, "创建转制工作区失败: "+err.Error())
		return
	}
	if _, err := e.runCmd(rctx, "", lg, e.gitBin(), "clone", "--quiet", conv.SourceURL, ws); err != nil {
		if ok, msg := interrupted(); ok {
			finish(CCanceled, msg)
			return
		}
		finish(CFailed, "克隆源仓库失败（确认是公开仓库且地址可克隆）: "+err.Error())
		return
	}
	e.setConv(id, func(c *Conversion) { c.Workspace = ws })
	if lg != nil {
		lg.Printf("源仓库已克隆：%s", ws)
	}

	// 2. converting：确定性脚手架 + goder 会话转制 + 提交强推
	e.setConv(id, func(c *Conversion) { c.State = CConverting })
	if err := scaffoldAppProject(ws, conv.AppID); err != nil {
		finish(CFailed, "脚手架写入失败: "+err.Error())
		return
	}
	summary, err := e.convertViaGoder(rctx, id, &conv, ws, e.resolveModel(rctx), lg)
	if err != nil {
		if ok, msg := interrupted(); ok {
			finish(CCanceled, msg)
			return
		}
		if errors.Is(err, errCanceled) {
			finish(CCanceled, "人工取消，可重新发起")
			return
		}
		finish(CFailed, err.Error())
		return
	}
	e.setConv(id, func(c *Conversion) { c.Summary = truncateRunesFor(summary, 1200) })
	commit, err := e.commitAll(rctx, ws, fmt.Sprintf("feat: AI 转制 openos 应用 %s", conv.AppID), lg)
	if err != nil {
		finish(CFailed, "提交转制产物失败: "+err.Error())
		return
	}
	// 目标项目带 AutoInit 初始提交，源历史直推 main 需 force（项目为转制专属私有仓）
	gitUser, gitTok := e.repoAuth(rctx, &Repo{ConnectionID: cred.ConnID, Platform: PlatformGitLab})
	if _, err := e.runCmd(rctx, ws, lg, e.gitBin(), "push", "--quiet", "--force",
		authURL(target.CloneURL, gitUser, gitTok), commit+":refs/heads/main"); err != nil {
		finish(CFailed, "推送目标项目失败: "+err.Error())
		return
	}
	if lg != nil {
		lg.Printf("已推送目标项目 main（commit %s）", commit)
	}

	// 3. building：轮询 CI 流水线至终态
	e.setConv(id, func(c *Conversion) { c.State = CBuilding })
	pl, err := e.waitTargetPipeline(rctx, &connSnap, target.ExtID, lg)
	if err != nil {
		if ok, msg := interrupted(); ok {
			finish(CCanceled, msg)
			return
		}
		finish(CFailed, err.Error())
		return
	}
	if pl.Status != "success" {
		finish(CFailed, fmt.Sprintf("CI 构建失败（状态 %s），流水线：%s/-/pipelines/%d",
			pl.Status, strings.TrimRight(cred.BaseURL, "/"), pl.ID))
		return
	}
	e.setConv(id, func(c *Conversion) { c.PipelineID = pl.ID })
	if lg != nil {
		lg.Printf("CI 构建成功（流水线 #%d）", pl.ID)
	}

	// 4. publishing：工件回采 + 货架发布
	e.setConv(id, func(c *Conversion) { c.State = CPublishing })
	js, manifest, err := e.fetchArtifact(rctx, &connSnap, target.ExtID, ws, conv.AppID)
	if err != nil {
		if ok, msg := interrupted(); ok {
			finish(CCanceled, msg)
			return
		}
		finish(CFailed, err.Error())
		return
	}
	version, err := e.publishToMarket(manifest, js)
	if err != nil {
		finish(CFailed, err.Error())
		return
	}
	if lg != nil {
		lg.Printf("已发布货架：%s %s（entry files/%s/%s/%s.js）", manifest.AppID, version, manifest.AppID, version, manifest.AppID)
	}
	e.setConv(id, func(c *Conversion) { c.Version = version })
	finish(CDone, "")
}

// reuseConvTarget 转制目标项目复用：本记录已落档（失败重跑）或同 appid 更早
// 转制已建成（升级版本）时返回既有项目投影（CloneURL 按 web_url 推导，与
// 建仓投影同规则）；无可用返回 nil。
func (e *Engine) reuseConvTarget(c *Conversion) *ConnRepo {
	var proj int
	var projPath, web string
	e.st.View(func(s *State) {
		if c.TargetProject > 0 && c.TargetWeb != "" {
			proj, projPath, web = c.TargetProject, c.TargetPath, c.TargetWeb
			return
		}
		for _, cv := range s.Conversions {
			if proj == 0 && cv.ID != c.ID && cv.AppID == c.AppID && cv.TargetProject > 0 && cv.TargetWeb != "" {
				proj, projPath, web = cv.TargetProject, cv.TargetPath, cv.TargetWeb
			}
		}
	})
	if proj == 0 {
		return nil
	}
	web = strings.TrimRight(web, "/")
	return &ConnRepo{
		ExtID: strconv.Itoa(proj), Path: projPath, WebURL: web,
		CloneURL: web + ".git", DefaultBranch: "main",
	}
}

// conversionWorkspace 转制工作区：操作者 home「代码管家/转制/<appid>」（文件
// App 可见可下载，goder 会话同址）；无 homes 根退回内部目录。
func (e *Engine) conversionWorkspace(c *Conversion) string {
	op := firstNonEmpty(c.Operator, e.opts.DefaultOperator)
	if op == "" || e.opts.HomesDir == "" {
		return filepath.Join(e.opts.DataDir, "convert", sanitizeDirName(c.AppID, c.ID))
	}
	return filepath.Join(e.opts.HomesDir, sanitizeDirName(op, "user"), "代码管家", "转制", sanitizeDirName(c.AppID, c.ID))
}

// ---- 目标流水线轮询与工件回采 ----

type convPipeline struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
}

// waitTargetPipeline 推送后轮询目标项目 main 的最新流水线至终态（等出现上限
// convWaitPipeline；终态判定沿用 GitLab 语义，manual 视为未完成继续等）。
func (e *Engine) waitTargetPipeline(ctx context.Context, conn *Connection, projectExtID string, lg *runLog) (*convPipeline, error) {
	deadline := time.Now().Add(convWaitPipeline)
	var last *convPipeline
	for {
		var pls []convPipeline
		path := fmt.Sprintf("/projects/%s/pipelines?ref=main&per_page=1&order_by=id&sort=desc", projectExtID)
		st, _, _, err := e.connRequest(ctx, conn, http.MethodGet, path, nil, &pls)
		if err != nil {
			return nil, err
		}
		if st >= 300 {
			return nil, fmt.Errorf("流水线查询 → %d", st)
		}
		if len(pls) > 0 {
			cur := pls[0]
			if last == nil {
				last = &convPipeline{ID: cur.ID, Status: cur.Status}
				e.setConvOfPipeline(cur.ID)
				if lg != nil {
					lg.Printf("CI 流水线 #%d（%s）", cur.ID, cur.Status)
				}
			} else {
				last.Status = cur.Status
			}
			switch last.Status {
			case "success", "failed", "canceled", "skipped":
				return last, nil
			}
		} else if time.Now().After(deadline) {
			return nil, errors.New("推送后未出现 CI 流水线（Runner 未注册或 .gitlab-ci.yml 未生效），可在 GitLab 查看目标项目")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(convPollEvery):
		}
	}
}

// setConvOfPipeline 把流水线号记到当前转制（日志线程里拿不到 id，经 cur 传递）。
func (e *Engine) setConvOfPipeline(pipelineID int) {
	e.mu.Lock()
	id := e.cur.id
	e.mu.Unlock()
	if id == "" {
		return
	}
	e.setConv(id, func(c *Conversion) { c.PipelineID = pipelineID })
}

// fetchArtifact 下载 build-app job 工件 zip，取 dist/<appid>.js 与 manifest。
func (e *Engine) fetchArtifact(ctx context.Context, conn *Connection, projectExtID, ws, appid string) ([]byte, appManifest, error) {
	apiPath := fmt.Sprintf("/projects/%s/jobs/artifacts/main/download?job=%s", projectExtID, convArtifactJob)
	st, _, raw, err := e.connRequest(ctx, conn, http.MethodGet, apiPath, nil, nil)
	if err != nil {
		return nil, appManifest{}, err
	}
	if st == http.StatusNotFound {
		return nil, appManifest{}, fmt.Errorf("工件不存在（job %s 未产出 artifacts，检查 .gitlab-ci.yml）", convArtifactJob)
	}
	if st != http.StatusOK {
		return nil, appManifest{}, fmt.Errorf("工件下载 → %d", st)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, appManifest{}, fmt.Errorf("工件 zip 解析失败: %w", err)
	}
	want := "dist/" + appid + ".js"
	var js, fallback []byte
	for _, f := range zr.File {
		if f.UncompressedSize64 > convEntryMax || f.UncompressedSize64 == 0 {
			continue
		}
		// zip 条目名按规范恒为 / 分隔，用 path 而非 filepath（后者随平台分隔符）
		if f.Name != want && !(path.Dir(f.Name) == "dist" && strings.HasSuffix(f.Name, ".js")) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(rc, convEntryMax))
		rc.Close()
		if f.Name == want {
			js = body
		} else if fallback == nil {
			fallback = body
		}
	}
	if js == nil {
		js = fallback
	}
	if js == nil {
		return nil, appManifest{}, fmt.Errorf("工件中未找到 %s（dist 下无入口 JS）", want)
	}
	manifest, err := readAppManifest(ws, appid)
	if err != nil {
		return nil, appManifest{}, err
	}
	return js, manifest, nil
}

// ---- 货架发布 ----

// appManifest 转制工程的 manifest.json（AI 维护名称/图标/分类/描述）。
type appManifest struct {
	AppID       string `json:"id"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Category    string `json:"category"`
	Description string `json:"description"`
}

func readAppManifest(ws, appid string) (appManifest, error) {
	var m appManifest
	raw, err := os.ReadFile(filepath.Join(ws, "manifest.json"))
	if err != nil {
		return m, fmt.Errorf("manifest.json 缺失（转制会话未产出）: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("manifest.json 解析失败: %w", err)
	}
	if m.AppID == "" {
		m.AppID = appid
	}
	if m.AppID != appid {
		return m, fmt.Errorf("manifest.json 的 id（%s）与应用 id（%s）不一致", m.AppID, appid)
	}
	m.Name = firstNonEmpty(m.Name, appid)
	m.Icon = firstNonEmpty(m.Icon, "🧩")
	m.Category = firstNonEmpty(m.Category, "工具")
	return m, nil
}

// publishToMarket 入口 JS 写入货架版本目录 + 原子合并 catalog.json（不带
// type=host，可经商店安装）；版本号沿用仓库纪律（末位递增，目录冲突再进位）。
func (e *Engine) publishToMarket(m appManifest, js []byte) (string, error) {
	dir := e.opts.MarketDir
	if dir == "" {
		return "", errors.New("货架目录未配置（OPENOS_STEWARD_MARKET_DIR）")
	}
	catalog := readMarketCatalog(dir)
	version := "v0.0.1"
	if old := catalog.Apps[m.AppID]; old != nil {
		if v, ok := old["version"].(string); ok && v != "" {
			version = bumpPatchTag(v)
		}
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "files", m.AppID, version)); os.IsNotExist(err) {
			break
		}
		version = bumpPatchTag(version) // 极小概率撞已发布版本，进位
	}
	vDir := filepath.Join(dir, "files", m.AppID, version)
	if err := os.MkdirAll(vDir, 0o755); err != nil {
		return "", fmt.Errorf("创建货架目录失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(vDir, m.AppID+".js"), js, 0o644); err != nil {
		return "", fmt.Errorf("写入入口产物失败: %w", err)
	}
	if catalog.Apps == nil {
		catalog.Apps = map[string]map[string]any{}
	}
	catalog.Apps[m.AppID] = map[string]any{
		"version":     version,
		"entry":       fmt.Sprintf("files/%s/%s/%s.js", m.AppID, version, m.AppID),
		"name":        m.Name,
		"icon":        m.Icon,
		"category":    m.Category,
		"description": m.Description,
	}
	catalog.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := writeMarketCatalog(dir, catalog); err != nil {
		return "", err
	}
	return version, nil
}

// marketCatalog 货架 catalog.json 文档（apps 值字段松散——host 条目带 type 等）。
type marketCatalog struct {
	Version   int                       `json:"version"`
	UpdatedAt string                    `json:"updatedAt,omitempty"`
	Apps      map[string]map[string]any `json:"apps"`
}

func readMarketCatalog(dir string) marketCatalog {
	var c marketCatalog
	raw, _ := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &c)
	}
	if c.Version == 0 {
		c.Version = 1
	}
	return c
}

func writeMarketCatalog(dir string, c marketCatalog) error {
	return atomicfile.WriteJSON(filepath.Join(dir, "catalog.json"), c, 0o644)
}

// MarketApps 货架应用清单（应用页展示；未配置货架返回 market_configured=false）。
func (e *Engine) MarketApps() (map[string]any, error) {
	dir := e.opts.MarketDir
	if dir == "" {
		return map[string]any{"market_configured": false, "apps": []map[string]any{}}, nil
	}
	catalog := readMarketCatalog(dir)
	apps := make([]map[string]any, 0, len(catalog.Apps))
	for id, ent := range catalog.Apps {
		proj := map[string]any{"id": id}
		for k, v := range ent {
			proj[k] = v
		}
		apps = append(apps, proj)
	}
	return map[string]any{"market_configured": true, "market_dir": dir, "apps": apps}, nil
}

// ---- 脚手架（确定性部分：构建工程 + CI，AI 只写 src/App.vue 与 manifest）----

func scaffoldAppProject(ws, appid string) error {
	files := map[string]string{
		"package.json": fmt.Sprintf(`{
  "name": %q,
  "private": true,
  "version": "0.0.0",
  "type": "module"
}
`, appid),
		"vite.config.js": fmt.Sprintf(`import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

// SFC 样式内联进入口 JS（模块求值时注入 <style>）——产物单文件无外部资产
function cssInjectPlugin(id) {
  return {
    name: 'css-inject',
    closeBundle() {
      const dir = resolve(here, 'dist')
      const entry = resolve(dir, id + '.js')
      let inject = ''
      for (const f of readdirSync(dir)) {
        if (f.endsWith('.css')) {
          inject += ';(function(){var d=document,s=d.createElement("style");s.textContent=' +
            JSON.stringify(readFileSync(resolve(dir, f), 'utf8')) + ';d.head.appendChild(s)})();\n'
        }
      }
      if (inject) writeFileSync(entry, inject + readFileSync(entry, 'utf8'))
    },
  }
}

export default defineConfig({
  plugins: [vue(), cssInjectPlugin(%q)],
  css: { transformer: 'postcss' },
  build: {
    outDir: 'dist',
    cssCodeSplit: false,
    lib: { entry: resolve(here, 'src/main.js'), formats: ['es'], fileName: () => %q + '.js' },
    // 裸名一律外部化：宿主页面 importmap 解析（vue/@openos*/pinia/echarts 单实例）
    rollupOptions: { external: [/^vue($|\/)/, /^@vue($|\/)/, /^pinia($|\/)/, /^@openos($|\/)/, /^@openos-all($|\/)/, /^echarts($|\/)/] },
  },
})
`, appid, appid),
		".gitlab-ci.yml": `build-app:
  stage: build
  image: node:22-alpine
  script:
    - npm install --no-save vite@^8 @vitejs/plugin-vue@^6
    - npx vite build
  artifacts:
    paths:
      - dist/
`,
		"src/main.js": `import App from './App.vue'

export default App
`,
		"manifest.json": fmt.Sprintf(`{
  "id": %q,
  "name": %q,
  "icon": "🧩",
  "category": "工具",
  "description": ""
}
`, appid, appid),
	}
	for path, body := range files {
		full := filepath.Join(ws, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return fmt.Errorf("写 %s: %w", path, err)
		}
	}
	return nil
}

// buildConvertTask 拼给 goder 的转制任务描述。
func buildConvertTask(c *Conversion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "把当前工作区（git 仓库，已克隆源项目 %s）转制为一个 openos 桌面应用。\n\n", c.SourceURL)
	b.WriteString(`工程已为你脚手架就绪（不要改动这些文件）：
- vite.config.js：单文件 ESM 构建（入口 dist/` + c.AppID + `.js，CSS 自动内联，裸名依赖外部化）
- src/main.js：入口（default 导出 App 组件）
- .gitlab-ci.yml：CI 构建 job（产出 dist/ 工件）
- package.json

你的任务：
1. 通读源项目的 README 与代码，理解它的核心能力。
2. 编写 src/App.vue（可拆分 src/ 下的子组件与模块）：把核心能力包装成一个
   可用的 openos 桌面应用窗口界面。按项目形态选择呈现方式：
   - CLI / 服务类项目 → 管理面板（配置表单、状态展示、结果列表）；
   - 已有 Web UI 的项目 → 把界面移植为 Vue 组件；
   - 数据 / 算法类项目 → 交互演示或参数化运行界面。
3. 组件要求：
   - Vue 3 <script setup> 组合式 API；UI 组件用 @openos/ui（OsButton、OsInput、
     OsTable、OsModal、OsPanel、toast 等，裸名导入，构建时外部化）；
   - 图表可用 echarts（裸名导入）；样式写在 SFC <style scoped>，用 openos 的
     CSS 变量（--os-input-bg / --os-border / --os-text / --os-accent 等）适配
     明暗两态；
   - 应用是独立窗口：不依赖宿主会话与 /api/v1；需要后端的项目做成可配置
     目标地址的形式，或用 localStorage 持久化；
   - 界面文案用中文，布局清晰。
4. 更新 manifest.json 的 name / icon（一个 emoji）/ category（工具|办公|AI|
   运维|游戏 之一）/ description（一句话简介）——id 保持 ` + c.AppID + ` 不变。
5. 不要执行 git commit / push——提交与推送由外部系统完成。

完成后用两三句话总结应用做了什么。`)
	return b.String()
}

// convertViaGoder 转制的 goder 路径（复用修复执行器的会话生命周期，落
// 「代码管家」分组，引擎只跟状态；会话指针记录在 Conversion 上）。
func (e *Engine) convertViaGoder(ctx context.Context, convID string, c *Conversion, workspace, model string, lg *runLog) (string, error) {
	op := firstNonEmpty(c.Operator, e.opts.DefaultOperator)
	if op == "" {
		return "", errors.New("未指定转制操作者")
	}
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("工作区路径解析失败: %w", err)
	}
	sid, rid, usedModel, err := e.opts.Goder.CreateTask(ctx, GoderTask{
		UID: op, Workspace: ws, Task: buildConvertTask(c), Model: model,
		Title: fmt.Sprintf("转制 · %s", c.AppID), Group: goderGroup,
	})
	if err != nil {
		return "", fmt.Errorf("创建 Goder 会话失败: %s", cleanHostErr(err))
	}
	e.setConv(convID, func(x *Conversion) { x.SessionID, x.RespID = sid, rid })
	if lg != nil {
		lg.Printf("Goder 转制会话已建（操作者 %s，模型 %s）：%s", op, usedModel, sid)
	}
	return e.goderAwait(ctx, op, rid, sid, "转制", lg)
}

func atoiOr(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		return n
	}
	return def
}
