// Package web 内嵌并渲染全部模板与静态资源。
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"bbs/internal/db"
)

//go:embed templates/*.html templates/partials/*.html
var tmplFS embed.FS

//go:embed static
var staticFS embed.FS

// Static 返回静态资源子文件系统(htmx.min.js / style.css / app.js)。
func Static() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// Base 是所有页面数据结构的公共头。
type Base struct {
	Title string
	User  *db.User // 可能为 nil(未登录)
	CSRF  string
}

// PostView 是帖子 partial 的数据:帖子 + 当前查看者(决定是否显示删除按钮)。
type PostView struct {
	Post   db.Post
	Viewer *db.User
}

var funcs = template.FuncMap{
	"relTime": RelTime,
	"csrfField": func(token string) template.HTML {
		return template.HTML(`<input type="hidden" name="_csrf" value="` + token + `">`)
	},
	"initial":  Initial,
	"date":     func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") },
	"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	"postView": func(p db.Post, viewer *db.User) PostView {
		return PostView{Post: p, Viewer: viewer}
	},
	"canDeletePost": canDeletePost,
	"canEditPost":   canEditPost,
	"pages": func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i + 1
		}
		return out
	},
}

func canDeletePost(p db.Post, viewer *db.User) bool {
	if viewer == nil || p.IsFirst {
		return false // 首帖的删除 = 删主题,走独立入口
	}
	return viewer.ID == p.AuthorID || viewer.IsAdmin()
}

// canEditPost 与删除同权:作者本人或管理员可编辑。
func canEditPost(p db.Post, viewer *db.User) bool {
	return viewer != nil && (viewer.ID == p.AuthorID || viewer.IsAdmin())
}

// Initial 取名字的第一个字符(按 rune,支持中文)做头像占位。
func Initial(name string) string {
	for _, r := range name {
		return string(r)
	}
	return "?"
}

// RelTime 把 unix 时间戳转成中文相对时间。
func RelTime(ts int64) string {
	d := time.Now().Unix() - ts
	switch {
	case d < 60:
		return "刚刚"
	case d < 3600:
		return fmt.Sprintf("%d 分钟前", d/60)
	case d < 86400:
		return fmt.Sprintf("%d 小时前", d/3600)
	case d < 30*86400:
		return fmt.Sprintf("%d 天前", d/86400)
	default:
		return time.Unix(ts, 0).Format("2006-01-02")
	}
}

// Renderer 按页面名持有解析好的模板集。
type Renderer struct {
	pages map[string]*template.Template
}

var pageNames = []string{
	"home", "login", "register", "category", "thread", "new_thread",
	"edit_thread", "edit_post", "profile", "edit_profile", "notifications",
}

func NewRenderer() (*Renderer, error) {
	r := &Renderer{pages: make(map[string]*template.Template, len(pageNames))}
	for _, name := range pageNames {
		t, err := template.New("layout").Funcs(funcs).ParseFS(tmplFS,
			"templates/layout.html",
			"templates/partials/post.html",
			"templates/partials/pagination.html",
			"templates/"+name+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		r.pages[name] = t
	}
	return r, nil
}

// Render 输出整页。
func (r *Renderer) Render(w io.Writer, status int, name string, data any) error {
	t, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("unknown page template: %s", name)
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(status)
	}
	return t.ExecuteTemplate(w, "layout", data)
}

// Partial 输出单个 define 片段(htmx 局部刷新用)。
func (r *Renderer) Partial(w io.Writer, status int, page, define string, data any) error {
	t, ok := r.pages[page]
	if !ok {
		return fmt.Errorf("unknown page template: %s", page)
	}
	tt := t.Lookup(define)
	if tt == nil {
		return fmt.Errorf("unknown define %q in %s", define, page)
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		if strings.Contains(define, "post") {
			rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		rw.WriteHeader(status)
	}
	return tt.Execute(w, data)
}
