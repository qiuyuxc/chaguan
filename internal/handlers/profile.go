// 用户资料页与资料编辑(简介 + 头像)。
package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

const (
	maxBioLen = 200
	// 改显示名收费:显示名全站唯一且到处都在展示,收点积分挡住随手改名
	renameCost = 3 * db.PointScale
)

type profileData struct {
	web.Base
	Profile       *db.User
	Topics        []db.Thread
	Tab           string // posts | likes | favorites
	Threads       int64
	Replies       int64
	Posts         int64
	Following     int64            // 关注
	Followers     int64            // 粉丝
	Liked         int64            // 获赞(收到的赞)
	LikeTotal     int64            // 导航计数:我点赞过的文章总数
	FavTotal      int64            // 导航计数:我收藏的主题总数
	LikedThreads  []db.LikedThread // 「点赞」分区:我点赞过的文章
	FavThreads    []db.FavThread   // 「收藏」分区:我收藏的主题
	Exp           int64            // 等级经验
	Level         int              // LV0..LV6
	ExpStart      int64            // 当前等级起始经验
	ExpNext       int64            // 升下一级所需经验(LV6 时等于 ExpStart,经验条满)
	ExpPct        int              // 经验条百分比 0..100
	IsSelf        bool
	CanDM         bool   // 显示「发私信」:已登录且不是自己
	IsBanned      bool   // 账号当前封禁中(已过期的 banned_until 不算)
	BanUntil      int64  // 解封时间(IsBanned 为真时有效)
	CertText      string // 认证信息(管理员 / 版主+管辖版块),空则不展示
	Page, Pages   int
	BaseURL       string
	HasQ          bool
	Count         int64
}

const profileItemsPerPage = 15

// banState 判断账号当前是否处于封禁中。banned_until 落在过去只是历史记录,
// 会话中间件也已按同样口径放行,不应再显示为封禁。
func banState(u *db.User) (banned bool, until int64) {
	if u == nil || !u.BannedUntil.Valid || u.BannedUntil.Int64 <= time.Now().Unix() {
		return false, 0
	}
	return true, u.BannedUntil.Int64
}

// 等级经验规则:发主题 +12、回复 +3、收到点赞 +1,再加上签到/活动/商城送的经验。
func socialExp(threads, replies, liked, extra int64) int64 {
	return threads*12 + replies*3 + liked + extra
}

// levelThresholds 仿 B 站成长曲线(简化):下标即等级 LV0..LV6。
var levelThresholds = [...]int64{0, 60, 250, 800, 2200, 6000, 16000}

// levelOf 由经验求等级(等级进度区间另见 levelInfo)。
func levelOf(exp int64) int {
	lv := 0
	for i := 1; i < len(levelThresholds); i++ {
		if exp >= levelThresholds[i] {
			lv = i
		} else {
			break
		}
	}
	return lv
}

// levelInfo 求最终等级与展示经验:
// 管理员手动指定等级时,经验按“该级起点”兜底(真实经验低于门槛就补到门槛),
// LV6 顶级固定满经验 16000(顶级不再往上,显示即满);自动升级按真实经验展示。
func levelInfo(exp int64, override sql.NullInt64) (lv int, shown, start, next int64) {
	lv = levelOf(exp)
	manual := false
	if override.Valid && override.Int64 >= 0 && override.Int64 < int64(len(levelThresholds)) {
		lv = int(override.Int64)
		manual = true
	}
	start = levelThresholds[lv]
	next = levelThresholds[len(levelThresholds)-1]
	if lv < len(levelThresholds)-1 {
		next = levelThresholds[lv+1]
	}
	shown = exp
	if manual {
		shown = start
		if lv == 6 {
			shown = start // 顶级:满 16000,不显示真实累计
		} else if exp > start {
			shown = exp
		}
	}
	return lv, shown, start, next
}

// clipRunes 给正文预览截断到 n 个字符。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

type editProfileData struct {
	web.Base
	Profile   *db.User
	Name      string     // 修改后的账号名称(校验失败时保留输入)
	Bio       string
	Badges    []db.Badge // 我持有的勋章(供选择佩戴)
	BadgeMode string     // follow | hide | wear
	WornID    int64      // 正佩戴的勋章 id
	AvatarNew string     // 刚上传、尚未保存的新头像路径(校验失败后保留选择)
	Error     string
}

// badgeState 把用户当前的标签状态拆成编辑表单的选项。
// 勋章改为发放/兑换后,用户只能在「跟随身份 / 隐藏 / 佩戴某枚勋章」之间选。
func badgeState(u *db.User) (mode string, wornID int64) {
	if u == nil || !u.BadgeText.Valid {
		return "follow", 0
	}
	if u.BadgeText.String == "" {
		return "hide", 0
	}
	if u.BadgeID.Valid {
		return "wear", u.BadgeID.Int64
	}
	return "follow", 0
}

// profile GET /u/{id}:公开资料页。
func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, err := s.store.GetUserByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if u == nil {
		http.NotFound(w, r)
		return
	}
	threads, err := s.store.CountUserThreads(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	replies, err := s.store.CountUserReplies(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	stats, err := s.store.SocialStats(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// 等级经验只看真实获赞,后台覆盖的展示获赞不参与,避免经验被手动数值带飞
	likedReal, err := s.store.LikesReceived(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	exp := socialExp(threads, replies, likedReal, u.ExpExtra)
	level, expShown, expStart, expNext := levelInfo(exp, u.LevelOverride)
	expPct := 100
	if expNext > expStart {
		expPct = int((expShown - expStart) * 100 / (expNext - expStart))
		if expPct > 100 {
			expPct = 100
		}
	}
	tab := r.URL.Query().Get("tab")
	likeTotal, err := s.store.CountLikedThreads(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	favTotal, err := s.store.CountFavThreads(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	switch tab {
	case "likes", "favorites":
	default:
		tab = "posts"
	}
	page := pageParam(r)
	offset := (page - 1) * profileItemsPerPage
	var topics []db.Thread
	var likedThreads []db.LikedThread
	var favThreads []db.FavThread
	var total int64
	switch tab {
	case "likes":
		likedThreads, err = s.store.ListLikedThreads(u.ID, profileItemsPerPage, offset)
		total = likeTotal
	case "favorites":
		favThreads, err = s.store.ListFavThreads(u.ID, profileItemsPerPage, offset)
		total = favTotal
	default:
		topics, err = s.store.ListUserThreads(u.ID, profileItemsPerPage, offset)
		total = threads
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	viewer := auth.From(r.Context()).User
	baseURL := "/u/" + strconv.FormatInt(u.ID, 10) + "?tab=" + tab
	title := u.Name + " 的资料"
	if viewer != nil && viewer.ID == u.ID {
		title = "我的资料"
	}
	cert := userCert(u, s.store)
	banned, banUntil := banState(u)
	s.rend.Render(w, 200, "profile", profileData{
		Base:          s.base(r, title),
		Profile:       u,
		Topics:        topics,
		LikedThreads:  likedThreads,
		FavThreads:    favThreads,
		Tab:           tab,
		Threads:       threads,
		Replies:       replies,
		Posts:         threads + replies,
		Following:     stats.Following,
		Followers:     stats.Followers,
		Liked:         stats.Liked,
		LikeTotal:     likeTotal,
		FavTotal:      favTotal,
		Exp:           expShown,
		Level:         level,
		ExpStart:      expStart,
		ExpNext:       expNext,
		ExpPct:        expPct,
		IsSelf:        viewer != nil && viewer.ID == u.ID,
		CanDM:         viewer != nil && viewer.ID != u.ID,
		IsBanned:      banned,
		BanUntil:      banUntil,
		CertText:      cert,
		Page:          page,
		Pages:         totalPages(total, profileItemsPerPage),
		BaseURL:       baseURL,
		HasQ:          true,
		Count:         total,
	})
}

func (s *Server) selfUser(w http.ResponseWriter, r *http.Request) *db.User {
	user := s.currentUser(w, r)
	if user == nil {
		return nil
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return nil
	}
	if user.ID != id {
		http.Error(w, "只能编辑自己的资料", http.StatusForbidden)
		return nil
	}
	return user
}

// editProfileForm GET /u/{id}/edit:本人资料编辑页。
func (s *Server) editProfileForm(w http.ResponseWriter, r *http.Request) {
	if s.selfUser(w, r) == nil {
		return
	}
	id, _ := pathID(r, "id")
	u, err := s.store.GetUserByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	mode, wornID := badgeState(u)
	badges, err := s.store.UserBadges(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "edit_profile", editProfileData{
		Base:      s.base(r, "编辑资料"),
		Profile:   u,
		Name:      u.Name,
		Bio:       u.Bio,
		Badges:    badges,
		BadgeMode: mode,
		WornID:    wornID,
	})
}

// editProfile POST /u/{id}/edit:保存账号名称/简介/称号标签,可选更换头像。
func (s *Server) editProfile(w http.ResponseWriter, r *http.Request) {
	user := s.selfUser(w, r)
	if user == nil {
		return
	}
	id, _ := pathID(r, "id")
	u, err := s.store.GetUserByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxImageBytes + 1<<20); err != nil {
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
	} else {
		r.ParseForm()
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = u.Name
	}
	bio := strings.TrimSpace(r.FormValue("bio"))
	avatarURL := strings.TrimSpace(r.FormValue("avatar_url"))
	avatarPath := u.AvatarPath
	pendingAvatar := ""
	if r.MultipartForm != nil && len(r.MultipartForm.File["avatar"]) > 0 {
		// 兼容旧版:直接以表单文件换头像
		uploadID, ok := s.saveImageUpload(w, r, "avatar", user.ID)
		if !ok {
			return
		}
		avatarPath = "/uploads/" + strconv.FormatInt(uploadID, 10)
	} else if avatarURL != "" && avatarURL != u.AvatarPath {
		if !s.validAvatarUpload(avatarURL, user.ID) {
			s.renderEditProfile(w, r, u, name, bio, "", "头像无效,请重新选择")
			return
		}
		avatarPath = avatarURL
	}
	if avatarPath != u.AvatarPath {
		pendingAvatar = avatarPath
	}
	fail := func(msg string) {
		s.renderEditProfile(w, r, u, name, bio, pendingAvatar, msg)
	}
	if name != u.Name {
		switch {
		case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 24:
			fail("显示名需要 2–24 个字符")
			return
		case strings.ContainsAny(name, " \t\r\n@/"):
			fail("显示名不能包含空格、@ 或斜杠")
			return
		}
	}
	if utf8.RuneCountInString(bio) > maxBioLen {
		fail("简介最多 200 字")
		return
	}
	if name != u.Name {
		if taken, _ := s.store.NameTaken(u.ID, name); taken {
			fail("这个显示名已被占用")
			return
		}
	}

	if err := s.store.UpdateUserBio(u.ID, bio); err != nil {
		s.serverError(w, err)
		return
	}
	if avatarPath != u.AvatarPath {
		if err := s.store.UpdateUserAvatar(u.ID, avatarPath); err != nil {
			s.serverError(w, err)
			return
		}
		if oldID, ok := uploadPathID(u.AvatarPath); ok {
			s.removeUploadFile(oldID) // 清理旧头像,失败静默
		}
	}
	if name != u.Name {
		// 改名与扣分在一个事务里:余额不够不改名,改名失败不扣分
		err := s.store.RenameDisplayName(u.ID, name, renameCost)
		switch err {
		case nil:
		case db.ErrNotEnoughPoints:
			fail("修改显示名需要 3 积分,你的余额不够")
			return
		case db.ErrDuplicateName:
			fail("这个显示名已被占用")
			return
		default:
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/u/"+strconv.FormatInt(u.ID, 10), http.StatusSeeOther)
}

// renderEditProfile 回填编辑资料表单(校验失败时用),勋章状态按库里的当前值。
func (s *Server) renderEditProfile(w http.ResponseWriter, r *http.Request, u *db.User,
	name, bio, pendingAvatar, errMsg string) {
	mode, wornID := badgeState(u)
	badges, err := s.store.UserBadges(u.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "edit_profile", editProfileData{
		Base:      s.base(r, "编辑资料"),
		Profile:   u,
		Name:      name,
		Bio:       bio,
		Badges:    badges,
		BadgeMode: mode,
		WornID:    wornID,
		AvatarNew: pendingAvatar,
		Error:     errMsg,
	})
}

// validAvatarUpload 校验头像路径是本人生成过的上传记录。
func (s *Server) validAvatarUpload(path string, userID int64) bool {
	id, ok := uploadPathID(path)
	if !ok {
		return false
	}
	u, err := s.store.GetUpload(id)
	return err == nil && u != nil && u.UserID == userID
}

// userSearch GET /api/users?q=…:登录用户的 @ 提及搜索(按名称模糊匹配)。
func (s *Server) userSearch(w http.ResponseWriter, r *http.Request) {
	if auth.From(r.Context()).User == nil {
		http.Error(w, "请先登录", http.StatusUnauthorized)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if runes := []rune(q); len(runes) > 30 {
		q = string(runes[:30])
	}
	if q == "" {
		writeJSON(w, []db.UserSearch{})
		return
	}
	users, err := s.store.SearchUsers(q, 8)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, users)
}

// userCert 生成资料页认证行文案:管理员全站认证;版主列出管辖版块。
func userCert(u *db.User, store *db.Store) string {
	if u == nil {
		return ""
	}
	if u.VerifyTitle.Valid && strings.TrimSpace(u.VerifyTitle.String) != "" {
		return strings.TrimSpace(u.VerifyTitle.String)
	}
	if u.VerifyKind.Valid {
		switch strings.TrimSpace(u.VerifyKind.String) {
		case "官号":
			return "官方"
		case "认证作者":
			return "作者"
		}
		if strings.TrimSpace(u.VerifyKind.String) != "" {
			return strings.TrimSpace(u.VerifyKind.String)
		}
	}
	switch u.Role {
	case "admin":
		return "管理员认证"
	case "mod":
		cats, err := store.ModCategories(u.ID)
		if err != nil || len(cats) == 0 {
			return "版主认证"
		}
		names := make([]string, 0, len(cats))
		for _, c := range cats {
			names = append(names, c.Name)
		}
		return "版主认证 · " + strings.Join(names, "、")
	}
	return ""
}
