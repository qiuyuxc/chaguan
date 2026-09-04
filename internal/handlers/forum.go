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
	s.rend.Render(w, 200, "admin_categories", adminCatsData{
		Base:       s.base(r, "版块管理"),
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

// ---------- 新主题 ----------

type newThreadData struct {
	web.Base
	Category *db.Category
	Error    string
	Title    string
	Content  string
}

func (s *Server) newThreadForm(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(w, r) == nil {
		return
	}
	cat, err := s.store.GetCategoryBySlug(r.PathValue("slug"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if cat == nil {
		http.NotFound(w, r)
		return
	}
	s.rend.Render(w, 200, "new_thread", newThreadData{
		Base: s.base(r, "发新帖 · "+cat.Name), Category: cat,
	})
}

func (s *Server) newThread(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	cat, err := s.store.GetCategoryBySlug(r.PathValue("slug"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if cat == nil {
		http.NotFound(w, r)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	content := strings.TrimSpace(r.FormValue("content"))

	fail := func(msg string) {
		s.rend.Render(w, 200, "new_thread", newThreadData{
			Base: s.base(r, "发新帖 · "+cat.Name), Category: cat,
			Error: msg, Title: title, Content: content,
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
}

// newThreadPicker GET /new:发帖前先选版块(顶栏「发帖」入口)。
type newPickerData struct {
	web.Base
	Categories []db.Category
	Category   *db.Category // 占位:与发帖表单共享模板分支(恒为 nil)
}

func (s *Server) newThreadPicker(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(w, r) == nil {
		return
	}
	cats, err := s.store.ListCategories()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "new_thread", newPickerData{
		Base:       s.base(r, "发新帖"),
		Categories: cats,
	})
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
	first, err := s.store.GetFirstPost(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
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
		pvs = append(pvs, web.PostView{Post: p, Viewer: viewer, Floor: floor, IsOP: p.AuthorID == t.AuthorID})
	}
	s.rend.Render(w, 200, "thread", threadData{
		Base:      s.base(r, t.Title),
		Thread:    t,
		Category:  cat,
		First:     first,
		PostViews: pvs,
		Page:      page,
		Pages:     totalPages(total, postsPerPage),
		BaseURL:   "/t/" + strconv.FormatInt(t.ID, 10),
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
	if !user.IsAdmin() && user.ID != p.AuthorID {
		http.Error(w, "只能删除自己的帖子", http.StatusForbidden)
		return
	}
	if p.IsFirst {
		http.Error(w, "首帖需通过「删除主题」移除整个主题", http.StatusBadRequest)
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
		http.Error(w, "只能删除自己的主题", http.StatusForbidden)
		return
	}
	if err := s.store.DeleteThread(t.ID); err != nil {
		s.serverError(w, err)
		return
	}
	slug := ""
	if cat, err := s.store.GetCategoryByID(t.CategoryID); err == nil && cat != nil {
		slug = cat.Slug
	}
	http.Redirect(w, r, "/c/"+slug, http.StatusSeeOther)
}

// ---------- 编辑 ----------

func (s *Server) canEditPost(user *db.User, p *db.Post) bool {
	return user != nil && p != nil && (user.ID == p.AuthorID || user.IsAdmin())
}

// lockedText 锁定主题的编辑限制:作者之外仅版主/管理员可继续操作。
func lockedForbidden(user *db.User, t *db.Thread) bool {
	return t != nil && t.IsLocked && !user.IsMod()
}

type editThreadData struct {
	web.Base
	Category *db.Category
	Thread   *db.Thread
	Title    string
	Content  string
	Error    string
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
		Base:     s.base(r, "编辑主题"),
		Category: cat,
		Thread:   t,
		Title:    t.Title,
		Content:  first.ContentMD,
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
			Base:     s.base(r, "编辑主题"),
			Category: cat,
			Thread:   t,
			Title:    title,
			Content:  content,
			Error:    msg,
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
