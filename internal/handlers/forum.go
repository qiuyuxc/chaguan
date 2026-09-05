package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/internal/markdown"
	"bbs/web"
)

// base 从请求上下文构造页面公共头,顺带加载抽屉/侧栏数据(导航与社区统计)。
func (s *Server) base(r *http.Request, title string) web.Base {
	i := auth.From(r.Context())
	b := web.Base{Title: title, User: i.User, CSRF: i.CSRF}
	if site, err := s.store.Site(); err == nil {
		b.Site = site
	} else {
		b.Site = db.Site{Name: db.SiteDefaultName, Footer: db.SiteDefaultFooter}
	}
	if cats, err := s.store.ListCategories(); err == nil {
		b.Categories = cats
	}
	if n, err := s.store.CountUsers(); err == nil {
		b.Members = n
	}
	if n, err := s.store.CountAllThreads(); err == nil {
		b.TotalThreads = n
	}
	if u := i.User; u != nil {
		b.Points = u.Points
		if done, err := s.store.CheckedIn(u.ID, today()); err == nil {
			b.CheckedIn = done
		}
		if threads, err := s.store.CountUserThreads(u.ID); err == nil {
			if replies, err := s.store.CountUserReplies(u.ID); err == nil {
				// 等级经验只看真实获赞,后台覆盖的展示获赞不参与
				if likedReal, err := s.store.LikesReceived(u.ID); err == nil {
					exp := socialExp(threads, replies, likedReal, u.ExpExtra)
					level, shown, start, next := levelInfo(exp, u.LevelOverride)
					b.Exp = shown
					b.Level = level
					b.ExpNext = next
					b.ExpPct = 100
					if next > start {
						b.ExpPct = int((shown - start) * 100 / (next - start))
						if b.ExpPct > 100 {
							b.ExpPct = 100
						}
					}
				}
			}
		}
	}
	return b
}

// ---------- 搜索 ----------

type searchData struct {
	web.Base
	Query       string
	Threads     []db.Thread
	HasQuery    bool
	NoResult    bool
	Page, Pages int
	BaseURL     string
	HasQ        bool
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(q); len(runes) > 100 {
		q = string(runes[:100])
	}
	page := pageParam(r)
	data := searchData{
		Base:     s.base(r, "搜索"),
		Query:    q,
		HasQuery: q != "",
		Page:     page,
		Pages:    1,
		BaseURL:  "/search",
	}
	if q != "" {
		total, err := s.store.CountSearchThreads(q)
		if err != nil {
			s.serverError(w, err)
			return
		}
		data.Pages = totalPages(total, threadsPerPage)
		if total > 0 {
			threads, err := s.store.SearchThreads(q, threadsPerPage, (page-1)*threadsPerPage)
			if err != nil {
				s.serverError(w, err)
				return
			}
			data.Threads = threads
		} else {
			data.NoResult = true
		}
		data.BaseURL = "/search?q=" + url.QueryEscape(q)
		data.HasQ = true
	}
	s.rend.Render(w, 200, "search", data)
}

// ---------- 首页:帖子流(最新/热帖 + 分类筛选)----------

type homeData struct {
	web.Base
	Threads     []db.Thread
	Categories  []db.Category
	ActiveCat   string
	Hot         bool
	IsEmpty     bool
	Page, Pages int
	BaseURL     string
	HasQ        bool
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	cats, err := s.store.ListCategories()
	if err != nil {
		s.serverError(w, err)
		return
	}
	catSlug := strings.TrimSpace(r.URL.Query().Get("cat"))
	hot := r.URL.Query().Get("tab") == "hot"
	valid := false
	for _, c := range cats {
		if c.Slug == catSlug {
			valid = true
			break
		}
	}
	if !valid {
		catSlug = ""
	}
	page := pageParam(r)
	total, err := s.store.CountFeedThreads(catSlug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	threads, err := s.store.ListFeedThreads(catSlug, hot, threadsPerPage, (page-1)*threadsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var q []string
	if hot {
		q = append(q, "tab=hot")
	}
	if catSlug != "" {
		q = append(q, "cat="+catSlug)
	}
	baseURL := "/"
	if len(q) > 0 {
		baseURL += "?" + strings.Join(q, "&")
	}
	s.rend.Render(w, 200, "home", homeData{
		Base:       s.base(r, "首页"),
		Threads:    threads,
		Categories: cats,
		ActiveCat:  catSlug,
		Hot:        hot,
		IsEmpty:    total == 0,
		Page:       page,
		Pages:      totalPages(total, threadsPerPage),
		BaseURL:    baseURL,
		HasQ:       strings.Contains(baseURL, "?"),
	})
}

// ---------- 版块管理(管理后台,建版块/删空版块)----------

type adminCatsData struct {
	web.Base
	ATab       string
	Categories []db.Category
	Error      string
}

func (s *Server) adminCategories(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	if !user.IsAdmin() {
		http.Error(w, "仅管理员可管理版块", http.StatusForbidden)
		return
	}
	cats, err := s.store.ListCategories()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.RenderAdmin(w, 200, "admin_categories", adminCatsData{
		Base:       s.base(r, "版块管理"),
		ATab:       "categories",
		Categories: cats,
	})
}

// deleteCategory 删除版块。空版块直接删;非空版块必须明确指定处理方式:
//
//	mode=cascade     连同版块内的主题与回复一起删除(政策原因需强删时用)
//	mode=move&to=ID  先把主题整体迁到别的版块,再删掉这个版块
//
// 无论哪种都保留最后一个版块,否则发帖页会没有可选版块。
func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	if !user.IsAdmin() {
		http.Error(w, "仅管理员可删除版块", http.StatusForbidden)
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if total, err := s.store.CountCategories(); err != nil {
		s.serverError(w, err)
		return
	} else if total <= 1 {
		http.Error(w, "至少要保留一个版块", http.StatusBadRequest)
		return
	}
	n, err := s.store.CountThreads(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if n == 0 {
		if err := s.store.DeleteCategory(id); err != nil {
			s.serverError(w, err)
			return
		}
		http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
		return
	}
	switch r.FormValue("mode") {
	case "cascade":
		if err := s.store.DeleteCategory(id); err != nil {
			s.serverError(w, err)
			return
		}
	case "move":
		toID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("to")), 10, 64)
		if err != nil || toID < 1 || toID == id {
			http.Error(w, "请选择要迁往的版块", http.StatusBadRequest)
			return
		}
		target, err := s.store.GetCategoryByID(toID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if target == nil {
			http.Error(w, "目标版块不存在", http.StatusBadRequest)
			return
		}
		if err := s.store.MoveThreadsAndDeleteCategory(id, toID); err != nil {
			s.serverError(w, err)
			return
		}
	default:
		http.Error(w, "版块内还有主题,请选择「连同主题删除」或「先迁移主题」", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// createCategory POST /admin/categories:管理员建版块。
func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	if !user.IsAdmin() {
		http.Error(w, "仅管理员可创建版块", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	desc := strings.TrimSpace(r.FormValue("description"))

	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 30 ||
		len(slug) < 1 || len(slug) > 30 || strings.ContainsAny(slug, " /") {
		http.Error(w, "版块名 1–30 字符,slug 1–30 字符且不含空格/斜杠", http.StatusBadRequest)
		return
	}
	if _, err := s.store.CreateCategory(slug, name, desc); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

// ---------- 版块页:主题列表 ----------

type categoryData struct {
	web.Base
	Category    *db.Category
	Threads     []db.Thread
	Page, Pages int
	BaseURL     string
	HasQ        bool
	IsEmpty     bool
}

func (s *Server) category(w http.ResponseWriter, r *http.Request) {
	cat, err := s.store.GetCategoryBySlug(r.PathValue("slug"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if cat == nil {
		http.NotFound(w, r)
		return
	}
	page := pageParam(r)
	total, err := s.store.CountThreads(cat.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	threads, err := s.store.ListThreads(cat.ID, threadsPerPage, (page-1)*threadsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "category", categoryData{
		Base:     s.base(r, cat.Name),
		Category: cat,
		Threads:  threads,
		Page:     page,
		Pages:    totalPages(total, threadsPerPage),
		BaseURL:  "/c/" + cat.Slug,
		IsEmpty:  total == 0,
	})
}

// ---------- 新主题:直接发帖(标题 + 版块下拉 + 正文),不再先走版块选择页 ----------

type newThreadData struct {
	web.Base
	Categories []db.Category
	Category   *db.Category // 预选版块(可选;下拉框会选中它)
	Error      string
	FormTitle  string // 发帖草稿标题(避免遮蔽 Base.Title 页面标题)
	Content    string
	// 帖子类型与门槛草稿(校验失败后回填)
	Kind     string
	MinLevel int
	Price    int64
	Prize    string
	Winners  int
	Stake    int64
	// 抽奖:PayKind 决定表单显示哪一组字段(item=实物奖 / points=积分奖)
	PayKind    string
	Sponsor    int64
	MaxEntries int
	DrawAtStr  string // datetime-local 的原始值,回填用
}

// loadNewThreadData 组装发帖表单:全量版块 + 预选 slug(可为空)。
func (s *Server) loadNewThreadData(r *http.Request, selSlug string) (newThreadData, error) {
	d := newThreadData{Base: s.base(r, "发新帖"), Kind: "normal", Winners: 1, PayKind: "item"}
	cats, err := s.store.ListCategories()
	if err != nil {
		return d, err
	}
	d.Categories = cats
	if selSlug != "" {
		for i := range cats {
			if cats[i].Slug == selSlug {
				d.Category = &cats[i]
				break
			}
		}
	}
	return d, nil
}

// newThreadForm GET /new:发帖表单(版块下拉直接选)。
func (s *Server) newThreadForm(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(w, r) == nil {
		return
	}
	d, err := s.loadNewThreadData(r, "")
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "new_thread", d)
}

// newThreadFormIn GET /c/{slug}/new:直达发帖并预选版块(版块页「发新帖」入口)。
func (s *Server) newThreadFormIn(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(w, r) == nil {
		return
	}
	slug := strings.TrimSpace(r.PathValue("slug"))
	d, err := s.loadNewThreadData(r, slug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if slug != "" && d.Category == nil {
		http.NotFound(w, r)
		return
	}
	s.rend.Render(w, 200, "new_thread", d)
}

// newThread POST /c/{slug}/new:旧直达路径(向后兼容,不发 category 字段)。
func (s *Server) newThread(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	slug := strings.TrimSpace(r.PathValue("slug"))
	cat, err := s.store.GetCategoryBySlug(slug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if cat == nil {
		http.NotFound(w, r)
		return
	}
	s.createThread(w, r, user, cat, slug)
}

// newThreadPost POST /new:发帖表单提交(取 category 下拉选中的版块)。
func (s *Server) newThreadPost(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	slug := strings.TrimSpace(r.FormValue("category"))
	cat, err := s.store.GetCategoryBySlug(slug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if cat == nil {
		d, derr := s.loadNewThreadData(r, slug)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		d.FormTitle = strings.TrimSpace(r.FormValue("title"))
		d.Content = strings.TrimSpace(r.FormValue("content"))
		d.Error = "请选择要发往的版块"
		s.rend.Render(w, 200, "new_thread", d)
		return
	}
	s.createThread(w, r, user, cat, slug)
}

// createThread 校验并落库主题(两种提交路径共用);失败回填表单。
func (s *Server) createThread(w http.ResponseWriter, r *http.Request, user *db.User, cat *db.Category, selSlug string) {
	title := strings.TrimSpace(r.FormValue("title"))
	content := strings.TrimSpace(r.FormValue("content"))
	// 表单里的类型有三档:normal / lottery(实物奖) / lottery_points(积分奖)。
	// threads.kind 仍然只有 normal|lottery,奖品形态记在 lotteries.pay_kind 上。
	formKind := strings.TrimSpace(r.FormValue("kind"))
	kind, payKind := "normal", "item"
	switch formKind {
	case "lottery":
		kind = "lottery"
	case "lottery_points":
		kind, payKind = "lottery", "points"
	default:
		formKind = "normal" // 回填时靠它选中芯片,别留空值
	}
	minLevel, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("min_level")))
	price, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("price")), 10, 64)
	prize := strings.TrimSpace(r.FormValue("prize"))
	winners, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("winners")))
	stake, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("stake")), 10, 64)
	sponsor, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("sponsor")), 10, 64)
	maxEntries, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("max_entries")))
	drawAtStr := strings.TrimSpace(r.FormValue("draw_at"))
	var drawAt int64
	if drawAtStr != "" {
		// datetime-local 不带时区。只用服务端 time.Local 解析的话,服务器时区
		// 和用户不一致就会整体偏几小时(容器不设 TZ 时是 UTC,用户在东八区,
		// 结果「到点」比预期晚 8 小时)。所以让浏览器把自己的 UTC 偏移一起发来,
		// 拿不到(禁用 JS)才退回服务端本地时区。
		loc := time.Local
		if off, err := strconv.Atoi(strings.TrimSpace(r.FormValue("tz_offset"))); err == nil &&
			off >= -14*60 && off <= 14*60 {
			loc = time.FixedZone("client", off*60)
		}
		if ts, err := time.ParseInLocation("2006-01-02T15:04", drawAtStr, loc); err == nil {
			drawAt = ts.Unix()
		}
	}
	if payKind == "item" && winners < 1 {
		winners = 1 // 实物奖必须有明确人数,0 人没意义
	}

	fail := func(msg string) {
		d, err := s.loadNewThreadData(r, selSlug)
		if err != nil {
			s.serverError(w, err)
			return
		}
		d.Error, d.FormTitle, d.Content = msg, title, content
		d.Kind, d.MinLevel, d.Price = formKind, minLevel, price
		d.Prize, d.Winners, d.Stake = prize, winners, stake
		d.PayKind, d.Sponsor, d.MaxEntries, d.DrawAtStr = payKind, sponsor, maxEntries, drawAtStr
		s.rend.Render(w, 200, "new_thread", d)
	}
	switch {
	case utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > maxTitleLen:
		fail("标题 1–120 字")
		return
	case utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > maxPostLen:
		fail("正文 1–10000 字")
		return
	case minLevel < 0 || minLevel > 6:
		fail("观看等级门槛需在 LV0–LV6 之间")
		return
	case price < 0 || price > maxThreadPrice:
		fail("观看积分需在 0–10000 之间")
		return
	}
	if kind == "lottery" {
		switch {
		case payKind == "item" && (utf8.RuneCountInString(prize) < 1 || utf8.RuneCountInString(prize) > 80):
			fail("请填写奖品说明(1–80 字)")
			return
		case winners < 0 || winners > 50:
			fail("中奖人数需在 0–50 之间(0=不设人数,参与者全员分)")
			return
		case stake < 0 || stake > maxLotteryStake:
			fail("参与积分需在 0–1000 之间")
			return
		case maxEntries < 0 || maxEntries > 10000:
			fail("参与人数上限需在 0–10000 之间(0=不限)")
			return
		case drawAtStr != "" && drawAt <= time.Now().Unix():
			fail("自动开奖时间要晚于现在")
			return
		case drawAtStr != "" && drawAt > time.Now().Add(90*24*time.Hour).Unix():
			fail("自动开奖时间最多设到 90 天后")
			return
		}
		if payKind == "points" {
			switch {
			case sponsor < 0 || sponsor > maxLotteryPool:
				fail("奖池积分需在 0–100000 之间")
				return
			case sponsor < 1 && stake < 1:
				fail("积分抽奖要么自己出奖池,要么设参与投入,不然没有奖可发")
				return
			case stake < 1 && winners > 0 && int64(winners) > sponsor:
				// 每人至少分到 1 分,所以中奖人数不可能超过奖池
				fail("中奖人数不能超过奖池积分 —— 每位中奖者至少分到 1 积分")
				return
			}
			// 积分奖的「奖品」是奖池本身,标题上不再要求另填说明
			prize = "积分奖池"
		}
	}
	id, err := s.store.CreateThread(cat.ID, user.ID, title, content, markdown.Render(content), db.NewThread{
		Kind: kind, MinLevel: minLevel, Price: price,
		Prize: prize, Winners: winners, Stake: stake,
		PayKind: payKind, Sponsor: sponsor, MaxEntries: maxEntries, DrawAt: drawAt,
	})
	if err == db.ErrNotEnoughPoints {
		fail("你的积分不够垫这个奖池")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t, err := s.store.GetThread(id); err == nil && t != nil {
		s.notifyReply(user.ID, t, 0, content) // 首帖里的 @提及也要通知
	}
	http.Redirect(w, r, "/t/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ---------- 主题页 ----------

type threadData struct {
	web.Base
	Thread      *db.Thread
	Category    *db.Category
	First       *db.Post // 首帖(op-card 区)
	PostViews   []web.PostView
	Page, Pages int
	BaseURL     string
	HasQ        bool
	LikeCount   int64 // 文章(首帖)获赞
	LikedByMe   bool
	FavCount    int64
	FavedByMe   bool
	TipTotal    int64 // 该帖收到的打赏总额
	CanTip      bool  // 登录且不是自己的帖子
	MyPoints    int64 // 我的积分余额(打赏面板提示)
	CanModerate bool  // 当前查看者:管理员或该版块的版主(可置顶/锁定)
	Gate        threadGate
	Lot         *db.Lottery        // 抽奖设置(非抽奖帖为 nil)
	LotEntries  []db.LotteryEntry  // 参与名单(中奖者在前)
	LotJoined   bool               // 我是否已参与
	CanDraw     bool               // 我能否开奖(楼主/管理员且未开奖)
	Unlocks     int64              // 付费帖已解锁人数(作者/管理员可见)
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.GetThread(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	page := pageParam(r)
	total, err := s.store.CountPosts(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	posts, err := s.store.ListPosts(t.ID, postsPerPage, (page-1)*postsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cat, err := s.store.GetCategoryByID(t.CategoryID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.store.IncrThreadViews(t.ID)

	viewer := auth.From(r.Context()).User
	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}
	likeMap, err := s.store.PostLikesByThread(t.ID, viewerID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	first, err := s.store.GetFirstPost(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	canMod := false
	if viewer != nil {
		if viewer.IsAdmin() {
			canMod = true
		} else if viewer.IsMod() {
			canMod, err = s.store.IsModOf(viewer.ID, t.CategoryID)
			if err != nil {
				s.serverError(w, err)
				return
			}
		}
	}
	pvs := make([]web.PostView, 0, len(posts))
	for _, p := range posts {
		if p.IsFirst {
			continue // 首帖已单独取到,进 op-card 区
		}
		floor, err := s.store.CountPostsUpTo(t.ID, p.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		likes := likeMap[p.ID]
		canDelete := viewer != nil && (p.AuthorID == viewer.ID || canMod)
		pvs = append(pvs, web.PostView{Post: p, Viewer: viewer, Floor: floor,
			IsOP: p.AuthorID == t.AuthorID, LikeCount: likes.Count, LikedByMe: likes.Liked,
			CanDelete: canDelete})
	}
	likeCount, favCount, liked, faved, err := s.store.ThreadReacts(t.ID, 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if viewer != nil {
		likeCount, favCount, liked, faved, err = s.store.ThreadReacts(t.ID, viewer.ID)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}
	tipTotal, err := s.store.ThreadTipTotal(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	myPoints := int64(0)
	if viewer != nil {
		myPoints = viewer.Points
	}
	base := s.base(r, t.Title)
	gate, err := s.gateFor(t, viewer, base.Level, canMod)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 门槛没过就不把正文与回复送到模板里,避免「样式挡住但源码能看」
	if !gate.OK {
		pvs = nil
	}
	var lot *db.Lottery
	var lotEntries []db.LotteryEntry
	lotJoined, canDraw := false, false
	if t.Kind == "lottery" {
		if lot, err = s.store.GetLottery(t.ID); err != nil {
			s.serverError(w, err)
			return
		}
		if lot != nil {
			if lotEntries, err = s.store.ListLotteryEntries(t.ID, lotteryEntryLimit); err != nil {
				s.serverError(w, err)
				return
			}
			if viewer != nil {
				if lotJoined, err = s.store.JoinedLottery(t.ID, viewer.ID); err != nil {
					s.serverError(w, err)
					return
				}
				canDraw = !lot.Over() && (viewer.ID == t.AuthorID || viewer.IsAdmin())
			}
		}
	}
	var unlocks int64
	if t.Price > 0 && viewer != nil && (viewer.ID == t.AuthorID || viewer.IsAdmin()) {
		if unlocks, err = s.store.CountThreadUnlocks(t.ID); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.rend.Render(w, 200, "thread", threadData{
		Base:        base,
		Thread:      t,
		Category:    cat,
		First:       first,
		PostViews:   pvs,
		Page:        page,
		Pages:       totalPages(total, postsPerPage),
		BaseURL:     "/t/" + strconv.FormatInt(t.ID, 10),
		LikeCount:   likeCount,
		LikedByMe:   liked,
		FavCount:    favCount,
		FavedByMe:   faved,
		TipTotal:    tipTotal,
		CanTip:      viewer != nil && viewer.ID != t.AuthorID,
		MyPoints:    myPoints,
		CanModerate: canMod,
		Gate:        gate,
		Lot:         lot,
		LotEntries:  lotEntries,
		LotJoined:   lotJoined,
		CanDraw:     canDraw,
		Unlocks:     unlocks,
	})
}

// ---------- 回复(htmx 局部刷新) ----------

func (s *Server) reply(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.GetThread(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	if t.IsLocked {
		http.Error(w, "主题已锁定,无法回复", http.StatusForbidden)
		return
	}
	// 有阅读门槛的帖子,没过门槛也不能回复
	canMod := s.canModerateThread(user, t)
	level, err := s.userLevel(user)
	if err != nil {
		s.serverError(w, err)
		return
	}
	gate, err := s.gateFor(t, user, level, canMod)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !gate.OK {
		http.Error(w, "先满足阅读门槛才能回复", http.StatusForbidden)
		return
	}
	content := strings.TrimSpace(r.FormValue("content"))
	if utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > maxPostLen {
		http.Error(w, "正文 1–10000 字", http.StatusUnprocessableEntity)
		return
	}
	// 抽奖帖:回复即参与,设了参与积分就一并扣款
	if msg := s.joinLotteryOnReply(t, user); msg != "" {
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	postID, err := s.store.CreatePost(t.ID, user.ID, content, markdown.Render(content))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.notifyReply(user.ID, t, postID, content)
	p, err := s.store.GetPost(postID)
	if err != nil || p == nil {
		s.serverError(w, err)
		return
	}
	floor, err := s.store.CountPostsUpTo(t.ID, postID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Partial(w, 200, "thread", "post", web.PostView{
		Post: *p, Viewer: user, Floor: floor, IsOP: user.ID == t.AuthorID,
		CanDelete: user.ID == p.AuthorID,
	})
}

// ---------- 删除 ----------

// deletePost 删除单条回复(非首帖),htmx 调用,返回空 body 让前端移除元素。
func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPost(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		http.NotFound(w, r) // 已删过,幂等处理:htmx 会收到 404 并保留元素,可接受
		return
	}
	if p.IsFirst {
		http.Error(w, "首帖需通过「删除主题」移除整个主题", http.StatusBadRequest)
		return
	}
	if !user.IsAdmin() && user.ID != p.AuthorID && !s.canModeratePost(user, p) {
		http.Error(w, "只能删除自己的帖子", http.StatusForbidden)
		return
	}
	if err := s.store.DeletePost(p.ID); err != nil {
		s.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// deleteThread 删除整个主题(首帖或管理员发起),常规表单 + 跳回版块。
func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.GetThread(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	if !user.IsAdmin() && user.ID != t.AuthorID {
		if !user.IsMod() {
			http.Error(w, "只能删除自己的主题", http.StatusForbidden)
			return
		}
		mod, err := s.store.IsModOf(user.ID, t.CategoryID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !mod {
			http.Error(w, "仅该版块的版主/管理员可删除", http.StatusForbidden)
			return
		}
	}
	if err := s.store.DeleteThread(t.ID); err != nil {
		s.serverError(w, err)
		return
	}
	slug := ""
	if cat, err := s.store.GetCategoryByID(t.CategoryID); err == nil && cat != nil {
		slug = cat.Slug
	}
	s.redirectAfter(w, r, "/c/"+slug)
}

// ---------- 编辑 ----------

func (s *Server) canEditPost(user *db.User, p *db.Post) bool {
	return user != nil && p != nil && (user.ID == p.AuthorID || user.IsAdmin())
}

// canModeratePost 该用户能否以管理身份删这条回复:管理员,或回复所在主题
// 的管辖版主。帖子归属查询失败一律按无权处理。
func (s *Server) canModeratePost(user *db.User, p *db.Post) bool {
	if user == nil || p == nil || !user.IsMod() {
		return false
	}
	t, err := s.store.GetThread(p.ThreadID)
	if err != nil || t == nil {
		return false
	}
	if user.IsAdmin() {
		return true
	}
	mod, err := s.store.IsModOf(user.ID, t.CategoryID)
	return err == nil && mod
}

// canModerateThread 该用户是否为该主题所在版块的管理者(管理员或管辖版主)。
func (s *Server) canModerateThread(user *db.User, t *db.Thread) bool {
	if user == nil || t == nil {
		return false
	}
	if user.IsAdmin() {
		return true
	}
	if !user.IsMod() {
		return false
	}
	mod, err := s.store.IsModOf(user.ID, t.CategoryID)
	return err == nil && mod
}

// lockedText 锁定主题的编辑限制:作者之外仅版主/管理员可继续操作。
func lockedForbidden(user *db.User, t *db.Thread) bool {
	return t != nil && t.IsLocked && !user.IsMod()
}

type editThreadData struct {
	web.Base
	Category  *db.Category
	Thread    *db.Thread
	FormTitle string // 主题标题草稿(避免遮蔽 Base.Title 页面标题)
	Content   string
	Error     string
}

func (s *Server) loadEditableThread(w http.ResponseWriter, r *http.Request) (*db.Thread, *db.User) {
	user := s.currentUser(w, r)
	if user == nil {
		return nil, nil
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return nil, nil
	}
	t, err := s.store.GetThread(id)
	if err != nil {
		s.serverError(w, err)
		return nil, nil
	}
	if t == nil {
		http.NotFound(w, r)
		return nil, nil
	}
	return t, user
}

func (s *Server) editThreadForm(w http.ResponseWriter, r *http.Request) {
	t, user := s.loadEditableThread(w, r)
	if t == nil {
		return
	}
	first, err := s.store.GetFirstPost(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if first == nil {
		http.NotFound(w, r)
		return
	}
	if !s.canEditPost(user, first) {
		http.Error(w, "只能编辑自己的主题", http.StatusForbidden)
		return
	}
	if lockedForbidden(user, t) {
		http.Error(w, "主题已锁定,仅版主/管理员可编辑", http.StatusForbidden)
		return
	}
	cat, err := s.store.GetCategoryByID(t.CategoryID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "edit_thread", editThreadData{
		Base:      s.base(r, "编辑主题"),
		Category:  cat,
		Thread:    t,
		FormTitle: t.Title,
		Content:   first.ContentMD,
	})
}

func (s *Server) editThread(w http.ResponseWriter, r *http.Request) {
	t, user := s.loadEditableThread(w, r)
	if t == nil {
		return
	}
	first, err := s.store.GetFirstPost(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if first == nil {
		http.NotFound(w, r)
		return
	}
	if !s.canEditPost(user, first) {
		http.Error(w, "只能编辑自己的主题", http.StatusForbidden)
		return
	}
	if lockedForbidden(user, t) {
		http.Error(w, "主题已锁定,仅版主/管理员可编辑", http.StatusForbidden)
		return
	}
	cat, err := s.store.GetCategoryByID(t.CategoryID)
	if err != nil {
		s.serverError(w, err)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	content := strings.TrimSpace(r.FormValue("content"))
	fail := func(msg string) {
		s.rend.Render(w, 200, "edit_thread", editThreadData{
			Base:      s.base(r, "编辑主题"),
			Category:  cat,
			Thread:    t,
			FormTitle: title,
			Content:   content,
			Error:     msg,
		})
	}
	switch {
	case utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > maxTitleLen:
		fail("标题 1–120 字")
		return
	case utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > maxPostLen:
		fail("正文 1–10000 字")
		return
	}
	if err := s.store.UpdateThreadTitle(t.ID, title); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.store.UpdatePost(first.ID, content, markdown.Render(content)); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/t/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
}

type editPostData struct {
	web.Base
	Thread  *db.Thread
	PostID  int64
	Content string
	Error   string
}

func (s *Server) editPostForm(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPost(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil || p.IsFirst {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.GetThread(p.ThreadID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !s.canEditPost(user, p) {
		http.Error(w, "只能编辑自己的回复", http.StatusForbidden)
		return
	}
	if lockedForbidden(user, t) {
		http.Error(w, "主题已锁定,仅版主/管理员可编辑", http.StatusForbidden)
		return
	}
	s.rend.Render(w, 200, "edit_post", editPostData{
		Base:    s.base(r, "编辑回复"),
		Thread:  t,
		PostID:  p.ID,
		Content: p.ContentMD,
	})
}

func (s *Server) editPost(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPost(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil || p.IsFirst {
		http.NotFound(w, r)
		return
	}
	t, err := s.store.GetThread(p.ThreadID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !s.canEditPost(user, p) {
		http.Error(w, "只能编辑自己的回复", http.StatusForbidden)
		return
	}
	if lockedForbidden(user, t) {
		http.Error(w, "主题已锁定,仅版主/管理员可编辑", http.StatusForbidden)
		return
	}

	content := strings.TrimSpace(r.FormValue("content"))
	if utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > maxPostLen {
		s.rend.Render(w, 200, "edit_post", editPostData{
			Base:    s.base(r, "编辑回复"),
			Thread:  t,
			PostID:  p.ID,
			Content: content,
			Error:   "正文 1–10000 字",
		})
		return
	}
	if err := s.store.UpdatePost(p.ID, content, markdown.Render(content)); err != nil {
		s.serverError(w, err)
		return
	}
	page := 1
	if n, err := s.store.CountPostsUpTo(t.ID, p.ID); err == nil && n > 0 {
		page = totalPages(n, postsPerPage)
	}
	u := "/t/" + strconv.FormatInt(t.ID, 10)
	if page > 1 {
		u += "?page=" + strconv.Itoa(page)
	}
	http.Redirect(w, r, u+"#p"+strconv.FormatInt(p.ID, 10), http.StatusSeeOther)
}
