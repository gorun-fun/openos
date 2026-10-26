// Cookie 登记簿与清除端点（「清除缓存」的服务端配套）。
// 浏览器 document.cookie 枚举不了不可见路径上的 Cookie：登录流经多目标站
//（epassport/e.meituan.com 等），票据落在 /app/<id>/https/<其他域>/… 深路径，
// 业务页 iframe 看不到也删不掉——美团「清了缓存还登录」的根因。响应头写
// Cookie 不受当前文档路径限制，端点据此整批下发过期头，一次清光任意路径。
// 两个登记来源：①响应侧 rewriteSetCookie 的精确最终路径；②请求侧——每个
// 代理请求携带的 Cookie 名 × 请求路径的逐级目录前缀（Cookie 的 Path 必是
// 请求路径的某级前缀，候选写偏是无害空操作；这一侧让升级部署前就已存在的
// 旧票据在被携带一次后即入册）。页面 JS 写的 Cookie 与 IndexedDB 不经服务端，
// 由 hook.js 在应用命名空间登记（见 hook.js pxLog）。
package webproxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"openos/server/internal/store"
)

// pxClearPath 清除端点的固定子路径（挂在 /app/<id>/ 下；解码不出合法目标，
// 在该端点之前此类路径恒 400，站点页面不可能与之冲突）。
const pxClearPath = "__os_px_clear__"

// cookieRegKV 登记簿在 system_kv 的持久化键（openos 重启不丢登记）。
const cookieRegKV = "webproxy_cookie_reg"

// cookieRegMaxPerApp 单应用登记上限（名字×路径去重后实际远小于此，防无界增长）。
const cookieRegMaxPerApp = 4000

// cookieReg 服务端 Cookie 登记簿：appID → "name\tpath" 集合。
type cookieReg struct {
	mu    sync.Mutex
	db    *store.DB
	m     map[string]map[string]struct{}
	timer *time.Timer
}

func newCookieReg(db *store.DB) *cookieReg {
	r := &cookieReg{db: db, m: map[string]map[string]struct{}{}}
	if db == nil { // 单测直调 New(nil)：登记簿仅内存态
		return r
	}
	v, err := db.GetSystemKV(cookieRegKV)
	if err != nil || v == "" {
		return r
	}
	var persisted map[string][]string
	if json.Unmarshal([]byte(v), &persisted) == nil {
		for app, list := range persisted {
			for _, e := range list {
				r.add(app, e)
			}
		}
	}
	return r
}

// add 登记一条（须持锁或单线程构造期调用）。
func (r *cookieReg) add(app, entry string) {
	if app == "" || entry == "" {
		return
	}
	set := r.m[app]
	if set == nil {
		set = map[string]struct{}{}
		r.m[app] = set
	}
	if len(set) >= cookieRegMaxPerApp {
		return
	}
	set[entry] = struct{}{}
}

// record 登记一条 name\tpath，防抖持久化。
func (r *cookieReg) record(app, entry string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(app, entry)
	if r.timer == nil {
		r.timer = time.AfterFunc(3*time.Second, r.flush)
	}
}

// Close 停机兜底：防抖窗口（≤3s）内尚未落库的登记立即落盘（优雅停机时经
// Proxy.Close 调用）。停机路径上不再有并发写入；正常运行期误调也无害——
// 快照落库幂等，后续登记照常走防抖。
func (r *cookieReg) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()
	r.flush()
}

// flush 快照落库（防抖到期调用）。
func (r *cookieReg) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timer = nil
	if r.db == nil {
		return
	}
	out := make(map[string][]string, len(r.m))
	for app, set := range r.m {
		list := make([]string, 0, len(set))
		for e := range set {
			list = append(list, e)
		}
		out[app] = list
	}
	b, err := json.Marshal(out)
	if err == nil {
		_ = r.db.SetSystemKV(cookieRegKV, string(b))
	}
}

// recordRequestCookies 请求侧登记：请求携带的 Cookie 名 × 请求路径的逐级目录
// 前缀（含 /app/<id>/ 应用根；Path=/ 的宿主自身 Cookie 不在清扫面，不记）。
// 宿主会话 Cookie（openos_token/openos_*）随每个代理请求携带，须排除——
// 它们不是站点数据，登记只会污染清除端点的下发面。
func (r *cookieReg) recordRequestCookies(app string, req *http.Request) {
	if r == nil || app == "" {
		return
	}
	base := "/app/" + app + "/"
	p := req.URL.Path
	if !strings.HasPrefix(p, base) {
		return
	}
	var cookies []*http.Cookie
	for _, c := range req.Cookies() {
		if c.Name == "openos_token" || strings.HasPrefix(c.Name, "openos_") || c.Name == "hotel_refresh" {
			continue
		}
		cookies = append(cookies, c)
	}
	if len(cookies) == 0 {
		return
	}
	var dirs []string
	acc := base
	dirs = append(dirs, acc)
	for _, seg := range strings.Split(strings.TrimPrefix(p, base), "/") {
		if seg == "" {
			continue
		}
		acc += seg + "/"
		dirs = append(dirs, acc)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range cookies {
		for _, d := range dirs {
			r.add(app, c.Name+"\t"+d)
		}
	}
}

// expireBatch 对指定应用登记在册的 Cookie 分批下发过期头（路径精确命中即删除），
// **发过即从登记簿移除**（站点再种会重新入册）——remaining 随批单调递减，调用方
// 循环取批到 0 收敛。登记条目可达数千（子资源响应按 default-path 落 Cookie，
// 同名多路径），单响应全量下发会超浏览器响应头消化能力被静默丢弃。批量默认 60：
// dev 形态经 vite 代理（Node http 解析器 16KB 响应头上限，实测 ~150 条即 502），
// 生产直连可放宽但保持两侧一致。返回（本批条数, 剩余条数）。
func (r *cookieReg) expireBatch(w http.ResponseWriter, app string, limit int) (int, int) {
	if r == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 60
	}
	if limit > 100 {
		limit = 100
	}
	r.mu.Lock()
	list := make([]string, 0, len(r.m[app]))
	for e := range r.m[app] {
		list = append(list, e)
	}
	sort.Strings(list)
	if len(list) > limit {
		list = list[:limit]
	}
	for _, e := range list {
		delete(r.m[app], e)
	}
	if r.timer == nil {
		r.timer = time.AfterFunc(3*time.Second, r.flush)
	}
	remain := len(r.m[app])
	r.mu.Unlock()
	emitted := 0
	for _, e := range list {
		name, path, ok := strings.Cut(e, "\t")
		if !ok || name == "" || !strings.HasPrefix(path, "/") {
			continue
		}
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: path,
			MaxAge: -1, Expires: time.Unix(0, 0),
		})
		emitted++
	}
	return emitted, remain
}
