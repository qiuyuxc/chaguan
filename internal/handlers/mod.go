// 版主/管理员操作:置顶、锁定,以及用户管理(角色/封禁)。
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
)

// togglePin POST /t/{id}/pin:版主/管理员置顶或取消置顶。
func (s *Server) togglePin(w http.ResponseWriter, r *http.Request) {
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
	if !user.IsAdmin() {
		mod, err := s.store.IsModOf(user.ID, t.CategoryID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !mod {
			http.Error(w, "仅该版块的版主/管理员可置顶", http.StatusForbidden)
			return
		}
	}
	if err := s.store.SetThreadPinned(t.ID, !t.IsPinned); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/t/"+strconv.FormatInt(t.ID, 10))
}

// toggleLock POST /t/{id}/lock:版主/管理员锁定或解锁主题。
func (s *Server) toggleLock(w http.ResponseWriter, r *http.Request) {
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
	if !user.IsAdmin() {
		mod, err := s.store.IsModOf(user.ID, t.CategoryID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !mod {
			http.Error(w, "仅该版块的版主/管理员可锁定", http.StatusForbidden)
			return
		}
	}
	if err := s.store.SetThreadLocked(t.ID, !t.IsLocked); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/t/"+strconv.FormatInt(t.ID, 10))
}

// adminTarget 校验管理员身份并返回目标用户(不允许对自身操作)。
func (s *Server) adminTarget(w http.ResponseWriter, r *http.Request) (*db.User, bool) {
	viewer := s.currentUser(w, r)
	if viewer == nil {
		return nil, false
	}
	if !viewer.IsAdmin() {
		http.Error(w, "仅管理员可管理用户", http.StatusForbidden)
		return nil, false
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return nil, false
	}
	target, err := s.store.GetUserByID(id)
	if err != nil {
		s.serverError(w, err)
		return nil, false
	}
	if target == nil {
		http.NotFound(w, r)
		return nil, false
	}
	if target.ID == viewer.ID {
		http.Error(w, "不能对自己执行该操作", http.StatusBadRequest)
		return nil, false
	}
	return target, true
}

// setUserRole POST /admin/users/{id}/role:
// role=user → 撤销版主;role=mod 必须带 category 指定管辖版块。
func (s *Server) setUserRole(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if target.Role == "admin" {
		http.Error(w, "不能修改管理员的角色", http.StatusBadRequest)
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	switch role {
	case "user":
		if err := s.store.DemoteMod(target.ID); err != nil {
			s.serverError(w, err)
			return
		}
	case "mod":
		catID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("category")), 10, 64)
		if err != nil || catID < 1 {
			http.Error(w, "请选择要管辖的版块", http.StatusBadRequest)
			return
		}
		cat, err := s.store.GetCategoryByID(catID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if cat == nil {
			http.Error(w, "版块不存在", http.StatusBadRequest)
			return
		}
		if err := s.store.AddModCategory(target.ID, catID); err != nil {
			s.serverError(w, err)
			return
		}
	default:
		http.Error(w, "非法角色", http.StatusBadRequest)
		return
	}
	s.redirectAfter(w, r, "/admin/users")
}

// adminEditUser POST /admin/users/{id}/edit:后台编辑基础资料(名称/简介/称号标签),
// 可选重置密码(留空不改)。与本人资料页同一套校验规则。
func (s *Server) adminEditUser(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = target.Name
	}
	bio := strings.TrimSpace(r.FormValue("bio"))
	badgeMode := r.FormValue("badge_mode")
	badgeText := strings.TrimSpace(r.FormValue("badge_text"))
	password := r.FormValue("password")

	fail := func(msg string) {
		http.Error(w, msg, http.StatusBadRequest)
	}
	if name != target.Name {
		switch {
		case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 24:
			fail("账号名称需要 2–24 个字符")
			return
		case strings.ContainsAny(name, " \t\r\n@/"):
			fail("账号名称不能包含空格、@ 或斜杠")
			return
		}
		if clash, _ := s.store.GetUserByName(name); clash != nil && clash.ID != target.ID {
			fail("用户名已被占用")
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
	if password != "" {
		if utf8.RuneCountInString(password) < 8 {
			fail("新密码至少 8 位")
			return
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if err := s.store.UpdateUserPassword(target.ID, hash); err != nil {
			s.serverError(w, err)
			return
		}
	}

	if name != target.Name {
		if err := s.store.UpdateUserName(target.ID, name); err != nil {
			if err == db.ErrDuplicateName {
				fail("用户名已被占用")
			} else {
				s.serverError(w, err)
			}
			return
		}
	}
	if bio != target.Bio {
		if err := s.store.UpdateUserBio(target.ID, bio); err != nil {
			s.serverError(w, err)
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
	if badge.Valid != target.BadgeText.Valid || badge.String != target.BadgeText.String {
		if err := s.store.UpdateUserBadge(target.ID, badge); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.redirectAfter(w, r, "/admin/users")
}

// adminSetAvatar POST /admin/users/{id}/avatar:后台直接换头像(multipart field: avatar)。
func (s *Server) adminSetAvatar(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(maxImageBytes + 1<<20); err != nil {
		http.Error(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if len(r.MultipartForm.File["avatar"]) == 0 {
		http.Error(w, "请选择图片", http.StatusBadRequest)
		return
	}
	uploadID, ok := s.saveImageUpload(w, r, "avatar", target.ID)
	if !ok {
		return
	}
	newPath := "/uploads/" + strconv.FormatInt(uploadID, 10)
	if err := s.store.UpdateUserAvatar(target.ID, newPath); err != nil {
		s.serverError(w, err)
		return
	}
	if oldID, ok := uploadPathID(target.AvatarPath); ok {
		s.removeUploadFile(oldID) // 清理旧头像,失败静默
	}
	s.redirectAfter(w, r, "/admin/users")
}

// setVerify POST /admin/users/{id}/verify:直接写入认证称号,可自定义任意文案
// (官号 / 认证作者 / 具体对象等),用于同步预览主页、帖子与回复里的 V 标与认证行。
// 空 title 表示清除:普通用户回到无认证,管理员/版主回到按身份自动认证。
func (s *Server) setVerify(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if runes := []rune(title); len(runes) > 20 {
		http.Error(w, "认证称号最多 20 字", http.StatusBadRequest)
		return
	}
	if err := s.store.SetVerifyTitle(target.ID, title); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/admin/users")
}

// banUser POST /admin/users/{id}/ban:按天数封禁(days: 1–3650,10 年约等于永久)。
func (s *Server) banUser(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if target.Role == "admin" {
		http.Error(w, "不能封禁管理员", http.StatusBadRequest)
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("days")))
	if err != nil || days < 1 || days > 3650 {
		http.Error(w, "封禁天数需在 1–3650 之间", http.StatusBadRequest)
		return
	}
	until := time.Now().Unix() + int64(days)*86400
	if err := s.store.BanUser(target.ID, until); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/admin/users")
}

// unbanUser POST /admin/users/{id}/unban:解除封禁。
func (s *Server) unbanUser(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if err := s.store.UnbanUser(target.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/admin/users")
}
