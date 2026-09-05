// 阅读门槛(等级 / 付费解锁)与抽奖帖。
// 门槛只挡正文与回复区,标题、作者、统计始终可见,列表页也照常出现。
package handlers

import (
	"math/rand/v2"
	"net/http"
	"strconv"

	"bbs/internal/db"
)

const lotteryEntryLimit = 200 // 名单展示上限

// threadGate 当前查看者对某主题的可见性判断。
type threadGate struct {
	OK        bool  // 可以看正文
	MyLevel   int   // 我的等级(提示用)
	NeedLevel int   // >0:等级不够,需要这个等级
	NeedPay   int64 // >0:需支付这么多积分解锁
	Paid      bool  // 已解锁过
}

// gateFor 算出门槛结果。作者本人、管理员、该版块版主一律直通。
func (s *Server) gateFor(t *db.Thread, viewer *db.User, level int, canMod bool) (threadGate, error) {
	g := threadGate{OK: true, MyLevel: level}
	if t.MinLevel == 0 && t.Price == 0 {
		return g, nil
	}
	if viewer != nil && (viewer.ID == t.AuthorID || canMod) {
		return g, nil
	}
	if viewer == nil {
		g.OK = false
		g.NeedLevel = t.MinLevel
		g.NeedPay = t.Price
		return g, nil
	}
	if t.MinLevel > 0 && level < t.MinLevel {
		g.OK, g.NeedLevel = false, t.MinLevel
	}
	if t.Price > 0 {
		unlocked, err := s.store.ThreadUnlocked(t.ID, viewer.ID)
		if err != nil {
			return g, err
		}
		g.Paid = unlocked
		if !unlocked {
			g.OK, g.NeedPay = false, t.Price
		}
	}
	return g, nil
}

// unlockThread POST /t/{id}/unlock:支付积分解锁付费帖。
func (s *Server) unlockThread(w http.ResponseWriter, r *http.Request) {
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
	if t.Price <= 0 {
		http.Error(w, "这篇不需要解锁", http.StatusBadRequest)
		return
	}
	// 等级门槛没过时先别收钱
	if t.MinLevel > 0 {
		level, err := s.userLevel(user)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if level < t.MinLevel {
			http.Error(w, "这篇需要 LV"+strconv.Itoa(t.MinLevel)+" 及以上才能阅读", http.StatusForbidden)
			return
		}
	}
	_, err = s.store.UnlockThread(t.ID, user.ID, t.AuthorID, t.Price, "解锁《"+t.Title+"》")
	if err == db.ErrNotEnoughPoints {
		http.Error(w, "积分不足,先去签到攒点吧", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/t/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
}

// userLevel 算某人的当前等级(与侧栏/资料页同一套口径)。
func (s *Server) userLevel(u *db.User) (int, error) {
	if u == nil {
		return 0, nil
	}
	threads, err := s.store.CountUserThreads(u.ID)
	if err != nil {
		return 0, err
	}
	replies, err := s.store.CountUserReplies(u.ID)
	if err != nil {
		return 0, err
	}
	liked, err := s.store.LikesReceived(u.ID)
	if err != nil {
		return 0, err
	}
	level, _, _, _ := levelInfo(socialExp(threads, replies, liked, u.ExpExtra), u.LevelOverride)
	return level, nil
}

// joinLotteryOnReply 抽奖帖里「回复即参与」:第一次回复时登记参与,
// 设了参与积分就同时扣款进奖池。返回给用户看的错误文案(空=没问题)。
func (s *Server) joinLotteryOnReply(t *db.Thread, user *db.User) string {
	if t.Kind != "lottery" || user.ID == t.AuthorID {
		return ""
	}
	lot, err := s.store.GetLottery(t.ID)
	if err != nil || lot == nil || lot.Drawn() {
		return ""
	}
	_, err = s.store.JoinLottery(t.ID, user.ID, lot.Stake, "参与抽奖《"+t.Title+"》")
	if err == db.ErrNotEnoughPoints {
		return "参与这场抽奖需要 " + strconv.FormatInt(lot.Stake, 10) + " 积分,你的余额不够"
	}
	if err != nil {
		return "参与抽奖失败,请稍后再试"
	}
	return ""
}

// drawLottery POST /t/{id}/draw:楼主或管理员开奖。
// 随机取中奖者;奖池积分按人数均分,余数给第一位。
func (s *Server) drawLottery(w http.ResponseWriter, r *http.Request) {
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
	if user.ID != t.AuthorID && !user.IsAdmin() {
		http.Error(w, "只有楼主或管理员可以开奖", http.StatusForbidden)
		return
	}
	lot, err := s.store.GetLottery(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if lot == nil {
		http.Error(w, "这不是抽奖帖", http.StatusBadRequest)
		return
	}
	if lot.Drawn() {
		http.Redirect(w, r, "/t/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
		return
	}
	ids, err := s.store.LotteryEntryIDs(t.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(ids) == 0 {
		http.Error(w, "还没有人参与,不能开奖", http.StatusBadRequest)
		return
	}
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	n := lot.Winners
	if n > len(ids) {
		n = len(ids)
	}
	winners := ids[:n]
	prizes := make([]int64, n)
	if lot.Pool > 0 {
		each := lot.Pool / int64(n)
		for i := range prizes {
			prizes[i] = each
		}
		prizes[0] += lot.Pool - each*int64(n) // 余数给第一位,保证奖池发完
	}
	if _, err := s.store.CloseLottery(t.ID, winners, prizes); err != nil {
		s.serverError(w, err)
		return
	}
	for _, uid := range winners {
		if created, err := s.store.CreateNotification(uid, user.ID, "lottery", t.ID, 0); err == nil && created {
			s.notifyPush(uid)
		}
	}
	http.Redirect(w, r, "/t/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
}
