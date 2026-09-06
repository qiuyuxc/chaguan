// 积分:每日签到、打赏、积分明细页。
// 经验只在这里额外累加(exp_extra),等级换算仍走 profile.go 里的门槛表。
package handlers

import (
	"net/http"
	"strings"
	"time"

	"chaguan/internal/db"
	"chaguan/web"
)

const (
	// 积分常量以内部单位「分」记(1 积分 = 100 分)
	checkinPoints = 5 * db.PointScale     // 每天签到基础积分
	checkinExp    = 5                     // 签到经验(不放大)
	maxTip        = 10000 * db.PointScale // 单次打赏上限
	pointsPerPage = 20
)

// today 服务器本地日期,签到按它算「一天」。
func today() string { return time.Now().Format("2006-01-02") }

type pointsData struct {
	web.Base
	Points      int64
	Logs        []db.PointLog
	Days        int64 // 累计签到天数
	CheckedIn   bool
	Bonus       int64  // 增值服务带来的每日额外积分
	Gain        int64  // 今天签到能拿多少(基础 + 加成),按钮上直接写清
	Notice      string // 签到成功提示
	Page, Pages int
	BaseURL     string
	HasQ        bool
}

// points GET /points:我的积分与明细。
func (s *Server) points(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	page := pageParam(r)
	total, err := s.store.CountPointLogs(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	logs, err := s.store.ListPointLogs(user.ID, pointsPerPage, (page-1)*pointsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	days, err := s.store.CheckinCount(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	done, err := s.store.CheckedIn(user.ID, today())
	if err != nil {
		s.serverError(w, err)
		return
	}
	notice := ""
	switch r.URL.Query().Get("ok") {
	case "checkin":
		notice = "签到成功,积分已到账"
	case "already":
		notice = "今天已经签到过了"
	}
	s.rend.Render(w, 200, "points", pointsData{
		Base:      s.base(r, "我的积分"),
		Points:    user.Points,
		Logs:      logs,
		Days:      days,
		CheckedIn: done,
		Bonus:     user.CheckinExtra(),
		Gain:      checkinPoints + user.CheckinExtra(),
		Notice:    notice,
		Page:      page,
		Pages:     totalPages(total, pointsPerPage),
		BaseURL:   "/points",
	})
}

// checkin POST /checkin:每日签到,+积分 +经验。重复签到不报错,只是提示已签。
func (s *Server) checkin(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	gain := checkinPoints + user.CheckinExtra()
	done, err := s.store.Checkin(user.ID, today(), gain, checkinExp)
	if err != nil {
		s.serverError(w, err)
		return
	}
	target := "/points?ok=already"
	if done {
		target = "/points?ok=checkin"
	}
	// 从别处签到(如首页侧栏)回到原页面
	if next := strings.TrimSpace(r.FormValue("next")); safeNextPath(next) {
		target = next
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// tip POST /t/{id}/tip:给主题作者打赏积分,返回刷新后的反应条。
func (s *Server) tip(w http.ResponseWriter, r *http.Request) {
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
	if t.AuthorID == user.ID {
		http.Error(w, "不能打赏自己的帖子", http.StatusBadRequest)
		return
	}
	// 允许两位小数
	amount, err := db.ParsePoints(r.FormValue("amount"))
	if err != nil || amount < 1 || amount > maxTip {
		http.Error(w, "打赏积分需在 0.01–10000 之间", http.StatusBadRequest)
		return
	}
	note := "打赏《" + t.Title + "》"
	err = s.store.TransferPoints(user.ID, t.AuthorID, amount, db.PointTipOut, db.PointTipIn, note, t.ID)
	if err == db.ErrNotEnoughPoints {
		http.Error(w, "积分不足,先去签到攒点吧", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if created, err := s.store.CreateNotification(t.AuthorID, user.ID, "tip", t.ID, 0); err == nil && created {
		s.notifyPush(t.AuthorID)
	}
	s.renderReacts(w, t, user)
}
