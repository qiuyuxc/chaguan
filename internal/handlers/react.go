package handlers

import (
	"net/http"

	"bbs/internal/db"
	"bbs/web"
)

// reactsData 帖子页「点赞 / 收藏」反应条(整页渲染与 htmx 局部刷新共用)。
type reactsData struct {
	web.Base
	Thread    *db.Thread
	LikeCount int64
	LikedByMe bool
	FavCount  int64
	FavedByMe bool
}

// toggleLike POST /t/{id}/like:点赞 / 取消点赞文章(仅首帖文章,评论不加赞)。
func (s *Server) toggleLike(w http.ResponseWriter, r *http.Request) {
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
	if _, err := s.store.ToggleThreadLike(id, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.renderReacts(w, t, user)
}

// toggleFavorite POST /t/{id}/favorite:收藏 / 取消收藏主题。
func (s *Server) toggleFavorite(w http.ResponseWriter, r *http.Request) {
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
	if _, err := s.store.ToggleFavorite(id, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.renderReacts(w, t, user)
}

// togglePostLike POST /p/{id}/like:点赞/取消点赞某一楼(文章与回复均可;
// 资料页「我的点赞」只收纳对文章首帖的赞,回复赞不进入该列表)。
func (s *Server) togglePostLike(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPost(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.TogglePostLike(id, user.ID); err != nil {
		s.serverError(w, err)
		return
	}
	likes, err := s.store.PostLikeByID(id, user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Partial(w, 200, "thread", "plike", web.PostView{
		Post: *p, Viewer: user, LikeCount: likes.Count, LikedByMe: likes.Liked,
	})
}

func (s *Server) renderReacts(w http.ResponseWriter, t *db.Thread, user *db.User) {
	likeCount, favCount, liked, faved, err := s.store.ThreadReacts(t.ID, user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Partial(w, 200, "thread", "reacts", reactsData{
		Base:      web.Base{User: user},
		Thread:    t,
		LikeCount: likeCount,
		LikedByMe: liked,
		FavCount:  favCount,
		FavedByMe: faved,
	})
}
