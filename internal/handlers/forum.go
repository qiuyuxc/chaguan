package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
		if threads, err := s.store.CountUserThreads(u.ID); err == nil {
			if replies, err := s.store.CountUserReplies(u.ID); err == nil {
				if stats, err := s.store.SocialStats(u.ID); err == nil {
					exp := socialExp(threads, replies, stats.Liked)
					level, start, next := levelInfo(exp, u.LevelOverride)
					b.Exp = exp
					b.Level = level
					b.ExpNext = next
					b.ExpPct = 100
					if next > start {
						b.ExpPct = int((exp - start) * 100 / (next - start))
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

// deleteCategory 删除空版块(有主题的版块禁止删除,防止级联清空)。
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
	n, err := s.store.CountThreads(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if n > 0 {
		http.Error(w, "版块内还有主题,不能删除", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteCategory(id); err != nil {
		s.serverError(w, err)
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
}

// loadNewThreadData 组装发帖表单:全量版块 + 预选 slug(可为空)。
func (s *Server) loadNewThreadData(r *http.Request, selSlug string) (newThreadData, error) {
	d := newThreadData{Base: s.base(r, "发新帖")}
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
	fail := func(msg string) {
		d, err := s.loadNewThreadData(r, selSlug)
		if err != nil {
			s.serverError(w, err)
			return
		}
		d.Error, d.FormTitle, d.Content = msg, title, content
		s.rend.Render(w, 200, "new_thread", d)
	}
	switch {
	case utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > maxTitleLen:
		fail("标题 1–120 字")
		return
	case utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > maxPostLen:
		fail("正文 1–10000 字")
		return
	}
	id, err := s.store.CreateThread(cat.ID, user.ID, title, content, markdown.Render(content))
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
	CanModerate bool // 当前查看者:管理员或该版块的版主(可置顶/锁定)
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
	s.rend.Render(w, 200, "thread", threadData{
		Base:        s.base(r, t.Title),
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
		CanModerate: canMod,
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
	content := strings.TrimSpace(r.FormValue("content"))
	if utf8.RuneCountInString(content) < 1 || utf8.RuneCountInString(content) > maxPostLen {
		http.Error(w, "正文 1–10000 字", http.StatusUnprocessableEntity)
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
