// 管理后台:概览 / 用户管理 / 内容管理(仅管理员)。
package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"chaguan/internal/auth"
	"chaguan/internal/db"
	"chaguan/web"
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

// safeNextPath next 只认本站相对路径:`//host` 是协议相对地址,
// `/\host` 的反斜杠会被浏览器归一成 `/`,都能跳到外站。
func safeNextPath(next string) bool {
	if next == "" || next[0] != '/' || strings.ContainsAny(next, "\r\n") {
		return false
	}
	return len(next) == 1 || (next[1] != '/' && next[1] != '\\')
}

// redirectAfter 表单操作完成后优先跳回 next,没有则回 fallback。
func (s *Server) redirectAfter(w http.ResponseWriter, r *http.Request, fallback string) {
	if next := strings.TrimSpace(r.FormValue("next")); safeNextPath(next) {
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

// ---------- 站点设置 ----------

const (
	maxSiteName    = 30
	maxSiteTagline = 60
	maxSiteFooter  = 120
	maxAnnounce    = 200
)

type adminSiteData struct {
	web.Base
	ATab  string
	Error string
	Saved bool
	// 校验失败时回填用户刚填的内容(而不是库里的旧值)
	Form db.Site
}

func (s *Server) adminSitePage(w http.ResponseWriter, r *http.Request, form db.Site, errMsg string, saved bool) {
	s.rend.RenderAdmin(w, 200, "admin_site", adminSiteData{
		Base:  s.base(r, "站点设置"),
		ATab:  "site",
		Error: errMsg,
		Saved: saved,
		Form:  form,
	})
}

func (s *Server) adminSite(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	site, err := s.store.Site()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.adminSitePage(w, r, site, "", r.URL.Query().Get("ok") == "1")
}

// adminSaveSite POST /admin/site:保存品牌信息 / 页脚 / 公告(图标走单独的上传入口)。
func (s *Server) adminSaveSite(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	site, err := s.store.Site()
	if err != nil {
		s.serverError(w, err)
		return
	}
	form := db.Site{
		Name:         strings.TrimSpace(r.FormValue("name")),
		Tagline:      strings.TrimSpace(r.FormValue("tagline")),
		Footer:       strings.TrimSpace(r.FormValue("footer")),
		Announcement: strings.TrimSpace(r.FormValue("announcement")),
		IconPath:     site.IconPath, // 图标不在本表单里,保持原值以便回填预览
	}
	switch {
	case utf8.RuneCountInString(form.Name) < 1 || utf8.RuneCountInString(form.Name) > maxSiteName:
		s.adminSitePage(w, r, form, "站点名称 1–30 字", false)
		return
	case utf8.RuneCountInString(form.Tagline) > maxSiteTagline:
		s.adminSitePage(w, r, form, "品牌副标题最多 60 字", false)
		return
	case utf8.RuneCountInString(form.Footer) > maxSiteFooter:
		s.adminSitePage(w, r, form, "页脚文案最多 120 字", false)
		return
	case utf8.RuneCountInString(form.Announcement) > maxAnnounce:
		s.adminSitePage(w, r, form, "站点公告最多 200 字", false)
		return
	}
	if err := s.store.SetSiteInfo(form.Name, form.Tagline, form.Footer, form.Announcement); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/site?ok=1", http.StatusSeeOther)
}

// adminSiteIcon POST /admin/site/icon:换站点图标(multipart 字段 icon),
// 带 clear=1 表示恢复内置图标。旧图标文件随之清理。
func (s *Server) adminSiteIcon(w http.ResponseWriter, r *http.Request) {
	user := s.requireAdmin(w, r)
	if user == nil {
		return
	}
	site, err := s.store.Site()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if r.FormValue("clear") == "1" {
		if err := s.store.SetSiteIcon(""); err != nil {
			s.serverError(w, err)
			return
		}
		if oldID, ok := uploadPathID(site.IconPath); ok {
			s.removeUploadFile(oldID)
		}
		http.Redirect(w, r, "/admin/site?ok=1", http.StatusSeeOther)
		return
	}
	if err := r.ParseMultipartForm(maxImageBytes + 1<<20); err != nil {
		http.Error(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if len(r.MultipartForm.File["icon"]) == 0 {
		s.adminSitePage(w, r, site, "请选择图片", false)
		return
	}
	uploadID, ok := s.saveImageUpload(w, r, "icon", user.ID)
	if !ok {
		return
	}
	newPath := "/uploads/" + strconv.FormatInt(uploadID, 10)
	if err := s.store.SetSiteIcon(newPath); err != nil {
		s.serverError(w, err)
		return
	}
	if oldID, ok := uploadPathID(site.IconPath); ok {
		s.removeUploadFile(oldID) // 清理旧图标,失败静默
	}
	http.Redirect(w, r, "/admin/site?ok=1", http.StatusSeeOther)
}

// ---------- 邮件设置(SMTP + 邮件注册开关) ----------

type adminMailData struct {
	web.Base
	ATab    string
	Sec     db.Security
	HasPass bool // 已保存过口令:表单不回显,留空即不修改
	Error   string
	Saved   bool
	TestTo  string
	TestOK  bool
	TestErr string
}

func (s *Server) adminMailPage(w http.ResponseWriter, r *http.Request, d adminMailData) {
	sec, err := s.store.Security()
	if err != nil {
		s.serverError(w, err)
		return
	}
	d.Base = s.base(r, "邮件设置")
	d.ATab = "mail"
	d.Sec = sec
	d.HasPass = sec.SMTPPass != ""
	s.rend.RenderAdmin(w, 200, "admin_mail", d)
}

func (s *Server) adminMail(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.adminMailPage(w, r, adminMailData{Saved: r.URL.Query().Get("ok") == "1"})
}

// adminSaveMail POST /admin/mail:保存 SMTP 与邮件注册开关。
// 口令留空表示不修改;clear_pass=1 表示清空已保存的口令。
func (s *Server) adminSaveMail(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	host := strings.TrimSpace(r.FormValue("host"))
	port := strings.TrimSpace(r.FormValue("port"))
	user := strings.TrimSpace(r.FormValue("user"))
	pass := r.FormValue("pass")
	from := strings.TrimSpace(r.FormValue("from"))
	secure := strings.TrimSpace(r.FormValue("secure"))
	emailReg := r.FormValue("email_register") == "1"

	switch secure {
	case "starttls", "ssl", "none":
	default:
		secure = "starttls"
	}
	if p, err := strconv.Atoi(port); port != "" && (err != nil || p < 1 || p > 65535) {
		s.adminMailPage(w, r, adminMailData{Error: "端口需在 1–65535 之间(常见 587 或 465)"})
		return
	}
	if from != "" && !validEmail(from) {
		s.adminMailPage(w, r, adminMailData{Error: "发件人需要是有效邮箱地址"})
		return
	}
	if utf8.RuneCountInString(host) > 120 || utf8.RuneCountInString(user) > 120 {
		s.adminMailPage(w, r, adminMailData{Error: "服务器或账号过长(上限 120 字)"})
		return
	}
	ready := host != "" && port != "" && from != ""
	if emailReg && !ready {
		s.adminMailPage(w, r, adminMailData{
			Error: "开启邮件注册前,先把服务器、端口、发件人填完整,否则没人能注册",
		})
		return
	}
	if r.FormValue("clear_pass") == "1" {
		if err := s.store.ClearMailPass(); err != nil {
			s.serverError(w, err)
			return
		}
		pass = ""
	}
	if err := s.store.SetMailSettings(host, port, user, pass, from, secure, emailReg); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/mail?ok=1", http.StatusSeeOther)
}

// adminTestMail POST /admin/mail/test:按当前保存的配置发一封测试信,错误直接显示。
func (s *Server) adminTestMail(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	to := strings.TrimSpace(r.FormValue("to"))
	if !validEmail(to) {
		s.adminMailPage(w, r, adminMailData{TestTo: to, TestErr: "请填写有效的收件邮箱"})
		return
	}
	sec, err := s.store.Security()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !sec.MailReady() {
		s.adminMailPage(w, r, adminMailData{TestTo: to, TestErr: "请先填好服务器、端口与发件人并保存"})
		return
	}
	site := s.siteName()
	body := "这是一封来自 " + site + " 的测试邮件。\n\n收到它说明 SMTP 配置可用,注册验证与找回密码可以正常发信了。"
	if err := s.sendMail(sec, to, "["+site+"] SMTP 测试邮件", body); err != nil {
		s.adminMailPage(w, r, adminMailData{TestTo: to, TestErr: err.Error()})
		return
	}
	s.adminMailPage(w, r, adminMailData{TestTo: to, TestOK: true})
}

// ---------- 安全设置(Cloudflare Turnstile 人机验证) ----------

type adminSecurityData struct {
	web.Base
	ATab      string
	Sec       db.Security
	HasSecret bool
	Error     string
	Saved     bool
}

func (s *Server) adminSecurityPage(w http.ResponseWriter, r *http.Request, d adminSecurityData) {
	sec, err := s.store.Security()
	if err != nil {
		s.serverError(w, err)
		return
	}
	d.Base = s.base(r, "安全设置")
	d.ATab = "security"
	d.Sec = sec
	d.HasSecret = sec.TurnstileSecret != ""
	s.rend.RenderAdmin(w, 200, "admin_security", d)
}

func (s *Server) adminSecurity(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.adminSecurityPage(w, r, adminSecurityData{Saved: r.URL.Query().Get("ok") == "1"})
}

// adminSaveSecurity POST /admin/security:保存 Turnstile 站点密钥与开关。
// 密钥留空表示不修改;clear_secret=1 表示清空。
func (s *Server) adminSaveSecurity(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	site := strings.TrimSpace(r.FormValue("site_key"))
	secret := strings.TrimSpace(r.FormValue("secret_key"))
	on := r.FormValue("turnstile_on") == "1"
	if utf8.RuneCountInString(site) > 200 || utf8.RuneCountInString(secret) > 200 {
		s.adminSecurityPage(w, r, adminSecurityData{Error: "密钥过长(上限 200 字)"})
		return
	}
	cur, err := s.store.Security()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if r.FormValue("clear_secret") == "1" {
		if err := s.store.ClearCaptchaSecret(); err != nil {
			s.serverError(w, err)
			return
		}
		cur.TurnstileSecret, secret = "", ""
	}
	haveSecret := secret != "" || cur.TurnstileSecret != ""
	if on && (site == "" || !haveSecret) {
		s.adminSecurityPage(w, r, adminSecurityData{
			Error: "开启前请把站点密钥(Site Key)和服务端密钥(Secret Key)都填上",
		})
		return
	}
	if err := s.store.SetCaptchaSettings(site, secret, on); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/security?ok=1", http.StatusSeeOther)
}

// ---------- 积分管理 ----------

type adminPointsData struct {
	web.Base
	ATab    string
	Users   []db.AdminUserRow
	Q       string
	Count   int64
	Page    int
	Pages   int
	BaseURL string
	HasQ    bool
	Error   string
	Saved   string // 刚调整完的提示
}

func (s *Server) adminPointsPage(w http.ResponseWriter, r *http.Request, errMsg, saved string) {
	q := strings.TrimSpace(r.FormValue("q"))
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
	baseURL := "/admin/points"
	if q != "" {
		baseURL += "?q=" + url.QueryEscape(q)
	}
	s.rend.RenderAdmin(w, 200, "admin_points", adminPointsData{
		Base:    s.base(r, "积分管理"),
		ATab:    "points",
		Users:   users,
		Q:       q,
		Count:   total,
		Page:    page,
		Pages:   totalPages(total, adminUsersPerPage),
		BaseURL: baseURL,
		HasQ:    q != "",
		Error:   errMsg,
		Saved:   saved,
	})
}

func (s *Server) adminPoints(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.adminPointsPage(w, r, "", "")
}

// adminAdjustPoints POST /admin/points/{id}/adjust:给某人加减积分,必须写备注。
func (s *Server) adminAdjustPoints(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	// 后台调整允许两位小数
	raw := strings.TrimSpace(r.FormValue("delta"))
	neg := strings.HasPrefix(raw, "-")
	delta, err := db.ParsePoints(strings.TrimPrefix(raw, "-"))
	if neg {
		delta = -delta
	}
	if err != nil || delta == 0 || delta < -1000000*db.PointScale || delta > 1000000*db.PointScale {
		s.adminPointsPage(w, r, "积分变动需为非 0 数值(最多两位小数),且不超过 ±1000000", "")
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if utf8.RuneCountInString(note) < 1 || utf8.RuneCountInString(note) > 60 {
		s.adminPointsPage(w, r, "请填写 1–60 字的调整原因(会记进流水)", "")
		return
	}
	err = s.store.AdjustPoints(target.ID, delta, db.PointAdmin, note, 0, 0)
	if err == db.ErrNotEnoughPoints {
		s.adminPointsPage(w, r, target.Name+" 的积分不足,扣减失败", "")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	sign := "+"
	if delta < 0 {
		sign = "" // FormatPoints 自带负号
	}
	s.adminPointsPage(w, r, "", "已给 "+target.Name+" 调整 "+sign+db.FormatPoints(delta)+" 积分")
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
	Badges   []db.Badge // 全部勋章 + 该用户是否持有(后台发放/收回)
	WornID   int64      // 该用户正佩戴的勋章 id
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
	_, wornID := badgeState(target)
	badges, err := s.store.BadgesWithOwnership(target.ID)
	if err != nil {
		s.serverError(w, err)
		return
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
		Badges:   badges,
		WornID:   wornID,
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

// ---------- 增加用户 ----------

type adminUserNewData struct {
	web.Base
	ATab  string
	Error string
	Name  string
	Email string
}

func (s *Server) adminUserNewForm(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.rend.RenderAdmin(w, 200, "admin_user_new", adminUserNewData{
		Base: s.base(r, "增加用户"),
		ATab: "users",
	})
}

func (s *Server) adminUserNew(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	fail := func(msg string) {
		s.rend.RenderAdmin(w, 200, "admin_user_new", adminUserNewData{
			Base:  s.base(r, "增加用户"),
			ATab:  "users",
			Error: msg, Name: name, Email: email,
		})
	}

	switch {
	case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 24:
		fail("用户名需要 2–24 个字符")
		return
	case strings.ContainsAny(name, " \t\r\n@/"):
		fail("用户名不能包含空格、@ 或斜杠")
		return
	case email != "" && !validEmail(email):
		fail("邮箱格式不正确(可留空)")
		return
	case len(password) < 8:
		fail("密码至少 8 位")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	uid, err := s.store.CreateUser(name, email, hash)
	if err != nil {
		if err == db.ErrDuplicateName {
			fail("用户名或邮箱已被占用")
			return
		}
		s.serverError(w, err)
		return
	}
	// 后台建号视同邮箱已核实,否则开着邮件注册时登不进来
	if email != "" {
		if err := s.store.MarkEmailVerified(uid); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}
