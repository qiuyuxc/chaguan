// Package handlers 装配全部路由与中间件。
package handlers

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strconv"
	"strings"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

const (
	threadsPerPage = 15
	postsPerPage   = 15
	maxPostLen     = 10000
	maxTitleLen    = 120
)

type Server struct {
	store   *db.Store
	rend    *web.Renderer
	uploads string
}

func Routes(store *db.Store, rend *web.Renderer, uploadsDir string) http.Handler {
	s := &Server{store: store, rend: rend, uploads: uploadsDir}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /c/{slug}", s.category)
	mux.HandleFunc("GET /new", s.newThreadForm)
	mux.HandleFunc("POST /new", s.newThreadPost)
	mux.HandleFunc("GET /c/{slug}/new", s.newThreadFormIn)
	mux.HandleFunc("POST /c/{slug}/new", s.newThread)
	mux.HandleFunc("GET /t/{id}", s.thread)
	mux.HandleFunc("POST /t/{id}/reply", s.reply)
	mux.HandleFunc("GET /t/{id}/edit", s.editThreadForm)
	mux.HandleFunc("POST /t/{id}/edit", s.editThread)
	mux.HandleFunc("POST /t/{id}/pin", s.togglePin)
	mux.HandleFunc("POST /t/{id}/like", s.toggleLike)
	mux.HandleFunc("POST /t/{id}/favorite", s.toggleFavorite)
	mux.HandleFunc("POST /t/{id}/lock", s.toggleLock)
	mux.HandleFunc("POST /t/{id}/delete", s.deleteThread)
	mux.HandleFunc("GET /p/{id}/edit", s.editPostForm)
	mux.HandleFunc("POST /p/{id}/edit", s.editPost)
	mux.HandleFunc("POST /p/{id}/delete", s.deletePost)

	mux.HandleFunc("GET /u/{id}", s.profile)
	mux.HandleFunc("GET /u/{id}/edit", s.editProfileForm)
	mux.HandleFunc("POST /u/{id}/edit", s.editProfile)

	mux.HandleFunc("POST /uploads", s.uploadImage)
	mux.HandleFunc("GET /uploads/{id}", s.serveUpload)

	mux.HandleFunc("POST /admin/users/{id}/role", s.setUserRole)
	mux.HandleFunc("POST /admin/users/{id}/ban", s.banUser)
	mux.HandleFunc("POST /admin/users/{id}/unban", s.unbanUser)

	mux.HandleFunc("GET /notifications", s.notifications)
	mux.HandleFunc("GET /notifications/unread", s.unreadCount)
	mux.HandleFunc("POST /notifications/read-all", s.notificationsReadAll)
	mux.HandleFunc("POST /notifications/{id}/read", s.notificationRead)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /api/users", s.userSearch)

	mux.HandleFunc("GET /admin/categories", s.adminCategories)
	mux.HandleFunc("POST /admin/categories/{id}/delete", s.deleteCategory)

	mux.HandleFunc("POST /admin/categories", s.createCategory)

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.Static())))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return s.recoverMW(s.loadUserMW(s.csrfMW(mux)))
}

// ---------- 中间件 ----------

func (s *Server) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("panic %s %s: %v", r.Method, r.URL.Path, err)
				http.Error(w, "服务器开小差了", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// loadUserMW 读会话 cookie 注入 AuthInfo;匿名用户确保有 CSRF cookie(双提交)。
func (s *Server) loadUserMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := auth.AuthInfo{CSRF: s.ensureCSRFCookie(w, r)}
		if c, err := r.Cookie(auth.CookieName); err == nil {
			user, csrf, err := s.store.GetSessionUser(c.Value)
			if err != nil {
				log.Printf("session lookup: %v", err)
			} else if user != nil {
				info.User, info.CSRF = user, csrf
			}
		}
		next.ServeHTTP(w, r.WithContext(auth.WithAuth(r.Context(), info)))
	})
}

func (s *Server) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(auth.CSRFCookie); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	token := auth.NewToken()
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CSRFCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   auth.SessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   auth.Secure(r),
	})
	return token
}

// csrfMW 校验所有写请求:表单 _csrf 或 X-CSRF-Token 头,
// 必须与当前会话(或匿名 cookie)的 token 常量时间相等。
func (s *Server) csrfMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		info := auth.From(r.Context())
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.ParseMultipartForm(4 << 20) // 上传表单里也带 _csrf,需解析 multipart
		} else {
			r.ParseForm()
		}
		got := r.FormValue("_csrf")
		if got == "" {
			got = r.Header.Get("X-CSRF-Token")
		}
		if got == "" || info.CSRF == "" ||
			subtle.ConstantTimeCompare([]byte(got), []byte(info.CSRF)) != 1 {
			http.Error(w, "CSRF 校验失败,请刷新页面重试", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- 公共辅助 ----------

func (s *Server) serverError(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, "服务器开小差了", http.StatusInternalServerError)
}

// currentUser 未登录时重定向到 /login(写操作入口)。
func (s *Server) currentUser(w http.ResponseWriter, r *http.Request) *db.User {
	u := auth.From(r.Context()).User
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
	return u
}

func pathID(r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	return id, err == nil && id > 0
}

func pageParam(r *http.Request) int {
	p, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}

func totalPages(total int64, per int) int {
	if total == 0 {
		return 1
	}
	return int((total + int64(per) - 1) / int64(per))
}
