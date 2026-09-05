// Package web 内嵌并渲染全部模板与静态资源。
package web

import (
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"strconv"
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
	Title        string
	User         *db.User // 可能为 nil(未登录)
	CSRF         string
	Categories   []db.Category // 抽屉/侧栏导航
	Members      int64         // 社区统计
	TotalThreads int64         // 社区统计(主题总数;避免与各页 Threads 列表字段重名)
	Exp          int64         // 登录用户等级经验(侧栏简略显示;未登录为 0)
	Level        int           // 登录用户 LV0..LV6
	ExpNext      int64         // 登录用户下一级所需经验(LV6 时经验条满)
	ExpPct       int           // 登录用户当前等级经验进度 0..100
	Site         db.Site       // 站点信息(品牌/页脚/图标/公告),后台可配
	Points       int64         // 登录用户的积分余额(侧栏签到卡)
	CheckedIn    bool          // 登录用户今天是否已签到
}

// PostView 是帖子 partial 的数据:帖子 + 当前查看者(决定是否显示删除按钮)。
type PostView struct {
	Post      db.Post
	Viewer    *db.User
	Floor     int64 // 楼层(首帖为 1)
	IsOP      bool  // 是否为楼主本人(回复区显示「楼主」徽章)
	LikeCount int64 // 该楼获赞(回复点赞;不进「我的点赞」列表)
	LikedByMe bool  // 我是否赞过该楼
	CanDelete bool  // 该查看者可删此回复:作者本人 / 管理员 / 管辖该版块的版主
}

// DMView 是私信气泡的数据:消息 + 是否我发的 + 对方信息(渲染头像用)。
type DMView struct {
	Msg  db.DMMessage
	Mine bool
	Peer *db.DMConversation
}

var funcs = template.FuncMap{
	"relTime": RelTime,
	"csrfField": func(token string) template.HTML {
		return template.HTML(`<input type="hidden" name="_csrf" value="` + token + `">`)
	},
	"initial":   Initial,
	"catColor":  func(i int) string { return "c" + strconv.Itoa(i%6+1) },
	"avColor":   func(id int64) string { return "c" + strconv.Itoa(int(id)%6+1) },
	"bcatColor": func(id int64) string { return "bcat-" + strconv.Itoa(int(id)%6+1) },
	"date":      func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02") },
	// relTime 只讲得清过去;定点开奖这类未来时间要给绝对值
	"absTime":  func(ts int64) string { return time.Unix(ts, 0).Format("01-02 15:04") },
	"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	// otpauth:// 这类非 http 协议会被模板的 URL 过滤器拦掉,
	// 这里显式放行(链接由服务端拼装,参数都经过 url 转义)
	"safeURL": func(s string) template.URL { return template.URL(s) },
	"postView": func(p db.Post, viewer *db.User) PostView {
		return PostView{Post: p, Viewer: viewer}
	},
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			if k, ok := kv[i].(string); ok {
				m[k] = kv[i+1]
			}
		}
		return m
	},
	"pageURL": func(baseURL string, hasQ bool, page int) string {
		if page <= 1 {
			return baseURL
		}
		sep := "?"
		if hasQ {
			sep = "&"
		}
		return fmt.Sprintf("%s%spage=%d", baseURL, sep, page)
	},
	"canEditPost": canEditPost,
	"pages": func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i + 1
		}
		return out
	},
	"roleBadge":    roleBadge,
	"vBadge":       vBadge,
	"vSeal":        vSeal,
	"vColorClass":  vColorClass,
	"quotePreview": quotePreview,
	"pointKind":    pointKindName,
	// pts 渲染积分:库里存的是「分」,这里换成人看的两位小数(整数不带小数点)
	"pts": db.FormatPoints,
}

// pointKindName 把积分流水类型翻成中文,列表里直接显示。
func pointKindName(kind string) string {
	switch kind {
	case db.PointCheckin:
		return "每日签到"
	case db.PointTipOut:
		return "打赏支出"
	case db.PointTipIn:
		return "收到打赏"
	case db.PointAdmin:
		return "管理员调整"
	case db.PointUnlockOut:
		return "解锁付费帖"
	case db.PointUnlockIn:
		return "付费帖收入"
	case db.PointStake:
		return "参与抽奖"
	case db.PointWin:
		return "抽奖中奖"
	case db.PointLotFund:
		return "抽奖出奖"
	case db.PointLotBack:
		return "奖池退回"
	case db.PointShop:
		return "商城兑换"
	case db.PointRedpackOut:
		return "发出红包"
	case db.PointRedpackIn:
		return "领取红包"
	case db.PointRedpackBack:
		return "红包退回"
	case db.PointRename:
		return "修改显示名"
	default:
		return kind
	}
}

// roleBadge 渲染用户称号徽章:
// badge NULL → 跟随身份(管理员/版主);"" → 隐藏;自定义文本 → 替换身份文案。
// 自定义称号对所有人统一实心配色:同一种勋章不应因身份而异色,
// 浅灰与页面底色太接近,故用实心强调;角色配色只用于「跟随身份」的默认标签。
func roleBadge(role string, badge sql.NullString) template.HTML {
	var label, cls string
	if badge.Valid {
		if badge.String == "" {
			return ""
		}
		label, cls = badge.String, "badge badge-solid"
	} else {
		switch role {
		case "admin":
			label, cls = "管理员", "badge badge-admin"
		case "mod":
			label, cls = "版主", "badge badge-mod"
		default:
			return ""
		}
	}
	return template.HTML(`<span class="` + cls + `">` + template.HTMLEscapeString(label) + `</span>`)
}

// verifyKindName 归一化分类:官方=蓝 V、厂商=红 V、作者=黄 V;
// 兼容旧数据里的「官号」「认证作者」写法。
func verifyKindName(kind sql.NullString) string {
	if !kind.Valid {
		return ""
	}
	k := strings.TrimSpace(kind.String)
	switch k {
	case "官号":
		return "官方"
	case "认证作者":
		return "作者"
	}
	return k
}

// vLabelOf 认证文案:自定义文案(如「米哈游官方」「游戏作者」)优先,
// 其次认证分类;管理员/版主未认证时按身份;其余返回空。
func vLabelOf(role string, kind, title sql.NullString) string {
	if title.Valid && strings.TrimSpace(title.String) != "" {
		return strings.TrimSpace(title.String)
	}
	if k := verifyKindName(kind); k != "" {
		return k
	}
	switch role {
	case "admin":
		return "管理员认证"
	case "mod":
		return "版主认证"
	}
	return ""
}

// vColorClass 认证 V 的配色:官方=默认蓝,厂商=红,作者=黄;
// 旧数据无分类时按文案兜底(官号归蓝、认证作者归黄)。
func vColorClass(kind, title sql.NullString) string {
	switch verifyKindName(kind) {
	case "厂商":
		return " v-red"
	case "作者":
		return " v-yellow"
	}
	// 旧数据可能只有文案没有分类:按文案兜底配色(官号归蓝,即默认)
	if title.Valid && strings.TrimSpace(title.String) == "认证作者" {
		return " v-yellow"
	}
	return ""
}

// vBadge 渲染行内「V」认证标(资料页认证行等),配色跟随认证分类。
func vBadge(role string, kind, title sql.NullString) template.HTML {
	label := vLabelOf(role, kind, title)
	if label == "" {
		return ""
	}
	return template.HTML(`<span class="v-badge` + vColorClass(kind, title) + `" title="` +
		template.HTMLEscapeString(label) + `" aria-label="认证">V</span>`)
}

// vSeal 渲染压在作者头像上的「V」认证标(文章/回复等小头像场景);
// 无认证时返回空串,由外层相对容器(.av-frame)定位。
func vSeal(role string, kind, title sql.NullString) template.HTML {
	label := vLabelOf(role, kind, title)
	if label == "" {
		return ""
	}
	return template.HTML(`<span class="v-mark av-seal` + vColorClass(kind, title) + `" title="` +
		template.HTMLEscapeString(label) + `" aria-label="认证">V</span>`)
}

// quotePreview 取帖子「自己写的正文」压成一行并截前 120 字,作为「引用回复」的预览。
// 引用某楼时只应带上对方自己写的内容,引用行(含 `>` 嵌套引用别人更早楼层)不算数,
// 否则 3 楼引 2 楼时会把 2 楼引 1 楼的整段一起套进来,出现连环嵌套。
func quotePreview(src string) string {
	trim := func(s string) string {
		r := []rune(s)
		if len(r) > 120 {
			return string(r[:120])
		}
		return s
	}
	var own []string
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimLeft(ln, " \t"), ">") {
			continue
		}
		own = append(own, ln)
	}
	txt := strings.Join(strings.Fields(strings.Join(own, "\n")), " ")
	if txt == "" {
		txt = strings.Join(strings.Fields(src), " ") // 整楼都是引用时,回退用原文
	}
	return trim(txt)
}

// canEditPost 编辑权限:作者本人或管理员可编辑。
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
	"verify_apply", "settings", "dm_list", "dm_thread", "points", "shop",
	"forgot", "reset", "verify_resend", "account", "login_2fa",
	"admin_categories", "admin_overview", "admin_users", "admin_threads", "admin_verify", "admin_user_new", "admin_site", "admin_mail", "admin_security", "admin_points", "admin_shop", "search",
}

func NewRenderer() (*Renderer, error) {
	r := &Renderer{pages: make(map[string]*template.Template, len(pageNames))}
	for _, name := range pageNames {
		t, err := template.New("layout").Funcs(funcs).ParseFS(tmplFS,
			"templates/layout.html",
			"templates/admin_layout.html",
			"templates/partials/composer.html",
			"templates/partials/post.html",
			"templates/partials/pagination.html",
			"templates/partials/reacts.html",
			"templates/partials/thread_row.html",
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

// RenderAdmin 输出后台整页:使用独立的管理布局(自带左侧导航,不套前台版式)。
func (r *Renderer) RenderAdmin(w io.Writer, status int, name string, data any) error {
	t, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("unknown page template: %s", name)
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(status)
	}
	return t.ExecuteTemplate(w, "admin_layout", data)
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
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(status)
	}
	return tt.Execute(w, data)
}
