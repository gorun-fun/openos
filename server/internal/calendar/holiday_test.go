package calendar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHolidaysBuiltin(t *testing.T) {
	days, src := Holidays(2026)
	if src != "builtin" {
		t.Fatalf("2026 应命中内置数据，实际 source=%s", src)
	}
	if len(days) == 0 {
		t.Fatal("2026 内置数据为空")
	}
	// 抽查关键日期：国庆假期首日与调休补班日
	byDate := map[string]Day{}
	for _, d := range days {
		byDate[d.Date] = d
	}
	if d := byDate["2026-10-01"]; !d.IsOffDay || d.Name != "国庆节" {
		t.Errorf("2026-10-01 应为国庆节放假，实际 %+v", d)
	}
	if d := byDate["2026-10-10"]; d.IsOffDay {
		t.Errorf("2026-10-10 应为调休补班（isOffDay=false），实际 %+v", d)
	}
	if d := byDate["2026-02-17"]; !d.IsOffDay || d.Name != "春节" {
		t.Errorf("2026-02-17 应为春节放假，实际 %+v", d)
	}
}

func TestHolidaysUnknownYearFallsBackToRemote(t *testing.T) {
	// 1999 无内置数据 → 走远程同步（注入桩：返回调休两条）
	orig := remoteFetch
	calls := 0
	remoteFetch = func(year int) ([]Day, string) {
		calls++
		return []Day{{Name: "测试节", Date: "1999-05-01", IsOffDay: true}}, "remote"
	}
	t.Cleanup(func() {
		remoteFetch = orig
		resetCache()
	})
	resetCache()

	days, src := Holidays(1999)
	if src != "remote" || len(days) != 1 || days[0].Name != "测试节" {
		t.Fatalf("1999 应走远程同步，实际 source=%s days=%+v", src, days)
	}
	// 第二次命中缓存，不重复抓取
	_, _ = Holidays(1999)
	if calls != 1 {
		t.Errorf("第二次查询应命中缓存，实际远程抓取 %d 次", calls)
	}
}

func TestHolidaysEmptyRemote(t *testing.T) {
	orig := remoteFetch
	remoteFetch = func(year int) ([]Day, string) { return []Day{}, "none" }
	t.Cleanup(func() {
		remoteFetch = orig
		resetCache()
	})
	resetCache()

	days, src := Holidays(2027) // 未发布年份：空数组而非 null
	if src != "none" || days == nil || len(days) != 0 {
		t.Fatalf("未发布年份应返回空数组 none，实际 source=%s days=%+v", src, days)
	}
}

func TestHolidaysHandler(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/calendar/holidays?year=2025", nil)
	HolidaysHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d", w.Code)
	}
	var out struct {
		Year   int   `json:"year"`
		Source string `json:"source"`
		Days   []Day `json:"days"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}
	if out.Year != 2025 || out.Source != "builtin" || len(out.Days) == 0 {
		t.Errorf("响应内容异常：year=%d source=%s days=%d", out.Year, out.Source, len(out.Days))
	}
}

func resetCache() {
	mu.Lock()
	cache = map[int]cacheEntry{}
	mu.Unlock()
}
