// 版主/管理员操作:置顶、锁定,以及用户管理(角色/封禁)。
package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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

// setVerify POST /admin/users/{id}/verify:为普通成员设置认证称号(官号/认证作者等),
// 空 title 表示取消认证。管理员/版主按身份自动认证,不走此字段。
func (s *Server) setVerify(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	if target.Role != "user" {
		http.Error(w, "管理员/版主已按身份自动认证", http.StatusBadRequest)
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
