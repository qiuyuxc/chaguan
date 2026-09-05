// Package handlers 装配全部路由与中间件。
package handlers

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

const (
	threadsPerPage = 15
	postsPerPage   = 15
	maxPostLen     = 10000
	maxTitleLen    = 120
	// 发帖时可设的上限:付费帖价格、抽奖参与投入、楼主自掏的抽奖奖池
	maxThreadPrice  = 10000
	maxLotteryStake = 1000
	maxLotteryPool  = 100000
)

type Server struct {
	store   *db.Store
	rend    *web.Renderer
	uploads string
	hub     *hub // 实时推送(SSE)连接池
}

// Options 是几个运行期旋钮。零值走生产默认值;测试脚本传很小的值,
// 免得为了验证「24 小时后退回」真等一天。
type Options struct {
	RedpackTTL time.Duration // 红包未领取多久自动退回(默认 24h)
	SweepEvery time.Duration // 后台巡检间隔(默认 1 分钟)
}

func (o Options) redpackTTL() time.Duration {
	if o.RedpackTTL > 0 {
		return o.RedpackTTL
	}
	return 24 * time.Hour
}

// sweepEvery 决定「定点开奖」的实际精度:设定时刻最多晚一个巡检周期才生效。
// 所以别调太大 —— 一分钟一次两条带索引的查询,本地 SQLite 上开销可以忽略。
func (o Options) sweepEvery() time.Duration {
	if o.SweepEvery > 0 {
		return o.SweepEvery
	}
	return time.Minute
}

func Routes(store *db.Store, rend *web.Renderer, uploadsDir string, opts Options) http.Handler {
	s := &Server{store: store, rend: rend, uploads: uploadsDir, hub: newHub()}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.register)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /login/2fa", s.twoFAForm)
	mux.HandleFunc("POST /login/2fa", s.twoFA)
	mux.HandleFunc("GET /account", s.account)
	mux.HandleFunc("POST /account/name", s.accountName)
	mux.HandleFunc("POST /account/password", s.accountPassword)
	mux.HandleFunc("POST /account/email", s.accountEmail)
	mux.HandleFunc("POST /account/2fa/setup", s.account2FASetup)
	mux.HandleFunc("POST /account/2fa/enable", s.account2FAEnable)
	mux.HandleFunc("POST /account/2fa/disable", s.account2FADisable)
	mux.HandleFunc("GET /verify/email", s.verifyEmail)
	mux.HandleFunc("GET /verify/resend", s.resendForm)
	mux.HandleFunc("POST /verify/resend", s.resendVerify)
	mux.HandleFunc("GET /forgot", s.forgotForm)
	mux.HandleFunc("POST /forgot", s.forgot)
	mux.HandleFunc("GET /reset", s.resetForm)
	mux.HandleFunc("POST /reset", s.reset)

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
	mux.HandleFunc("POST /t/{id}/tip", s.tip)
	mux.HandleFunc("POST /t/{id}/unlock", s.unlockThread)
	mux.HandleFunc("POST /t/{id}/draw", s.drawLottery)
	mux.HandleFunc("POST /t/{id}/lock", s.toggleLock)
	mux.HandleFunc("POST /t/{id}/delete", s.deleteThread)
	mux.HandleFunc("POST /p/{id}/like", s.togglePostLike)
	mux.HandleFunc("GET /p/{id}/edit", s.editPostForm)
	mux.HandleFunc("POST /p/{id}/edit", s.editPost)
	mux.HandleFunc("POST /p/{id}/delete", s.deletePost)

	mux.HandleFunc("GET /u/{id}", s.profile)
	mux.HandleFunc("GET /u/{id}/edit", s.editProfileForm)
	mux.HandleFunc("POST /u/{id}/edit", s.editProfile)
	mux.HandleFunc("POST /u/{id}/badge", s.wearBadge)
	mux.HandleFunc("GET /shop", s.shop)
	mux.HandleFunc("POST /shop/{id}/redeem", s.redeem)

	mux.HandleFunc("POST /uploads", s.uploadImage)
	mux.HandleFunc("GET /uploads/{id}", s.serveUpload)

	mux.HandleFunc("POST /admin/users/{id}/role", s.setUserRole)
	mux.HandleFunc("POST /admin/users/{id}/ban", s.banUser)
	mux.HandleFunc("POST /admin/users/{id}/unban", s.unbanUser)
	mux.HandleFunc("POST /admin/users/{id}/verify", s.setVerify)
	mux.HandleFunc("POST /admin/users/{id}/level", s.setUserLevel)
	mux.HandleFunc("POST /admin/users/{id}/modcat/remove", s.removeModCategory)
	mux.HandleFunc("POST /admin/users/{id}/social", s.setSocialStats)
	mux.HandleFunc("POST /admin/users/{id}/edit", s.adminEditUser)
	mux.HandleFunc("POST /admin/users/{id}/avatar", s.adminSetAvatar)
	mux.HandleFunc("GET /verify/apply", s.verifyApplyForm)
	mux.HandleFunc("POST /verify/apply", s.verifyApplyPost)
	mux.HandleFunc("GET /admin/verify", s.adminVerify)
	mux.HandleFunc("POST /admin/verify/add", s.adminAddVerify)
	mux.HandleFunc("POST /admin/verify/{id}/approve", s.resolveVerify)
	mux.HandleFunc("POST /admin/verify/{id}/reject", s.resolveVerify)
	mux.HandleFunc("POST /admin/verify/{id}/remove", s.adminRemoveVerify)

	mux.HandleFunc("GET /notifications", s.notifications)
	mux.HandleFunc("GET /notifications/unread", s.unreadCount)
	mux.HandleFunc("POST /notifications/read-all", s.notificationsReadAll)
	mux.HandleFunc("POST /notifications/{id}/read", s.notificationRead)
	mux.HandleFunc("GET /events", s.events)
	mux.HandleFunc("GET /settings", s.settings)
	mux.HandleFunc("POST /settings/notify", s.saveNotifySettings)
	mux.HandleFunc("GET /points", s.points)
	mux.HandleFunc("POST /checkin", s.checkin)
	mux.HandleFunc("GET /messages", s.messages)
	mux.HandleFunc("POST /messages/start", s.dmStart)
	mux.HandleFunc("GET /messages/{id}", s.dmThread)
	mux.HandleFunc("GET /messages/{id}/list", s.dmList)
	mux.HandleFunc("POST /messages/{id}/send", s.dmSend)
	mux.HandleFunc("POST /messages/{id}/redpack", s.dmRedpack)
	mux.HandleFunc("POST /messages/{id}/claim", s.dmClaim)
	mux.HandleFunc("POST /messages/{id}/refund", s.dmRefund)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /api/users", s.userSearch)

	mux.HandleFunc("GET /admin/categories", s.adminCategories)
	mux.HandleFunc("POST /admin/categories/{id}/delete", s.deleteCategory)

	mux.HandleFunc("POST /admin/categories", s.createCategory)
	mux.HandleFunc("GET /admin", s.adminOverview)
	mux.HandleFunc("GET /admin/site", s.adminSite)
	mux.HandleFunc("POST /admin/site", s.adminSaveSite)
	mux.HandleFunc("POST /admin/site/icon", s.adminSiteIcon)
	mux.HandleFunc("GET /admin/mail", s.adminMail)
	mux.HandleFunc("POST /admin/mail", s.adminSaveMail)
	mux.HandleFunc("POST /admin/mail/test", s.adminTestMail)
	mux.HandleFunc("GET /admin/security", s.adminSecurity)
	mux.HandleFunc("POST /admin/security", s.adminSaveSecurity)
	mux.HandleFunc("GET /admin/points", s.adminPoints)
	mux.HandleFunc("POST /admin/points/{id}/adjust", s.adminAdjustPoints)
	mux.HandleFunc("GET /admin/shop", s.adminShop)
	mux.HandleFunc("POST /admin/shop", s.adminNewShopItem)
	mux.HandleFunc("POST /admin/shop/{id}/edit", s.adminEditShopItem)
	mux.HandleFunc("POST /admin/shop/{id}/toggle", s.adminToggleShopItem)
	mux.HandleFunc("POST /admin/shop/{id}/delete", s.adminDeleteShopItem)
	mux.HandleFunc("POST /admin/badges", s.adminNewBadge)
	mux.HandleFunc("POST /admin/badges/{id}/delete", s.adminDeleteBadge)
	mux.HandleFunc("POST /admin/users/{id}/badge", s.adminGrantBadge)
	mux.HandleFunc("GET /admin/users", s.adminUsers)
	mux.HandleFunc("GET /admin/users/new", s.adminUserNewForm)
	mux.HandleFunc("POST /admin/users/new", s.adminUserNew)
	mux.HandleFunc("GET /admin/users/{id}/panel", s.adminUserPanel)
	mux.HandleFunc("GET /admin/threads", s.adminThreads)

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.Static())))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	s.startSweeper(opts)
	return s.recoverMW(s.loadUserMW(s.csrfMW(mux)))
}

// startSweeper 起一个后台巡检:红包超时退回 + 抽奖定点开奖。
// 进程内 goroutine 而不是外部 cron —— 这是个单二进制应用,没有别的调度器可用;
// 生命周期跟进程一致,所以不带 ctx 也不会泄漏。启动时先扫一次,补上停机期间到点的。
func (s *Server) startSweeper(opts Options) {
	ttl, every := opts.redpackTTL(), opts.sweepEvery()
	go func() {
		s.sweepOnce(ttl)
		for range time.Tick(every) {
			s.sweepOnce(ttl)
		}
	}()
}

func (s *Server) sweepOnce(ttl time.Duration) {
	s.sweepRedpacks(ttl)
	s.sweepLotteries()
}

func (s *Server) sweepRedpacks(ttl time.Duration) {
	gone, err := s.store.ExpireRedpacks(time.Now().Add(-ttl).Unix())
	if err != nil {
		log.Printf("红包超时退回失败: %v", err)
		return
	}
	for _, rp := range gone {
		// 两边的会话页都要能立刻看到状态变成「已超时退回」
		s.hub.publish(rp.SenderID, "dm")
		s.hub.publish(rp.PeerID, "dm")
	}
	if len(gone) > 0 {
		log.Printf("红包超时退回 %d 笔", len(gone))
	}
}

// sweepLotteries 到点自动开奖。抽签逻辑和手动开奖共用 runDraw,
// actorID 传 0 表示系统开的(通知记在楼主名下)。
func (s *Server) sweepLotteries() {
	ids, err := s.store.DueLotteries(time.Now().Unix())
	if err != nil {
		log.Printf("定点开奖扫描失败: %v", err)
		return
	}
	for _, id := range ids {
		t, err := s.store.GetThread(id)
		if err != nil || t == nil {
			continue
		}
		lot, err := s.store.GetLottery(id)
		if err != nil || lot == nil || lot.Over() {
			continue
		}
		if err := s.runDraw(id, t.AuthorID, lot, 0); err != nil {
			log.Printf("定点开奖失败(主题 %d): %v", id, err)
			continue
		}
		log.Printf("定点开奖 主题 %d", id)
	}
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
