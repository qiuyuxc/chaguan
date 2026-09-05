// 个人中心设置(头像菜单 →「设置」):当前只有通知偏好,后续设置项在此页续接。
package handlers

import (
	"net/http"
	"strings"

	"chaguan/internal/db"
	"chaguan/web"
)

type settingsData struct {
	web.Base
	Scope string // all | reply | mention | none
	DM    bool   // 私信实时提醒(关=免打扰)
	Saved bool
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	s.rend.Render(w, 200, "settings", settingsData{
		Base:  s.base(r, "设置"),
		Scope: db.ValidNotifyScope(user.NotifyScope),
		DM:    user.NotifyDM,
		Saved: r.URL.Query().Get("ok") == "1",
	})
}

// saveNotifySettings POST /settings/notify:保存接收范围与私信提醒。
// 通知一律实时推送,不再提供「接收频率」这档设置。
func (s *Server) saveNotifySettings(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	scope := strings.TrimSpace(r.FormValue("scope"))
	// 表单里没带 dm 字段时保持原值:设置接口只改提交上来的项,
	// 不让一次不完整的提交把用户的私信提醒静默关掉。
	dm := user.NotifyDM
	if v := strings.TrimSpace(r.FormValue("dm")); v != "" {
		dm = v == "1"
	}
	if err := s.store.SetNotifyPrefs(user.ID, scope, 0, dm); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/settings?ok=1", http.StatusSeeOther)
}
