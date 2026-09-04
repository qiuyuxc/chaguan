// 管理后台:概览 / 用户管理 / 内容管理(仅管理员)。
package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

const adminUsersPerPage = 15

// requireAdmin 管理后台守卫:未登录跳登录,非管理员返回 403。
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *db.User {
	u := s.currentUser(w, r)
	if u == nil {
		return nil
	}
	if !u.IsAdmin() {
		http.Error(w, "仅管理员可访问后台", http.StatusForbidden)
		return nil
	}
	return u
}

// redirectAfter 后台表单操作完成后优先跳回表单里的 next(仅允许站内相对路径),
// 没有 next 时回退到常规目标页,保持资料页等旧入口行为不变。
func (s *Server) redirectAfter(w http.ResponseWriter, r *http.Request, fallback string) {
	next := strings.TrimSpace(r.FormValue("next"))
	if next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") &&
		!strings.ContainsAny(next, "\r\n") {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fallback, http.StatusSeeOther)
}

// adminPageURL 拼接带 q / page 的后台列表页 URL(跳回用,与分页链接一致)。
func adminPageURL(baseURL, q string, page int) string {
	var b strings.Builder
	b.WriteString(baseURL)
	sep := "?"
	if q != "" {
		b.WriteString(sep)
		b.WriteString("q=")
		b.WriteString(url.QueryEscape(q))
		sep = "&"
	}
	if page > 1 {
		b.WriteString(sep)
		b.WriteString("page=")
		b.WriteString(strconv.Itoa(page))
	}
	return b.String()
}

// ---------- 概览 ----------

type adminOverviewData struct {
	web.Base
	ATab    string
	Stats   db.AdminStats
	Recent  []db.User
	Threads []db.Thread
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	stats, err := s.store.AdminStats(dayStart)
	if err != nil {
		s.serverError(w, err)
		return
	}
	recent, err := s.store.RecentUsers(6)
	if err != nil {
		s.serverError(w, err)
		return
	}
	threads, err := s.store.ListFeedThreads("", false, 8, 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.RenderAdmin(w, 200, "admin_overview", adminOverviewData{
		Base:    s.base(r, "管理后台"),
		ATab:    "overview",
		Stats:   stats,
		Recent:  recent,
		Threads: threads,
	})
}

// ---------- 用户管理 ----------

type adminUsersData struct {
	web.Base
	ATab      string
	ViewerID  int64
	Users     []db.AdminUserRow
	Q         string
	Count     int64
	Page      int
	Pages     int
	BaseURL   string
	HasQ      bool
	Next      string
	PanelHref string // 非空时页面加载后自动打开该用户的管理弹窗
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	user := s.requireAdmin(w, r)
	if user == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(q); len(runes) > 60 {
		q = string(runes[:60])
	}
	page := pageParam(r)
	total, err := s.store.CountAdminUsers(q)
	if err != nil {
		s.serverError(w, err)
		return
	}
	users, err := s.store.ListAdminUsers(q, adminUsersPerPage, (page-1)*adminUsersPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	hasQ := q != ""
	baseURL := "/admin/users"
	if hasQ {
		baseURL += "?q=" + url.QueryEscape(q)
	}
	next := adminPageURL("/admin/users", q, page)

	// 操作回跳带 panel:页面加载后自动重新打开对应用户弹窗,保持管理上下文
	panelHref := ""
	if pid := r.URL.Query().Get("panel"); pid != "" {
		if id, err := strconv.ParseInt(pid, 10, 64); err == nil && id > 0 {
			if t, err := s.store.GetUserByID(id); err == nil && t != nil {
				panelHref = adminPanelHref(id, q, page)
			}
		}
	}
	s.rend.RenderAdmin(w, 200, "admin_users", adminUsersData{
		Base:      s.base(r, "用户管理"),
		ATab:      "users",
		ViewerID:  user.ID,
		Users:     users,
		Q:         q,
		Count:     total,
		Page:      page,
		Pages:     totalPages(total, adminUsersPerPage),
		BaseURL:   baseURL,
		HasQ:      hasQ,
		Next:      next,
		PanelHref: panelHref,
	})
}

// adminPanelHref 弹窗面板的抓取地址,携带 q / page,便于面板里操作后回跳保持列表状态。
func adminPanelHref(id int64, q string, page int) string {
	u := "/admin/users/" + strconv.FormatInt(id, 10) + "/panel"
	sep := "?"
	if q != "" {
		u += sep + "q=" + url.QueryEscape(q)
		sep = "&"
	}
	if page > 1 {
		u += sep + "page=" + strconv.Itoa(page)
	}
	return u
}

// usersReturnURL 列表回跳地址;带 panel 时页面加载后自动重开该用户弹窗。
func usersReturnURL(id int64, q string, page int) string {
	u := adminPageURL("/admin/users", q, page)
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "panel=" + strconv.FormatInt(id, 10)
}

// ---------- 用户管理弹窗(列表内直接操作,不进独立页面) ----------

type adminPanelCat struct {
	ID    int64
	Name  string
	Owned bool // 该用户已是此版块版主
}

type adminUserPanelData struct {
	CSRF     string
	ViewerID int64
	Target   *db.User
	ModCats  []db.Category
	Cats     []adminPanelCat
	Threads  int64
	Replies  int64
	Likes    int64
	Next     string
}

// adminUserPanel GET /admin/users/{id}/panel:返回用户管理弹窗片段。
func (s *Server) adminUserPanel(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	target, err := s.store.GetUserByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	threads, err := s.store.CountUserThreads(target.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	replies, err := s.store.CountUserReplies(target.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	stats, err := s.store.SocialStats(target.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	modCats, err := s.store.ModCategories(target.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, err := s.store.ListCategories()
	if err != nil {
		s.serverError(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(q); len(runes) > 60 {
		q = string(runes[:60])
	}
	owned := make(map[int64]bool, len(modCats))
	for _, c := range modCats {
		owned[c.ID] = true
	}
	opts := make([]adminPanelCat, 0, len(cats))
	for _, c := range cats {
		opts = append(opts, adminPanelCat{ID: c.ID, Name: c.Name, Owned: owned[c.ID]})
	}
	info := auth.From(r.Context())
	viewerID := int64(0)
	if info.User != nil {
		viewerID = info.User.ID
	}
	s.rend.Partial(w, 200, "admin_users", "admin_user_panel", adminUserPanelData{
		CSRF:     info.CSRF,
		ViewerID: viewerID,
		Target:   target,
		ModCats:  modCats,
		Cats:     opts,
		Threads:  threads,
		Replies:  replies,
		Likes:    stats.Liked,
		Next:     usersReturnURL(id, q, pageParam(r)),
	})
}

// ---------- 内容管理 ----------

type adminCatFilter struct {
	ID   int64
	Name string
	Href string
	On   bool
}

type adminThreadsData struct {
	web.Base
	ATab    string
	Threads []db.Thread
	Q       string
	Cat     int64
	Cats    []adminCatFilter
	Count   int64
	Page    int
	Pages   int
	BaseURL string
	HasQ    bool
	Next    string
}

// adminThreadsFilterHref 内容管理筛选链接(保留关键词,切换版块)。
func adminThreadsFilterHref(q string, catID int64) string {
	u := "/admin/threads"
	sep := "?"
	if q != "" {
		u += sep + "q=" + url.QueryEscape(q)
		sep = "&"
	}
	if catID > 0 {
		u += sep + "c=" + strconv.FormatInt(catID, 10)
	}
	return u
}

func (s *Server) adminThreads(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(q); len(runes) > 60 {
		q = string(runes[:60])
	}
	var catID int64
	if c := r.URL.Query().Get("c"); c != "" {
		catID, _ = strconv.ParseInt(c, 10, 64)
	}
	page := pageParam(r)
	total, err := s.store.CountAdminThreads(q, catID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	threads, err := s.store.ListAdminThreads(q, catID, threadsPerPage, (page-1)*threadsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, err := s.store.ListCategories()
	if err != nil {
		s.serverError(w, err)
		return
	}
	filters := make([]adminCatFilter, 0, len(cats)+1)
	filters = append(filters, adminCatFilter{ID: 0, Name: "全部", Href: adminThreadsFilterHref(q, 0), On: catID == 0})
	for _, c := range cats {
		filters = append(filters, adminCatFilter{ID: c.ID, Name: c.Name, Href: adminThreadsFilterHref(q, c.ID), On: catID == c.ID})
	}
	s.rend.RenderAdmin(w, 200, "admin_threads", adminThreadsData{
		Base:    s.base(r, "内容管理"),
		ATab:    "threads",
		Threads: threads,
		Q:       q,
		Cat:     catID,
		Cats:    filters,
		Count:   total,
		Page:    page,
		Pages:   totalPages(total, threadsPerPage),
		BaseURL: adminThreadsFilterHref(q, catID),
		HasQ:    q != "" || catID > 0,
		Next:    adminPageURL(adminThreadsFilterHref(q, catID), "", page),
	})
}
