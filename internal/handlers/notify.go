// 站内通知:回复通知 + @提及,角标轮询。
package handlers

import (
	"log"
	"net/http"
	"regexp"
	"strings"

	"bbs/internal/auth"
	"bbs/internal/db"
	"bbs/web"
)

const notifLimit = 50

var mentionRe = regexp.MustCompile(`@([^\s@,，。!！?？;；:：/]+)`)

type notifyData struct {
	web.Base
	Notifications []db.Notification
}

// notifications GET /notifications:通知列表,打开即全部已读。
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
	if err := s.store.MarkNotificationsRead(user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "notifications", notifyData{
		Base:          base(r, "通知"),
		Notifications: list,
	})
}

// unreadCount GET /notifications/unread:未读数 JSON,前端 30s 轮询。
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
	writeJSON(w, map[string]int64{"unread": n})
}

// notifyReply 回复落库后给主题作者与 @提及用户发通知(失败仅记日志)。
func (s *Server) notifyReply(actorID int64, t *db.Thread, postID int64, content string) {
	if t.AuthorID != actorID {
		if err := s.store.CreateNotification(t.AuthorID, actorID, "reply", t.ID, postID); err != nil {
			log.Printf("notify reply: %v", err)
		}
	}
	for _, uid := range s.mentionTargets(content, t.AuthorID, actorID) {
		if err := s.store.CreateNotification(uid, actorID, "mention", t.ID, postID); err != nil {
			log.Printf("notify mention: %v", err)
		}
	}
}

// mentionTargets 解析 @用户名;作者已被「回复」通知覆盖,不再重复发 mention。
func (s *Server) mentionTargets(content string, threadAuthorID, actorID int64) []int64 {
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
		if uid != actorID && uid != threadAuthorID {
			out = append(out, uid)
		}
	}
	return out
}
