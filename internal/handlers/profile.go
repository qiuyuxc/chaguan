// 用户资料页与资料编辑(简介 + 头像)。
package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

const maxBioLen = 200

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
	IsAdminViewer bool
	CertText      string // 认证信息(管理员 / 版主+管辖版块),空则不展示
	Page, Pages   int
	BaseURL       string
	HasQ          bool
	Count         int64
}

const profileItemsPerPage = 15

// 等级经验规则:发主题 +12、回复 +3、收到点赞 +1(后续互动扩展在此累加)。
func socialExp(threads, replies, liked int64) int64 {
	return threads*12 + replies*3 + liked
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

// levelInfo 求最终等级与经验条区间:管理员手动覆盖(0..6)优先,
// 否则按经验自动;区间从最终等级对应的门槛起算。
func levelInfo(exp int64, override sql.NullInt64) (lv int, start, next int64) {
	lv = levelOf(exp)
	if override.Valid && override.Int64 >= 0 && override.Int64 < int64(len(levelThresholds)) {
		lv = int(override.Int64)
	}
	start = levelThresholds[lv]
	next = levelThresholds[len(levelThresholds)-1]
	if lv < len(levelThresholds)-1 {
		next = levelThresholds[lv+1]
	}
	return lv, start, next
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
	Name      string // 修改后的账号名称(校验失败时保留输入)
	Bio       string
	BadgeMode string // follow | hide | custom
	BadgeText string
	AvatarNew string // 刚上传、尚未保存的新头像路径(校验失败后保留选择)
	Error     string
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
	exp := socialExp(threads, replies, stats.Liked)
	level, expStart, expNext := levelInfo(exp, u.LevelOverride)
	expPct := 100
	if expNext > expStart {
		expPct = int((exp - expStart) * 100 / (expNext - expStart))
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
		Exp:           exp,
		Level:         level,
		ExpStart:      expStart,
		ExpNext:       expNext,
		ExpPct:        expPct,
		IsSelf:        viewer != nil && viewer.ID == u.ID,
		IsAdminViewer: viewer != nil && viewer.IsAdmin(),
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

// badgeState 把用户当前称号拆成编辑表单的状态。
func badgeState(u *db.User) (mode, text string) {
	if u == nil || !u.BadgeText.Valid {
		return "follow", ""
	}
	if u.BadgeText.String == "" {
		return "hide", ""
	}
	return "custom", u.BadgeText.String
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
	mode, text := badgeState(u)
	s.rend.Render(w, 200, "edit_profile", editProfileData{
		Base:      s.base(r, "编辑资料"),
		Profile:   u,
		Name:      u.Name,
		Bio:       u.Bio,
		BadgeMode: mode,
		BadgeText: text,
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
	badgeMode := r.FormValue("badge_mode")
	badgeText := strings.TrimSpace(r.FormValue("badge_text"))
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
			s.rend.Render(w, 200, "edit_profile", editProfileData{
				Base:      s.base(r, "编辑资料"),
				Profile:   u,
				Name:      name,
				Bio:       bio,
				BadgeMode: badgeMode,
				BadgeText: badgeText,
				Error:     "头像无效,请重新选择",
			})
			return
		}
		avatarPath = avatarURL
	}
	if avatarPath != u.AvatarPath {
		pendingAvatar = avatarPath
	}
	fail := func(msg string) {
		s.rend.Render(w, 200, "edit_profile", editProfileData{
			Base:      s.base(r, "编辑资料"),
			Profile:   u,
			Name:      name,
			Bio:       bio,
			BadgeMode: badgeMode,
			BadgeText: badgeText,
			AvatarNew: pendingAvatar,
			Error:     msg,
		})
	}
	if name != u.Name {
		switch {
		case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 24:
			fail("账号名称需要 2–24 个字符")
			return
		case strings.ContainsAny(name, " \t\r\n@/"):
			fail("账号名称不能包含空格、@ 或斜杠")
			return
		}
	}
	if utf8.RuneCountInString(bio) > maxBioLen {
		fail("简介最多 200 字")
		return
	}
	if utf8.RuneCountInString(badgeText) > 12 {
		fail("自定义称号最多 12 个字符")
		return
	}
	if name != u.Name {
		if clash, _ := s.store.GetUserByName(name); clash != nil && clash.ID != u.ID {
			fail("用户名已被占用")
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
		if err := s.store.UpdateUserName(u.ID, name); err != nil {
			if err == db.ErrDuplicateName {
				fail("用户名已被占用")
			} else {
				s.serverError(w, err)
			}
			return
		}
	}
	badge := sql.NullString{}
	switch badgeMode {
	case "custom":
		badge = sql.NullString{String: badgeText, Valid: true}
	case "hide":
		badge = sql.NullString{String: "", Valid: true}
	}
	if badge.Valid != u.BadgeText.Valid || badge.String != u.BadgeText.String {
		if err := s.store.UpdateUserBadge(u.ID, badge); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/u/"+strconv.FormatInt(u.ID, 10), http.StatusSeeOther)
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
