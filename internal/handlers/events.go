// 实时推送:每个登录用户一条 SSE 长连接,通知/私信落库后向对应用户发信号,
// 前端收到信号再去拉具体数字。信号里不带业务数据,推送方不额外占用数据库连接。
package handlers

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"bbs/internal/auth"
)

// sseKeepalive 心跳间隔:穿过反代/移动网络时防止连接被静默回收。
const sseKeepalive = 25 * time.Second

// hub 维护 userID → 该用户的所有连接(同一账号可能开了多个标签页)。
type hub struct {
	mu    sync.Mutex
	conns map[int64]map[chan string]struct{}
}

func newHub() *hub {
	return &hub{conns: make(map[int64]map[chan string]struct{})}
}

func (h *hub) add(userID int64) chan string {
	ch := make(chan string, 8)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[userID] == nil {
		h.conns[userID] = make(map[chan string]struct{})
	}
	h.conns[userID][ch] = struct{}{}
	return ch
}

func (h *hub) remove(userID int64, ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.conns[userID]; set != nil {
		delete(set, ch)
		if len(set) == 0 {
			delete(h.conns, userID)
		}
	}
	close(ch)
}

// publish 给某用户的所有连接投递一个事件名;连接积压时丢弃该次信号
// (前端下次心跳/轮询仍会补上,不值得为此阻塞写请求)。
func (h *hub) publish(userID int64, event string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.conns[userID] {
		select {
		case ch <- event:
		default:
		}
	}
}

// notifyPush 通知类信号(铃铛角标)。
func (s *Server) notifyPush(userID int64) { s.hub.publish(userID, "notif") }

// events GET /events:SSE 长连接。未登录直接 204,前端不会重连。
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	user := auth.From(r.Context()).User
	if user == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx 默认会缓冲,显式关掉
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 5000\n\n")
	flusher.Flush()

	ch := s.hub.add(user.ID)
	defer s.hub.remove(user.ID, ch)

	ticker := time.NewTicker(sseKeepalive)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: 1\n\n", ev)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
