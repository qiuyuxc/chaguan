// 认证:用户申请(官方/厂商/作者,文案自定义)+ 管理员后台审批。
package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"chaguan/internal/db"
	"chaguan/web"
)

const (
	maxVerifySubject = 80
	maxVerifyNote    = 500
)

// verifyKinds 可申请的认证分类:官方/厂商 红 V,作者 黄 V;文案由申请人自定义。
var verifyKinds = []string{"官方", "厂商", "作者"}

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
	label := ""
	if u.VerifyTitle.Valid && strings.TrimSpace(u.VerifyTitle.String) != "" {
		label = strings.TrimSpace(u.VerifyTitle.String)
	} else if u.VerifyKind.Valid {
		switch k := strings.TrimSpace(u.VerifyKind.String); k {
		case "官号":
			label = "官方"
		case "认证作者":
			label = "作者"
		default:
			label = k
		}
	}
	if label != "" {
		return "当前已认证:「" + label + "」", false
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
	okKind := false
	for _, k := range verifyKinds {
		if kind == k {
			okKind = true
			break
		}
	}
	if !okKind {
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
	ATab     string
	List     []db.VerifyRequest
	Verified []db.VerifiedUser
	Kinds    []string
	Error    string
	Saved    string
}

func (s *Server) adminVerifyPage(w http.ResponseWriter, r *http.Request, errMsg, saved string) {
	list, err := s.store.ListVerifyRequests()
	if err != nil {
		s.serverError(w, err)
		return
	}
	verified, err := s.store.ListVerifiedUsers()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.RenderAdmin(w, 200, "admin_verify", adminVerifyData{
		Base:     s.base(r, "认证管理"),
		ATab:     "verify",
		List:     list,
		Verified: verified,
		Kinds:    verifyKinds,
		Error:    errMsg,
		Saved:    saved,
	})
}

func (s *Server) adminVerify(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.adminVerifyPage(w, r, "", "")
}

// adminAddVerify POST /admin/verify/add:不走申请流程,直接给某个账号打上认证。
// 按账号名找人;分类必须是 官方/厂商/作者,文案可留空(留空则显示分类名)。
func (s *Server) adminAddVerify(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	kind := strings.TrimSpace(r.FormValue("kind"))
	title := strings.TrimSpace(r.FormValue("title"))
	okKind := false
	for _, k := range verifyKinds {
		if kind == k {
			okKind = true
			break
		}
	}
	if !okKind {
		s.adminVerifyPage(w, r, "请选择认证分类(官方 / 厂商 / 作者)", "")
		return
	}
	if utf8.RuneCountInString(title) > maxVerifySubject {
		s.adminVerifyPage(w, r, "认证文案最多 80 字", "")
		return
	}
	user, err := s.store.FindUser(name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if user == nil {
		s.adminVerifyPage(w, r, "找不到账号「"+name+"」(账户名、显示名或邮箱都可以)", "")
		return
	}
	if err := s.store.SetVerify(user.ID, kind, title); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminVerifyPage(w, r, "", "已给 "+user.Name+" 加上「"+kind+"」认证")
}

// adminRemoveVerify POST /admin/verify/{id}/remove:撤销某人的认证。
func (s *Server) adminRemoveVerify(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetVerify(id, "", ""); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminVerifyPage(w, r, "", "认证已撤销")
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
