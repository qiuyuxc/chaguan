// 积分商城:前台兑换(勋章 / 签到加成)+ 后台商品与勋章管理。
package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"chaguan/internal/db"
	"chaguan/web"
)

const shopOrderLimit = 20

type shopData struct {
	web.Base
	Items  []db.ShopItem
	Orders []db.ShopOrder
	Points int64
	Notice string
	Error  string
}

// shop GET /shop:积分商城。
func (s *Server) shop(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	s.shopPage(w, r, user, "", "")
}

func (s *Server) shopPage(w http.ResponseWriter, r *http.Request, user *db.User, notice, errMsg string) {
	items, err := s.store.ListShopItems(true, user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	orders, err := s.store.ListShopOrders(user.ID, shopOrderLimit)
	if err != nil {
		s.serverError(w, err)
		return
	}
	points, err := s.store.Points(user.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.Render(w, 200, "shop", shopData{
		Base:   s.base(r, "积分商城"),
		Items:  items,
		Orders: orders,
		Points: points,
		Notice: notice,
		Error:  errMsg,
	})
}

// redeem POST /shop/{id}/redeem:用积分兑换商品。
func (s *Server) redeem(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.GetShopItem(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if it == nil || !it.Active {
		http.NotFound(w, r)
		return
	}
	err = s.store.RedeemShopItem(user.ID, *it)
	switch err {
	case nil:
		note := "兑换成功:" + it.Name
		switch it.Kind {
		case "badge":
			note += " · 去「编辑资料」里就能佩戴了"
		case "custom":
			note += " · 管理员会按说明为你发放"
		}
		s.shopPage(w, r, user, note, "")
	case db.ErrNotEnoughPoints:
		s.shopPage(w, r, user, "", "积分不足,先去签到或发帖攒点")
	case db.ErrShopOwned:
		s.shopPage(w, r, user, "", "这枚勋章你已经有了")
	case db.ErrShopSoldOut:
		s.shopPage(w, r, user, "", "手慢了,这件已经兑换完了")
	default:
		s.serverError(w, err)
	}
}

// wearBadge POST /u/{id}/badge:选择佩戴的勋章(或跟随身份 / 隐藏标签)。
func (s *Server) wearBadge(w http.ResponseWriter, r *http.Request) {
	user := s.selfUser(w, r)
	if user == nil {
		return
	}
	mode := strings.TrimSpace(r.FormValue("badge_mode"))
	badgeID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("badge_id")), 10, 64)
	switch mode {
	case "wear":
		if badgeID <= 0 {
			http.Error(w, "请选择一枚勋章", http.StatusBadRequest)
			return
		}
		owned, err := s.store.HasBadge(user.ID, badgeID)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !owned {
			http.Error(w, "你还没有这枚勋章", http.StatusForbidden)
			return
		}
		if err := s.store.WearBadge(user.ID, badgeID, false); err != nil {
			s.serverError(w, err)
			return
		}
	case "hide":
		if err := s.store.WearBadge(user.ID, 0, true); err != nil {
			s.serverError(w, err)
			return
		}
	default: // follow
		if err := s.store.WearBadge(user.ID, 0, false); err != nil {
			s.serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/u/"+strconv.FormatInt(user.ID, 10)+"/edit", http.StatusSeeOther)
}

// ---------- 后台:商城与勋章 ----------

type adminShopData struct {
	web.Base
	ATab   string
	Items  []db.ShopItem
	Badges []db.Badge
	Orders []db.AdminShopOrder
	Error  string
	Saved  string
}

func (s *Server) adminShopPage(w http.ResponseWriter, r *http.Request, errMsg, saved string) {
	items, err := s.store.ListShopItems(false, 0)
	if err != nil {
		s.serverError(w, err)
		return
	}
	badges, err := s.store.ListBadges()
	if err != nil {
		s.serverError(w, err)
		return
	}
	orders, err := s.store.ListAllShopOrders(20)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.rend.RenderAdmin(w, 200, "admin_shop", adminShopData{
		Base:   s.base(r, "商城与勋章"),
		ATab:   "shop",
		Items:  items,
		Badges: badges,
		Orders: orders,
		Error:  errMsg,
		Saved:  saved,
	})
}

func (s *Server) adminShop(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	s.adminShopPage(w, r, "", "")
}

// adminNewBadge POST /admin/badges:新建勋章。
func (s *Server) adminNewBadge(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	note := strings.TrimSpace(r.FormValue("note"))
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 12 {
		s.adminShopPage(w, r, "勋章文案 1–12 字(会显示在用户名后面)", "")
		return
	}
	if utf8.RuneCountInString(note) > 60 {
		s.adminShopPage(w, r, "说明最多 60 字", "")
		return
	}
	if _, err := s.store.CreateBadge(name, note); err != nil {
		if err == db.ErrDuplicateName {
			s.adminShopPage(w, r, "已经有同名勋章了", "")
			return
		}
		s.serverError(w, err)
		return
	}
	s.adminShopPage(w, r, "", "勋章「"+name+"」已创建")
}

// adminDeleteBadge POST /admin/badges/{id}/delete:删除勋章(佩戴者回落身份标签)。
func (s *Server) adminDeleteBadge(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteBadge(id); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminShopPage(w, r, "", "勋章已删除")
}

// parseShopItemForm 从表单读商品字段并校验,第二个返回值非空即错误提示。
// kind 由调用方给:新建时从表单读,编辑时沿用商品原有类型(类型不可改)。
func parseShopItemForm(r *http.Request, kind string) (db.ShopItem, string) {
	name := strings.TrimSpace(r.FormValue("name"))
	note := strings.TrimSpace(r.FormValue("note"))
	price, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("price")), 10, 64)
	bonus, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("bonus")), 10, 64)
	days, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("days")), 10, 64)
	badgeID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("badge_id")), 10, 64)
	stock := int64(-1)
	if v := strings.TrimSpace(r.FormValue("stock")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > 1000000 {
			return db.ShopItem{}, "库存留空表示不限量,填数字需在 0–1000000 之间"
		}
		stock = n
	}
	switch {
	case utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 30:
		return db.ShopItem{}, "商品名 1–30 字"
	case utf8.RuneCountInString(note) > 80:
		return db.ShopItem{}, "说明最多 80 字"
	case price < 1 || price > 1000000:
		return db.ShopItem{}, "价格需在 1–1000000 积分之间"
	}
	// 价格与加成表单里填的是整数积分,库里存「分」,在这里换算一次
	it := db.ShopItem{Kind: kind, Name: name, Note: note, Price: db.Pts(price), Stock: stock}
	switch kind {
	case "badge":
		if badgeID <= 0 {
			return db.ShopItem{}, "请选择这件商品对应的勋章"
		}
		it.BadgeID = sql.NullInt64{Int64: badgeID, Valid: true}
	case "checkin":
		if bonus < 1 || bonus > 100 {
			return db.ShopItem{}, "签到加成需在 1–100 积分/天之间"
		}
		if days < 0 || days > 3650 {
			return db.ShopItem{}, "有效天数需在 0–3650 之间(0=不限期)"
		}
		it.Bonus, it.Days = db.Pts(bonus), days
	case "custom":
		if utf8.RuneCountInString(note) < 1 {
			return db.ShopItem{}, "自定义商品请在说明里写清兑换后怎么发放"
		}
	}
	return it, ""
}

// adminNewShopItem POST /admin/shop:新建商品(勋章 / 签到加成 / 自定义)。
func (s *Server) adminNewShopItem(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	switch kind {
	case "badge", "checkin", "custom":
	default:
		kind = "badge"
	}
	it, msg := parseShopItemForm(r, kind)
	if msg != "" {
		s.adminShopPage(w, r, msg, "")
		return
	}
	if _, err := s.store.CreateShopItem(it); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminShopPage(w, r, "", "商品「"+it.Name+"」已上架")
}

// adminEditShopItem POST /admin/shop/{id}/edit:改已有商品。
// 类型不可改,其余字段(名称/说明/价格/库存/加成/天数/对应勋章)都能改。
func (s *Server) adminEditShopItem(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	cur, err := s.store.GetShopItem(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if cur == nil {
		http.NotFound(w, r)
		return
	}
	it, msg := parseShopItemForm(r, cur.Kind)
	if msg != "" {
		s.adminShopPage(w, r, msg, "")
		return
	}
	it.ID = id
	if err := s.store.UpdateShopItem(it); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminShopPage(w, r, "", "商品「"+it.Name+"」已更新")
}

// adminToggleShopItem POST /admin/shop/{id}/toggle:上架/下架。
func (s *Server) adminToggleShopItem(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	it, err := s.store.GetShopItem(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if it == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetShopItemActive(id, !it.Active); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminShopPage(w, r, "", "")
}

// adminDeleteShopItem POST /admin/shop/{id}/delete:删除商品(已兑换记录保留)。
func (s *Server) adminDeleteShopItem(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteShopItem(id); err != nil {
		s.serverError(w, err)
		return
	}
	s.adminShopPage(w, r, "", "商品已删除")
}

// adminGrantBadge POST /admin/users/{id}/badge:后台发放或收回勋章。
func (s *Server) adminGrantBadge(w http.ResponseWriter, r *http.Request) {
	target, ok := s.adminTarget(w, r)
	if !ok {
		return
	}
	badgeID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("badge_id")), 10, 64)
	if err != nil || badgeID < 1 {
		http.Error(w, "请选择勋章", http.StatusBadRequest)
		return
	}
	if r.FormValue("revoke") == "1" {
		err = s.store.RevokeBadge(target.ID, badgeID)
	} else {
		err = s.store.GrantBadge(target.ID, badgeID, "admin")
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectAfter(w, r, "/admin/users")
}
