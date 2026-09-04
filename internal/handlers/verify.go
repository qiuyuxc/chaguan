// 认证:用户申请(官号/认证作者)+ 管理员后台审批。
package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"bbs/internal/db"
	"bbs/web"
)

const (
	maxVerifySubject = 80
	maxVerifyNote    = 500
)

// verifyState 申请人当前状态文案;返回 form=true 表示可以填表申请。
func verifyState(u *db.User, pending bool) (text string, form bool) {
	if u == nil {
		return "", false
	}
	if u.Role != "user" {
		if u.Role == "admin" {
			return "管理员按身份自动认证,无需申请。", false
		}
		return "版主按身份自动认证,无需申请。", false
	}
	if u.VerifyTitle.Valid && strings.TrimSpace(u.VerifyTitle.String) != "" {
		return "当前已认证:「" + strings.TrimSpace(u.VerifyTitle.String) + "」", false
	}
	if pending {
		return "已提交申请,等待管理员审核。", false
	}
	return "", true
}

type verifyApplyData struct {
	web.Base
	State   string // 顶部状态提示;空=展示表单
	Form    bool
	Done    bool
	Error   string
	Kind    string // 校验失败后保留选择
	Subject string // 校验失败后保留内容
	Note    string
}

func (s *Server) verifyApplyForm(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	pending, err := s.store.PendingVerify(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	state, form := verifyState(user, pending)
	s.rend.Render(w, 200, "verify_apply", verifyApplyData{
		Base:  s.base(r, "账号认证申请"),
		State: state,
		Form:  form,
		Done:  r.URL.Query().Get("ok") == "1",
	})
}

func (s *Server) verifyApplyPost(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	subject := strings.TrimSpace(r.FormValue("subject"))
	note := strings.TrimSpace(r.FormValue("note"))
	pending, err := s.store.PendingVerify(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if state, form := verifyState(user, pending); !form {
		s.rend.Render(w, 200, "verify_apply", verifyApplyData{
			Base: s.base(r, "账号认证申请"), State: state, Form: false,
		})
		return
	}
	if kind != "官号" && kind != "认证作者" {
		s.renderVerifyError(w, r, kind, subject, note, "请选择认证类型")
		return
	}
	if utf8.RuneCountInString(subject) < 1 || utf8.RuneCountInString(subject) > maxVerifySubject {
		s.renderVerifyError(w, r, kind, subject, note, "请填写认证对象(1–80 字)")
		return
	}
	if utf8.RuneCountInString(note) > maxVerifyNote {
		s.renderVerifyError(w, r, kind, subject, note, "说明最多 500 字")
		return
	}
	if ok, err := s.store.CreateVerifyRequest(user.ID, kind, subject, note); err != nil {
		s.serverError(w, err)
		return
	} else if !ok {
		s.renderVerifyError(w, r, kind, subject, note, "已有待审核申请,请勿重复提交")
		return
	}
	http.Redirect(w, r, "/verify/apply?ok=1", http.StatusSeeOther)
}

func (s *Server) renderVerifyError(w http.ResponseWriter, r *http.Request, kind, subject, note, msg string) {
	s.rend.Render(w, 422, "verify_apply", verifyApplyData{
		Base: s.base(r, "账号认证申请"), Form: true, Kind: kind, Subject: subject, Note: note, Error: msg,
	})
}

// ---------- 管理员:认证申请审批 ----------

type adminVerifyData struct {
	web.Base
	ATab string
	List []db.VerifyRequest
}

func (s *Server) adminVerify(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	list, err := s.store.ListVerifyRequests()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.RenderAdmin(w, 200, "admin_verify", adminVerifyData{
		Base: s.base(r, "认证申请"),
		ATab: "verify",
		List: list,
	})
}

// resolveVerify POST /admin/verify/{id}/approve 或 /reject。
func (s *Server) resolveVerify(w http.ResponseWriter, r *http.Request) {
	user := s.requireAdmin(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	approve := strings.HasSuffix(r.URL.Path, "/approve")
	if err := s.store.ResolveVerify(id, user.ID, approve); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/admin/verify")
}
