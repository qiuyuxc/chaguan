// 用户资料页与资料编辑(简介 + 头像)。
package handlers

import (
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
	Activity      []db.UserActivity
	Tab           string // topics | replies | posts
	Threads       int64
	Replies       int64
	Posts         int64
	IsSelf        bool
	IsAdminViewer bool
	Page, Pages   int
	BaseURL       string
	HasQ          bool
	Count         int64
}

const profileItemsPerPage = 15

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
	Bio       string
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
	tab := r.URL.Query().Get("tab")
	switch tab {
	case "replies", "posts":
	default:
		tab = "topics"
	}
	page := pageParam(r)
	offset := (page - 1) * profileItemsPerPage
	var topics []db.Thread
	var activity []db.UserActivity
	var total int64
	switch tab {
	case "replies":
		activity, err = s.store.ListUserReplies(u.ID, profileItemsPerPage, offset)
		total = replies
	case "posts":
		activity, err = s.store.ListUserActivity(u.ID, profileItemsPerPage, offset)
		total = threads + replies
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
	for i := range activity {
		activity[i].Snippet = clipRunes(activity[i].Snippet, 90)
	}
	s.rend.Render(w, 200, "profile", profileData{
		Base:          s.base(r, u.Name+" 的资料"),
		Profile:       u,
		Topics:        topics,
		Activity:      activity,
		Tab:           tab,
		Threads:       threads,
		Replies:       replies,
		Posts:         threads + replies,
		IsSelf:        viewer != nil && viewer.ID == u.ID,
		IsAdminViewer: viewer != nil && viewer.IsAdmin(),
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
	s.rend.Render(w, 200, "edit_profile", editProfileData{
		Base:    s.base(r, "编辑资料"),
		Profile: u,
		Bio:     u.Bio,
	})
}

// editProfile POST /u/{id}/edit:保存简介,可选更换头像。
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
			s.rend.Render(w, 200, "edit_profile", editProfileData{
				Base:    s.base(r, "编辑资料"),
				Profile: u,
				Bio:     bio,
				Error:   "头像无效,请重新选择",
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
			Bio:       bio,
			AvatarNew: pendingAvatar,
			Error:     msg,
		})
	}
	if utf8.RuneCountInString(bio) > maxBioLen {
		fail("简介最多 200 字")
		return
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
