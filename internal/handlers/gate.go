// 阅读门槛(等级 / 付费解锁)与抽奖帖。
// 门槛只挡正文与回复区,标题、作者、统计始终可见,列表页也照常出现。
package handlers

import (
	"math/rand/v2"
	"net/http"
	"slices"
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

// drawLottery POST /t/{id}/draw:楼主或管理员手动开奖。
// 真正的抽签逻辑在 runDraw 里,定点开奖的巡检走同一个函数。
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
	if lot.Over() {
		http.Redirect(w, r, "/t/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
		return
	}
	if err := s.runDraw(t.ID, t.AuthorID, lot, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/t/"+strconv.FormatInt(t.ID, 10), http.StatusSeeOther)
}

// runDraw 抽签并发奖。手动开奖和定点开奖走同一条路。
// actorID 只用来当通知的发起人,0 表示系统自动开奖(记在楼主名下)。
// 没人参与时不报错:把抽奖关掉、楼主预扣的奖池原路退回 —— 否则那笔钱会一直卡着。
func (s *Server) runDraw(threadID, authorID int64, lot *db.Lottery, actorID int64) error {
	ids, err := s.store.LotteryEntryIDs(threadID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		_, err := s.store.CancelLottery(threadID)
		return err
	}
	if actorID == 0 {
		actorID = authorID
	}
	lot.Entries = int64(len(ids))
	n := lot.MaxWinners()
	if n < 1 {
		// 积分奖但奖池是 0(实物奖不会走到这):没东西可发,当无人参与处理
		_, err := s.store.CancelLottery(threadID)
		return err
	}
	rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	winners := ids[:n]
	prizes := splitPool(lot.Pool, n)
	if _, err := s.store.CloseLottery(threadID, winners, prizes); err != nil {
		return err
	}
	for _, uid := range winners {
		if created, err := s.store.CreateNotification(uid, actorID, "lottery", threadID, 0); err == nil && created {
			s.notifyPush(uid)
		}
	}
	return nil
}

// splitPool 把奖池随机拆成 n 份,每份至少 1、总和严格等于 pool。
// 用「随机切点法」:在 1..pool-1 里取 n-1 个互不相同的切点,排序后取相邻差值。
// 比「先随机再取整」稳 —— 后者会拆出 0,也会因为取整把总额丢掉一两分。
// pool 为 0(实物奖)时返回全 0,表示只抽人不发积分。
func splitPool(pool int64, n int) []int64 {
	out := make([]int64, n)
	if pool <= 0 || n <= 0 {
		return out
	}
	if int64(n) >= pool { // 每人正好 1,没有可切的空间
		for i := range out {
			out[i] = 1
		}
		return out
	}
	cuts := map[int64]bool{}
	for int64(len(cuts)) < int64(n-1) {
		cuts[rand.Int64N(pool-1)+1] = true // 1..pool-1
	}
	sorted := make([]int64, 0, len(cuts))
	for c := range cuts {
		sorted = append(sorted, c)
	}
	slices.Sort(sorted)
	prev := int64(0)
	for i, c := range sorted {
		out[i] = c - prev
		prev = c
	}
	out[n-1] = pool - prev
	return out
}
