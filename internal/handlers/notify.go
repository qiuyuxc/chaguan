// 站内通知:回复通知 + @提及,角标轮询。
package handlers

import (
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"chaguan/internal/auth"
	"chaguan/internal/db"
	"chaguan/web"
)

const notifLimit = 50

var mentionRe = regexp.MustCompile(`@([^\s@,，。!！?？;；:：/]+)`)

type notifyData struct {
	web.Base
	Notifications []db.Notification
	Unread        int64
}

// notifications GET /notifications:通知列表(未读高亮,点击条目或「全部已读」时落已读)。
func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	list, err := s.store.ListNotifications(user.ID, notifLimit)
	if err != nil {
		s.serverError(w, err)
		return
	}
	unread, err := s.store.CountUnreadNotifications(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "notifications", notifyData{
		Base:          s.base(r, "通知"),
		Notifications: list,
		Unread:        unread,
	})
}

// notificationsReadAll POST /notifications/read-all:一键全部已读。
func (s *Server) notificationsReadAll(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	if err := s.store.MarkNotificationsRead(user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// notificationRead POST /notifications/{id}/read:单条标为已读(幂等)。
func (s *Server) notificationRead(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.MarkNotificationRead(user.ID, id); err != nil {
		s.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unreadCount GET /notifications/unread:未读数 JSON(通知 + 私信),
// 前端拿它同时更新铃铛与私信两个角标。
func (s *Server) unreadCount(w http.ResponseWriter, r *http.Request) {
	info := auth.From(r.Context())
	n := int64(0)
	if info.User != nil {
		var err error
		if n, err = s.store.CountUnreadNotifications(info.User.ID); err != nil {
			s.serverError(w, err)
			return
		}
	}
	writeJSON(w, map[string]int64{"unread": n, "dm": s.dmUnread(info.User)})
}

// notifyReply 回复落库后给主题作者与 @提及用户发通知(失败仅记日志)。
// 接收范围由 store.CreateNotification 统一过滤,真的落库了才推实时信号。
func (s *Server) notifyReply(actorID int64, t *db.Thread, postID int64, content string) {
	replySent := false
	if t.AuthorID != actorID {
		created, err := s.store.CreateNotification(t.AuthorID, actorID, "reply", t.ID, postID)
		if err != nil {
			log.Printf("notify reply: %v", err)
		} else if created {
			s.notifyPush(t.AuthorID)
			replySent = true
		}
	}
	// 作者已收到「回复」通知时不再重复发 mention;若那条通知被其接收范围
	// 过滤掉了(如只收 @提及),这里仍要把 @ 补给作者。
	var skip int64
	if replySent {
		skip = t.AuthorID
	}
	for _, uid := range s.mentionTargets(content, skip, actorID) {
		created, err := s.store.CreateNotification(uid, actorID, "mention", t.ID, postID)
		if err != nil {
			log.Printf("notify mention: %v", err)
		} else if created {
			s.notifyPush(uid)
		}
	}
}

// mentionTargets 解析 @用户名;skipUserID 与操作者本人不在结果内。
func (s *Server) mentionTargets(content string, skipUserID, actorID int64) []int64 {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if name := strings.TrimSpace(m[1]); name != "" {
			names = append(names, name)
		}
	}
	ids, err := s.store.UserIDsByName(names)
	if err != nil {
		log.Printf("mention lookup: %v", err)
		return nil
	}
	var out []int64
	for _, uid := range ids {
		if uid != actorID && uid != skipUserID {
			out = append(out, uid)
		}
	}
	return out
}
