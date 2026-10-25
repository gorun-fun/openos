// settings.go — 引擎设置与总览聚合（自 steward.go 按域拆分）。
package steward

import (
	"context"
	"strings"
	"time"
)

// ---- 设置与总览 ----

// SettingsView 设置页数据。
type SettingsView struct {
	ReportKey     string   `json:"report_key"`
	GitHubTokenOn bool     `json:"github_token_set"`
	Model         string   `json:"model"`
	Models        []string `json:"models,omitempty"`
	LLMReady      bool     `json:"llm_ready"`
	GhBin         string   `json:"gh_bin"`
	GitBin        string   `json:"git_bin"`
	TimeoutMin    int      `json:"timeout_min"`
	SyncMin       int      `json:"sync_min"`
}

// GetSettings 设置视图（模型清单实时拉取，失败留空不阻断）。
func (e *Engine) GetSettings(ctx context.Context) SettingsView {
	v := SettingsView{
		ReportKey:  e.EffectiveReportKey(),
		GhBin:      e.ghBin(),
		GitBin:     e.gitBin(),
		LLMReady:   e.llmKey != "",
		TimeoutMin: int(e.opts.Timeout / time.Minute),
		SyncMin:    int(e.opts.SyncEvery / time.Minute),
	}
	e.st.View(func(s *State) {
		v.Model = s.Settings.Model
		if s.Settings.GitHubToken != "" || e.opts.GitHubTokenEnv != "" {
			v.GitHubTokenOn = true
		}
	})
	if v.Model == "" {
		v.Model = e.defModel
	}
	if models, err := e.fetchModels(ctx); err == nil {
		v.Models = models
	}
	return v
}

// SetSettings 更新设置（GitHub Token 留空 = 不改）。
func (e *Engine) SetSettings(model, githubToken string) error {
	model = strings.TrimSpace(model)
	githubToken = strings.TrimSpace(githubToken)
	return e.st.Mutate(func(s *State) error {
		s.Settings.Model = model
		if githubToken != "" {
			s.Settings.GitHubToken = githubToken
		}
		return nil
	})
}

// Overview 桌面小组件/状态条数据。
type Overview struct {
	Pending int  `json:"pending"` // 待确认 + 排队中
	Running int  `json:"running"`
	Fixed   int  `json:"fixed"`
	Failed  int  `json:"failed"`
	LastRun *Run `json:"last_run,omitempty"`
}

func (e *Engine) Overview() Overview {
	var o Overview
	e.st.View(func(s *State) {
		for _, p := range s.Problems {
			switch p.State {
			case PNew, PConfirmed:
				o.Pending++
			case PRunning:
				o.Running++
			case PFixed:
				o.Fixed++
			case PFailed:
				o.Failed++
			}
		}
		if n := len(s.Runs); n > 0 {
			c := *s.Runs[n-1]
			o.LastRun = &c
		}
	})
	return o
}
