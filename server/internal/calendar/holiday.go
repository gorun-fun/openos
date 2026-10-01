// calendar 包：中国法定节假日（含调休安排）查询服务，供日历应用与小组件消费。
//
// 数据分层：
//  1. 内置数据 —— 已发布的国务院办公厅放假安排（内置 2025/2026，来源
//     NateScarlet/holiday-cn 项目，依据政府公文整理，与官方一致且年内不再变化）；
//  2. 在线同步 —— 其余年份按需抓取（holiday-cn → timor.tech 双源回退），
//     OPENOS_HOLIDAY_SYNC=off 可关闭（纯内网部署）；
//  3. 内存缓存 —— 每年结果缓存 24h；尚未发布的年份（空结果）缓存 1h，
//     以便安排公布后较快生效。
//
// 接口：GET /api/calendar/holidays?year=2026（需登录）
// 返回：{"year":2026,"source":"builtin|remote|none","days":[{name,date,isOffDay}...]}
package calendar

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// Day 一天的节假日属性：isOffDay=true 为放假，false 为调休上班（补班）。
type Day struct {
	Name     string `json:"name"`
	Date     string `json:"date"`
	IsOffDay bool   `json:"isOffDay"`
}

// builtin 内置官方数据（国务院办公厅当年放假安排；来源 holiday-cn 项目）。
var builtin = map[int][]Day{
	2025: {
		{Name: "元旦", Date: "2025-01-01", IsOffDay: true},
		{Name: "春节", Date: "2025-01-26", IsOffDay: false},
		{Name: "春节", Date: "2025-01-28", IsOffDay: true},
		{Name: "春节", Date: "2025-01-29", IsOffDay: true},
		{Name: "春节", Date: "2025-01-30", IsOffDay: true},
		{Name: "春节", Date: "2025-01-31", IsOffDay: true},
		{Name: "春节", Date: "2025-02-01", IsOffDay: true},
		{Name: "春节", Date: "2025-02-02", IsOffDay: true},
		{Name: "春节", Date: "2025-02-03", IsOffDay: true},
		{Name: "春节", Date: "2025-02-04", IsOffDay: true},
		{Name: "春节", Date: "2025-02-08", IsOffDay: false},
		{Name: "清明节", Date: "2025-04-04", IsOffDay: true},
		{Name: "清明节", Date: "2025-04-05", IsOffDay: true},
		{Name: "清明节", Date: "2025-04-06", IsOffDay: true},
		{Name: "劳动节", Date: "2025-04-27", IsOffDay: false},
		{Name: "劳动节", Date: "2025-05-01", IsOffDay: true},
		{Name: "劳动节", Date: "2025-05-02", IsOffDay: true},
		{Name: "劳动节", Date: "2025-05-03", IsOffDay: true},
		{Name: "劳动节", Date: "2025-05-04", IsOffDay: true},
		{Name: "劳动节", Date: "2025-05-05", IsOffDay: true},
		{Name: "端午节", Date: "2025-05-31", IsOffDay: true},
		{Name: "端午节", Date: "2025-06-01", IsOffDay: true},
		{Name: "端午节", Date: "2025-06-02", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-09-28", IsOffDay: false},
		{Name: "国庆节、中秋节", Date: "2025-10-01", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-02", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-03", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-04", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-05", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-06", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-07", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-08", IsOffDay: true},
		{Name: "国庆节、中秋节", Date: "2025-10-11", IsOffDay: false},
	},
	2026: {
		{Name: "元旦", Date: "2026-01-01", IsOffDay: true},
		{Name: "元旦", Date: "2026-01-02", IsOffDay: true},
		{Name: "元旦", Date: "2026-01-03", IsOffDay: true},
		{Name: "元旦", Date: "2026-01-04", IsOffDay: false},
		{Name: "春节", Date: "2026-02-14", IsOffDay: false},
		{Name: "春节", Date: "2026-02-15", IsOffDay: true},
		{Name: "春节", Date: "2026-02-16", IsOffDay: true},
		{Name: "春节", Date: "2026-02-17", IsOffDay: true},
		{Name: "春节", Date: "2026-02-18", IsOffDay: true},
		{Name: "春节", Date: "2026-02-19", IsOffDay: true},
		{Name: "春节", Date: "2026-02-20", IsOffDay: true},
		{Name: "春节", Date: "2026-02-21", IsOffDay: true},
		{Name: "春节", Date: "2026-02-22", IsOffDay: true},
		{Name: "春节", Date: "2026-02-23", IsOffDay: true},
		{Name: "春节", Date: "2026-02-28", IsOffDay: false},
		{Name: "清明节", Date: "2026-04-04", IsOffDay: true},
		{Name: "清明节", Date: "2026-04-05", IsOffDay: true},
		{Name: "清明节", Date: "2026-04-06", IsOffDay: true},
		{Name: "劳动节", Date: "2026-05-01", IsOffDay: true},
		{Name: "劳动节", Date: "2026-05-02", IsOffDay: true},
		{Name: "劳动节", Date: "2026-05-03", IsOffDay: true},
		{Name: "劳动节", Date: "2026-05-04", IsOffDay: true},
		{Name: "劳动节", Date: "2026-05-05", IsOffDay: true},
		{Name: "劳动节", Date: "2026-05-09", IsOffDay: false},
		{Name: "端午节", Date: "2026-06-19", IsOffDay: true},
		{Name: "端午节", Date: "2026-06-20", IsOffDay: true},
		{Name: "端午节", Date: "2026-06-21", IsOffDay: true},
		{Name: "国庆节", Date: "2026-09-20", IsOffDay: false},
		{Name: "中秋节", Date: "2026-09-25", IsOffDay: true},
		{Name: "中秋节", Date: "2026-09-26", IsOffDay: true},
		{Name: "中秋节", Date: "2026-09-27", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-01", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-02", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-03", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-04", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-05", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-06", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-07", IsOffDay: true},
		{Name: "国庆节", Date: "2026-10-10", IsOffDay: false},
	},
}

type cacheEntry struct {
	days   []Day
	source string
	at     time.Time
}

var (
	mu    sync.Mutex
	cache = map[int]cacheEntry{}
)

// client 在线抓取客户端（短超时，避免拖慢接口）。
var client = &http.Client{Timeout: 5 * time.Second}

// remoteFetch 可替换的远程同步实现（测试注入用）。
var remoteFetch = fetchRemote

// Holidays 返回某年中国节假日安排与数据来源。
// 内置年份直接返回（官方安排年内不再变化）；其余年份在线同步（带缓存）。
func Holidays(year int) ([]Day, string) {
	if days, ok := builtin[year]; ok {
		return sortedCopy(days), "builtin"
	}
	if os.Getenv("OPENOS_HOLIDAY_SYNC") == "off" {
		return []Day{}, "none"
	}
	mu.Lock()
	if e, ok := cache[year]; ok {
		// 空结果（年份未发布）缓存 1h，正常结果缓存 24h
		ttl := 24 * time.Hour
		if len(e.days) == 0 {
			ttl = time.Hour
		}
		if time.Since(e.at) < ttl {
			days, src := e.days, e.source
			mu.Unlock()
			return days, src
		}
	}
	mu.Unlock()

	days, src := remoteFetch(year)

	mu.Lock()
	cache[year] = cacheEntry{days: days, source: src, at: time.Now()}
	mu.Unlock()
	return days, src
}

// fetchRemote 双源抓取：holiday-cn（GitHub）→ timor.tech，任一成功即返回。
func fetchRemote(year int) ([]Day, string) {
	if days, err := fetchHolidayCN(year); err == nil && len(days) > 0 {
		return days, "remote"
	}
	if days, err := fetchTimor(year); err == nil && len(days) > 0 {
		return days, "remote"
	}
	return []Day{}, "none"
}

// fetchHolidayCN 抓 https://github.com/NateScarlet/holiday-cn（按政府公文整理的
// 年度 JSON：{year, days:[{name,date,isOffDay}]}）。
func fetchHolidayCN(year int) ([]Day, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/NateScarlet/holiday-cn/master/%d.json", year)
	body, err := getJSON(url)
	if err != nil {
		return nil, err
	}
	var data struct {
		Year int   `json:"year"`
		Days []Day `json:"days"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if data.Year != year {
		return nil, fmt.Errorf("holiday-cn 年份不匹配：%d", data.Year)
	}
	for _, d := range data.Days {
		if d.Date == "" || d.Name == "" {
			return nil, fmt.Errorf("holiday-cn 数据不完整")
		}
	}
	return data.Days, nil
}

// fetchTimor 抓 https://timor.tech/api/holiday/year/{y}（国内可达性更好的
// 节假日 API：{code:0, holiday:{"MM-DD":{holiday,name,date,...}}}，holiday
// 布尔即是否放假）。
func fetchTimor(year int) ([]Day, error) {
	body, err := getJSON(fmt.Sprintf("https://timor.tech/api/holiday/year/%d", year))
	if err != nil {
		return nil, err
	}
	var data struct {
		Code int `json:"code"`
		Holiday map[string]struct {
			Name     string `json:"name"`
			Date     string `json:"date"`
			Holiday  bool   `json:"holiday"`
		} `json:"holiday"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if data.Code != 0 || len(data.Holiday) == 0 {
		return nil, fmt.Errorf("timor.tech 返回异常：code=%d", data.Code)
	}
	days := make([]Day, 0, len(data.Holiday))
	for _, h := range data.Holiday {
		if h.Date == "" {
			continue
		}
		days = append(days, Day{Name: h.Name, Date: h.Date, IsOffDay: h.Holiday})
	}
	return days, nil
}

func getJSON(url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s：%s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func sortedCopy(days []Day) []Day {
	out := make([]Day, len(days))
	copy(out, days)
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// HolidaysHandler HTTP 处理器：GET /api/calendar/holidays?year=2026（缺省当年）。
// days 恒为数组（无数据返回空数组，年份未发布属正常情况）。
func HolidaysHandler(w http.ResponseWriter, r *http.Request) {
	year := time.Now().Year()
	if v := r.URL.Query().Get("year"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			year = n
		}
	}
	if year < 1900 || year > 2200 {
		http.Error(w, "年份超出范围", http.StatusBadRequest)
		return
	}
	days, source := Holidays(year)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"year":   year,
		"source": source,
		"days":   days,
	})
}
