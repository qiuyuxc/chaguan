// 私信:一对一实时会话。新消息通过 SSE 信号推给对方,对方页面上的消息列表
// 用 htmx 拉一次最新内容,不做前端状态机。正文按纯文本处理(不走 Markdown)。
package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"bbs/internal/db"
	"bbs/internal/markdown"
	"bbs/web"
)

const (
	maxDMLen        = 2000
	dmPerPage       = 20
	dmMessagesLimit = 200                  // 会话页最多展示最近这么多条
	maxRedpack      = 5000 * db.PointScale // 单个红包上限(内部单位「分」)
)

type dmListData struct {
	web.Base
	Conversations []db.DMConversation
	Page, Pages   int
	BaseURL       string
	HasQ          bool
}

// messages GET /messages:我的私信会话列表。
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	page := pageParam(r)
	total, err := s.store.CountDMConversations(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	convs, err := s.store.ListDMConversations(user.ID, dmPerPage, (page-1)*dmPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "dm_list", dmListData{
		Base:          s.base(r, "私信"),
		Conversations: convs,
		Page:          page,
		Pages:         totalPages(total, dmPerPage),
		BaseURL:       "/messages",
	})
}

// dmStart POST /messages/start(表单字段 to=用户 id):开始或继续会话,跳到会话页。
// 用 /messages/start 而不是 /messages/to/{id},是为了避开与 /messages/{id}/send
// 的路由歧义(ServeMux 对这类两段通配会直接 panic)。
func (s *Server) dmStart(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	peerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("to")), 10, 64)
	if err != nil || peerID < 1 {
		http.NotFound(w, r)
		return
	}
	if peerID == user.ID {
		http.Error(w, "不能给自己发私信", http.StatusBadRequest)
		return
	}
	peer, err := s.store.GetUserByID(peerID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if peer == nil {
		http.NotFound(w, r)
		return
	}
	tid, err := s.store.DMThreadFor(user.ID, peerID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/messages/"+strconv.FormatInt(tid, 10), http.StatusSeeOther)
}

type dmThreadData struct {
	web.Base
	Conv     *db.DMConversation
	Messages []web.DMView
}

// loadDMThread 取会话并校验当前用户是参与者;不是则 404(不泄露会话存在与否)。
func (s *Server) loadDMThread(w http.ResponseWriter, r *http.Request) (*db.DMConversation, *db.User) {
	user := s.currentUser(w, r)
	if user == nil {
		return nil, nil
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return nil, nil
	}
	conv, err := s.store.GetDMConversation(id, user.ID)
	if err != nil {
		s.serverError(w, err)
		return nil, nil
	}
	if conv == nil {
		http.NotFound(w, r)
		return nil, nil
	}
	return conv, user
}

// dmViews 组装消息视图并把对方发来的未读标为已读。
func (s *Server) dmViews(conv *db.DMConversation, viewer *db.User) ([]web.DMView, error) {
	msgs, err := s.store.ListDMMessages(conv.ID, dmMessagesLimit)
	if err != nil {
		return nil, err
	}
	if err := s.store.MarkDMRead(conv.ID, viewer.ID); err != nil {
		return nil, err
	}
	out := make([]web.DMView, 0, len(msgs))
	for _, m := range msgs {
		v := web.DMView{Msg: m, Mine: m.SenderID == viewer.ID, Peer: conv}
		if !m.IsRedpack() {
			// 红包气泡有自己的结构,body 只是给列表预览用的说明文字,不渲染
			v.BodyHTML = markdown.RenderDM(m.Body)
		}
		out = append(out, v)
	}
	return out, nil
}

// dmThread GET /messages/{id}:会话页。
func (s *Server) dmThread(w http.ResponseWriter, r *http.Request) {
	conv, user := s.loadDMThread(w, r)
	if conv == nil {
		return
	}
	views, err := s.dmViews(conv, user)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "dm_thread", dmThreadData{
		Base:     s.base(r, "与 "+conv.PeerName+" 的私信"),
		Conv:     conv,
		Messages: views,
	})
}

// dmList GET /messages/{id}/list:消息列表片段。收到实时信号后由 htmx 拉它刷新。
func (s *Server) dmList(w http.ResponseWriter, r *http.Request) {
	conv, user := s.loadDMThread(w, r)
	if conv == nil {
		return
	}
	views, err := s.dmViews(conv, user)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Partial(w, 200, "dm_thread", "dm_msgs", dmThreadData{Conv: conv, Messages: views})
}

// dmSend POST /messages/{id}/send:发送一条私信,返回刷新后的消息列表片段。
func (s *Server) dmSend(w http.ResponseWriter, r *http.Request) {
	conv, user := s.loadDMThread(w, r)
	if conv == nil {
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if utf8.RuneCountInString(body) < 1 || utf8.RuneCountInString(body) > maxDMLen {
		http.Error(w, "内容 1–2000 字", http.StatusUnprocessableEntity)
		return
	}
	if _, err := s.store.SendDM(conv.ID, user.ID, body); err != nil {
		s.serverError(w, err)
		return
	}
	// 信号照发,即便对方开了免打扰:那只影响顶栏角标,不该让打开着的会话不刷新
	s.hub.publish(conv.PeerID, "dm")
	s.renderDMList(w, conv, user)
}

// dmRedpack POST /messages/{id}/redpack:给对方发一个积分红包。
func (s *Server) dmRedpack(w http.ResponseWriter, r *http.Request) {
	conv, user := s.loadDMThread(w, r)
	if conv == nil {
		return
	}
	// 红包金额允许两位小数
	amount, err := db.ParsePoints(r.FormValue("amount"))
	if err != nil || amount < 1 || amount > maxRedpack {
		http.Error(w, "红包积分需在 0.01–5000 之间", http.StatusBadRequest)
		return
	}
	_, err = s.store.SendRedpack(conv.ID, user.ID, amount, "发给 "+conv.PeerName+" 的红包")
	if err == db.ErrNotEnoughPoints {
		http.Error(w, "积分不足,先去签到攒点吧", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.hub.publish(conv.PeerID, "dm")
	s.renderDMList(w, conv, user)
}

// dmClaim POST /messages/{id}/claim:领取红包(表单字段 msg=消息 id)。
func (s *Server) dmClaim(w http.ResponseWriter, r *http.Request) {
	conv, user := s.loadDMThread(w, r)
	if conv == nil {
		return
	}
	msgID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("msg")), 10, 64)
	if err != nil || msgID < 1 {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	if _, _, err := s.store.ClaimRedpack(msgID, conv.ID, user.ID,
		"领取 "+conv.PeerName+" 的红包"); err != nil {
		s.serverError(w, err)
		return
	}
	s.hub.publish(conv.PeerID, "dm") // 让对方那边也刷新成「已领取」
	s.renderDMList(w, conv, user)
}

// dmRefund POST /messages/{id}/refund:撤回自己发出的、对方还没领的红包。
func (s *Server) dmRefund(w http.ResponseWriter, r *http.Request) {
	conv, user := s.loadDMThread(w, r)
	if conv == nil {
		return
	}
	msgID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("msg")), 10, 64)
	if err != nil || msgID < 1 {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	_, gone, err := s.store.RefundRedpack(msgID, conv.ID, user.ID,
		"撤回发给 "+conv.PeerName+" 的红包")
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.hub.publish(conv.PeerID, "dm")
	if gone {
		// 这条红包是会话里唯一的消息,撤回时连会话一起删了(发错人不留痕),
		// 当前页面已经无内容可渲染 —— 让 htmx 整页跳回私信列表
		w.Header().Set("HX-Redirect", "/messages")
		w.WriteHeader(http.StatusOK)
		return
	}
	s.renderDMList(w, conv, user)
}

// renderDMList 渲染消息列表片段(发送/领取/撤回后统一走它)。
func (s *Server) renderDMList(w http.ResponseWriter, conv *db.DMConversation, user *db.User) {
	views, err := s.dmViews(conv, user)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Partial(w, 200, "dm_thread", "dm_msgs", dmThreadData{Conv: conv, Messages: views})
}

// dmUnread 供 unreadCount 复用:未读私信数;开了免打扰则一律报 0(只在列表里看)。
func (s *Server) dmUnread(user *db.User) int64 {
	if user == nil || !user.NotifyDM {
		return 0
	}
	n, err := s.store.CountUnreadDM(user.ID)
	if err != nil {
		return 0
	}
	return n
}
