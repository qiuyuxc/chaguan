// Package db 打开 SQLite 连接、执行内嵌迁移,并承载全部 SQL 查询。
// 不使用 ORM:换库时只需重写本包。
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migFS embed.FS

// Store 包装 *sql.DB。<100 人论坛用单连接串行化全部读写,
// 彻底消灭 SQLITE_BUSY;单条查询微秒级,串行不构成瓶颈。
type Store struct {
	DB *sql.DB
}

// Open 打开数据库并应用 pragmas。dsn 形如 "file:/data/chaguan.db"。
func Open(dsn string) (*Store, error) {
	// 每个新连接都会应用这些 pragma(单连接池下只有一条)
	dsn += "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	d.SetConnMaxIdleTime(0)
	if err := d.Ping(); err != nil {
		return nil, err
	}
	return &Store{DB: d}, nil
}

// Migrate 按文件名序执行未应用的迁移,每个迁移一个事务。
func (s *Store) Migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migFS, "migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var maxApplied int
	row := s.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`)
	if err := row.Scan(&maxApplied); err != nil {
		return err
	}

	for _, name := range names {
		ver, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration 文件名必须以数字版本开头: %s", name)
		}
		if ver <= maxApplied {
			continue
		}
		body, err := migFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		err = s.withTx(func(tx *sql.Tx) error {
			if _, err := tx.Exec(string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			_, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				ver, time.Now().Unix())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) withTx(fn func(*sql.Tx) error) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------- 模型 ----------

type User struct {
	ID            int64
	Name          string
	Email         string
	PasswordHash  string
	Role          string // user | mod | admin
	AvatarPath    string
	Bio           string
	CreatedAt     int64
	BannedUntil   sql.NullInt64
	BadgeText     sql.NullString // NULL=跟随身份; ''=隐藏; 非空=自定义称号
	VerifyKind    sql.NullString // 认证分类:官方/厂商/作者; NULL=无认证(管理员/版主可按身份)
	VerifyTitle   sql.NullString // 认证显示文案(自定义,如「米哈游官方」「游戏作者」;空则回退分类)
	LevelOverride sql.NullInt64  // 管理员手动指定等级 0..6; NULL=按经验自动
	StatFollowing sql.NullInt64  // 展示用关注数覆盖; NULL=按真实统计
	StatFollowers sql.NullInt64  // 展示用粉丝数覆盖; NULL=按真实统计
	StatLiked     sql.NullInt64  // 展示用获赞数覆盖; NULL=按真实统计
	NotifyScope   string         // 通知接收范围: all | reply | mention | none
	NotifyFreq    int            // 通知接收频率(秒);0=实时推送
	NotifyDM      bool           // 新私信是否实时提醒(关=免打扰,只在私信列表里看未读)
	EmailVerified sql.NullInt64  // 邮箱通过验证的时间;email 非空而此项为 NULL = 待验证
	Points        int64          // 积分余额
	ExpExtra      int64          // 签到/活动/商城送的额外经验(与发帖回复获赞相加)
	CheckinBonus  int64          // 增值服务:每天签到额外积分
	BonusUntil    sql.NullInt64  // 增值服务到期时间(NULL=不限期)
	BadgeID       sql.NullInt64  // 正佩戴的勋章 id(NULL=没戴,标签跟随身份或隐藏)
	LoginName     sql.NullString // 登录用的账户名(与显示名 Name 分开)
	TOTPSecret    sql.NullString // 两步验证密钥(生成后先存,启用才校验)
	TOTPEnabled   bool           // 是否已开启两步验证
}

// Login 返回登录用的账户名;老账号没单独设过就沿用显示名。
func (u *User) Login() string {
	if u == nil {
		return ""
	}
	if u.LoginName.Valid && u.LoginName.String != "" {
		return u.LoginName.String
	}
	return u.Name
}

// CheckinExtra 当前生效的签到额外积分(过期的增值服务不再计入)。
func (u *User) CheckinExtra() int64 {
	if u == nil || u.CheckinBonus <= 0 {
		return 0
	}
	if u.BonusUntil.Valid && u.BonusUntil.Int64 <= time.Now().Unix() {
		return 0
	}
	return u.CheckinBonus
}

// NeedsEmailVerify 该账号是否卡在「填了邮箱但没验证」的状态(登录要拦)。
// 没填邮箱的老账号不受影响,开启邮件注册也不会把它们锁死。
func (u *User) NeedsEmailVerify() bool {
	return u != nil && u.Email != "" && !u.EmailVerified.Valid
}

// 通知接收范围取值。
const (
	NotifyAll     = "all"
	NotifyReply   = "reply"
	NotifyMention = "mention"
	NotifyNone    = "none"
)

// ValidNotifyScope 校验接收范围,非法值按「全部」处理。
func ValidNotifyScope(v string) string {
	switch v {
	case NotifyReply, NotifyMention, NotifyNone:
		return v
	default:
		return NotifyAll
	}
}

// ValidNotifyFreq 校验接收频率:只接受 实时 / 5 分钟 / 30 分钟 三档。
func ValidNotifyFreq(sec int) int {
	switch sec {
	case 300, 1800:
		return sec
	default:
		return 0
	}
}

// WantsNotify 该用户是否接收此类通知。
// 「只接收评论 / 只接收 @提及」只筛社区互动(reply / mention);
// 打赏、抽奖这类账户通知照常发,只有整体关闭才不发。
func (u *User) WantsNotify(ntype string) bool {
	if u == nil {
		return false
	}
	social := ntype == "reply" || ntype == "mention"
	switch u.NotifyScope {
	case NotifyNone:
		return false
	case NotifyReply:
		return ntype == "reply" || !social
	case NotifyMention:
		return ntype == "mention" || !social
	default:
		return true
	}
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }
func (u *User) IsMod() bool   { return u != nil && (u.Role == "mod" || u.Role == "admin") }

type Category struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	ThreadCount int64
	LastPostAt  sql.NullInt64 // 活跃度排序用
}

type Thread struct {
	ID               int64
	CategoryID       int64
	CategorySlug     string
	CategoryName     string
	AuthorID         int64
	AuthorName       string
	AuthorAvatar     string
	AuthorRole       string
	AuthorVerifyKind sql.NullString // 作者认证分类(官方/厂商/作者)
	AuthorVerify     sql.NullString // 作者认证显示文案
	Title            string
	Kind             string // normal | lottery
	MinLevel         int    // 观看门槛等级(0=不限)
	Price            int64  // 观看需支付的积分(0=免费)
	IsPinned         bool
	IsLocked         bool
	CreatedAt        int64
	LastPostAt       int64
	ViewCount        int64
	PostCount        int64 // 含首帖
	LastPostBy       string
	LikeCount        int64 // 文章(首帖)获赞
	FavCount         int64 // 主题被收藏数
}

// ReplyCount 是用户视角的"回复数"(不含首帖)。
func (t *Thread) ReplyCount() int64 { return t.PostCount - 1 }

type Post struct {
	ID               int64
	ThreadID         int64
	AuthorID         int64
	AuthorName       string
	AuthorAvatar     string
	AuthorRole       string
	AuthorBadge      sql.NullString
	AuthorVerifyKind sql.NullString // 作者认证分类(官方/厂商/作者)
	AuthorVerify     sql.NullString // 作者认证显示文案
	ContentMD        string
	ContentHTML      string
	IsFirst          bool
	CreatedAt        int64
	EditedAt         sql.NullInt64
}

// UserSearch 是 @ 搜索接口返回的轻量用户信息(不携带邮箱/口令)。
type UserSearch struct {
	ID         int64
	Name       string
	AvatarPath string
	Role       string
	BadgeText  sql.NullString
}

// ---------- users / sessions ----------

var ErrDuplicateName = errors.New("用户名已被占用")

func (s *Store) CountUsers() (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CountAllThreads() (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM threads`).Scan(&n)
	return n, err
}

// AdminStats 管理后台概览的整站数字与「今日」新增量(按服务器本地时区零点起算)。
type AdminStats struct {
	Users        int64 // 成员总数
	Staff        int64 // 版主 + 管理员
	Threads      int64 // 主题总数
	Replies      int64 // 回复总数(不含首帖)
	Categories   int64 // 版块数
	TodayUsers   int64 // 今日新成员
	TodayThreads int64 // 今日新主题
	TodayReplies int64 // 今日新回复
}

func (s *Store) AdminStats(dayStart int64) (AdminStats, error) {
	var st AdminStats
	err := s.DB.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM users),
			(SELECT COUNT(*) FROM users WHERE role IN ('mod','admin')),
			(SELECT COUNT(*) FROM threads),
			(SELECT COUNT(*) FROM posts WHERE is_first = 0),
			(SELECT COUNT(*) FROM categories),
			(SELECT COUNT(*) FROM users WHERE created_at >= ?),
			(SELECT COUNT(*) FROM threads WHERE created_at >= ?),
			(SELECT COUNT(*) FROM posts WHERE is_first = 0 AND created_at >= ?)`,
		dayStart, dayStart, dayStart).
		Scan(&st.Users, &st.Staff, &st.Threads, &st.Replies, &st.Categories,
			&st.TodayUsers, &st.TodayThreads, &st.TodayReplies)
	return st, err
}

func (s *Store) CreateUser(name, email, passwordHash string) (int64, error) {
	n, err := s.CountUsers()
	if err != nil {
		return 0, err
	}
	role := "user"
	if n == 0 {
		role = "admin" // 首个注册用户即管理员
	}
	var emailArg any
	if email != "" {
		emailArg = email
	}
	res, err := s.DB.Exec(
		`INSERT INTO users (name, login_name, email, password_hash, role, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		name, name, emailArg, passwordHash, role, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicateName
		}
		return 0, err
	}
	return res.LastInsertId()
}

const userCols = `
	id, name, COALESCE(email,''), password_hash, role, COALESCE(avatar_path,''),
	COALESCE(bio,''), created_at, banned_until, badge_text, verify_kind, verify_title,
	level_override, stat_following, stat_followers, stat_liked, notify_scope, notify_freq, notify_dm,
	email_verified_at, points, exp_extra, checkin_bonus, bonus_until, badge_id,
	login_name, totp_secret, totp_enabled`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarPath,
		&u.Bio, &u.CreatedAt, &u.BannedUntil, &u.BadgeText, &u.VerifyKind, &u.VerifyTitle,
		&u.LevelOverride, &u.StatFollowing, &u.StatFollowers, &u.StatLiked,
		&u.NotifyScope, &u.NotifyFreq, &u.NotifyDM, &u.EmailVerified,
		&u.Points, &u.ExpExtra, &u.CheckinBonus, &u.BonusUntil, &u.BadgeID,
		&u.LoginName, &u.TOTPSecret, &u.TOTPEnabled)
	return u, err
}

// GetUserByLogin 登录用:账户名(login_name,回落 name)或邮箱都能登。
func (s *Store) GetUserByLogin(v string) (*User, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users
		WHERE COALESCE(NULLIF(login_name,''), name) = ? COLLATE NOCASE
		   OR email = ? COLLATE NOCASE`, v, v))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// FindUser 后台按「账户名 / 显示名 / 邮箱」任一找账号。
func (s *Store) FindUser(v string) (*User, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users
		WHERE name = ? COLLATE NOCASE
		   OR COALESCE(NULLIF(login_name,''), name) = ? COLLATE NOCASE
		   OR email = ? COLLATE NOCASE`, v, v, v))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// rowScanner 让占用检查既能用在 *sql.DB 上,也能用在事务里。
type rowScanner interface {
	QueryRow(string, ...any) *sql.Row
}

// nameTaken 该名字是否已被别人用作显示名或账户名。
// 两者共用一个命名空间:否则有人把账户名改成别人的显示名,登录时就会撞在一起。
func nameTaken(q rowScanner, userID int64, name string) (bool, error) {
	var n int64
	err := q.QueryRow(`SELECT COUNT(*) FROM users WHERE id <> ?
		AND (name = ? COLLATE NOCASE
		  OR COALESCE(NULLIF(login_name,''), name) = ? COLLATE NOCASE)`,
		userID, name, name).Scan(&n)
	return n > 0, err
}

// NameTaken 供 handler 提前给出友好提示(落库时仍有唯一索引兜底)。
func (s *Store) NameTaken(userID int64, name string) (bool, error) {
	return nameTaken(s.DB, userID, name)
}

// UpdateLoginName 改登录用的账户名;与他人的显示名/账户名冲突返回 ErrDuplicateName。
func (s *Store) UpdateLoginName(userID int64, name string) error {
	taken, err := nameTaken(s.DB, userID, name)
	if err != nil {
		return err
	}
	if taken {
		return ErrDuplicateName
	}
	_, err = s.DB.Exec(`UPDATE users SET login_name = ? WHERE id = ?`, name, userID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrDuplicateName
	}
	return err
}

// SetTOTPSecret 存下(尚未启用的)两步验证密钥。
func (s *Store) SetTOTPSecret(userID int64, secret string) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_secret = ?, totp_enabled = 0 WHERE id = ?`,
		secret, userID)
	return err
}

// EnableTOTP 校验通过后正式开启两步验证。
func (s *Store) EnableTOTP(userID int64) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_enabled = 1 WHERE id = ?
		AND totp_secret IS NOT NULL AND totp_secret <> ''`, userID)
	return err
}

// DisableTOTP 关闭两步验证并清掉密钥(重新开启需重新绑定)。
func (s *Store) DisableTOTP(userID int64) error {
	_, err := s.DB.Exec(`UPDATE users SET totp_enabled = 0, totp_secret = NULL WHERE id = ?`, userID)
	return err
}

// RenameDisplayName 改显示名并扣积分,一个事务:改名失败不扣钱,余额不够不改名。
func (s *Store) RenameDisplayName(userID int64, name string, cost int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		taken, err := nameTaken(tx, userID, name)
		if err != nil {
			return err
		}
		if taken {
			return ErrDuplicateName
		}
		if cost > 0 {
			if err := addPointsTx(tx, userID, -cost, PointRename, "修改显示名", 0, 0); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE users SET name = ? WHERE id = ?`, name, userID)
		if err != nil && strings.Contains(err.Error(), "UNIQUE") {
			return ErrDuplicateName
		}
		return err
	})
}

// GetUserByEmail 按邮箱找账号(找回密码用);邮箱为空串时直接返回 nil。
func (s *Store) GetUserByEmail(email string) (*User, error) {
	if strings.TrimSpace(email) == "" {
		return nil, nil
	}
	u, err := scanUser(s.DB.QueryRow(
		`SELECT `+userCols+` FROM users WHERE email = ? COLLATE NOCASE`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// SetNotifyPrefs 保存通知偏好(接收范围 + 频率 + 私信免打扰);取值由 ValidNotify* 归一化。
func (s *Store) SetNotifyPrefs(userID int64, scope string, freq int, dm bool) error {
	_, err := s.DB.Exec(`UPDATE users SET notify_scope = ?, notify_freq = ?, notify_dm = ? WHERE id = ?`,
		ValidNotifyScope(scope), ValidNotifyFreq(freq), dm, userID)
	return err
}

// SearchUsers 按名称模糊匹配用户(@ 搜索用);转义 LIKE 通配符,最多 limit 条。
func (s *Store) SearchUsers(q string, limit int) ([]UserSearch, error) {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.DB.Query(`
		SELECT id, name, COALESCE(avatar_path,''), role, badge_text
		FROM users
		WHERE name LIKE ? ESCAPE '\'
		ORDER BY (role = 'admin') DESC, (role = 'mod') DESC, name COLLATE NOCASE
		LIMIT ?`, "%"+q+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserSearch
	for rows.Next() {
		var u UserSearch
		if err := rows.Scan(&u.ID, &u.Name, &u.AvatarPath, &u.Role, &u.BadgeText); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// RecentUsers 最近加入的用户(管理后台「最近加入」)。
func (s *Store) RecentUsers(limit int) ([]User, error) {
	rows, err := s.DB.Query(`SELECT `+userCols+` FROM users ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// AdminUserRow 用户管理列表:基础信息 + 主题/回复/获赞计数,不暴露邮箱与口令。
type AdminUserRow struct {
	ID          int64
	Name        string
	AvatarPath  string
	Role        string
	BadgeText   sql.NullString
	CreatedAt   int64
	BannedUntil sql.NullInt64
	Threads     int64
	Replies     int64
	Likes       int64
	Points      int64
}

const adminUserSelect = `
	SELECT u.id, u.name, COALESCE(u.avatar_path,''), u.role, u.badge_text,
	       u.created_at, u.banned_until,
	       (SELECT COUNT(*) FROM threads t WHERE t.author_id = u.id),
	       (SELECT COUNT(*) FROM posts p WHERE p.author_id = u.id AND p.is_first = 0),
	       (SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id
	         WHERE p.author_id = u.id),
	       u.points
	FROM users u`

func scanAdminUser(row interface{ Scan(...any) error }) (*AdminUserRow, error) {
	u := &AdminUserRow{}
	err := row.Scan(&u.ID, &u.Name, &u.AvatarPath, &u.Role, &u.BadgeText,
		&u.CreatedAt, &u.BannedUntil, &u.Threads, &u.Replies, &u.Likes, &u.Points)
	return u, err
}

// CountAdminUsers 用户管理搜索总数。
func (s *Store) CountAdminUsers(q string) (int64, error) {
	var n int64
	q = strings.TrimSpace(q)
	if q == "" {
		err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
		return n, err
	}
	like := "%" + escapeLike(q) + "%"
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE name LIKE ? ESCAPE '\'`, like).Scan(&n)
	return n, err
}

// ListAdminUsers 用户管理列表:管理员/版主在前,其余按加入时间倒序。
func (s *Store) ListAdminUsers(q string, limit, offset int) ([]AdminUserRow, error) {
	args := []any{}
	where := ""
	if q = strings.TrimSpace(q); q != "" {
		where = ` WHERE u.name LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(q)+"%")
	}
	args = append(args, limit, offset)
	rows, err := s.DB.Query(adminUserSelect+where+`
		ORDER BY (u.role = 'admin') DESC, (u.role = 'mod') DESC, u.id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminUserRow
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (s *Store) GetUserByName(name string) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(
		`SELECT `+userCols+` FROM users WHERE name = ? COLLATE NOCASE`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByID 取完整用户信息(资料页/编辑资料用)。
func (s *Store) GetUserByID(id int64) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) CountUserThreads(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM threads WHERE author_id = ?`, userID).Scan(&n)
	return n, err
}

func (s *Store) CountUserReplies(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM posts WHERE author_id = ? AND is_first = 0`, userID).Scan(&n)
	return n, err
}

// SocialStats 个人主页社交统计:关注数 / 粉丝数 / 收到的赞。
type SocialStats struct {
	Following int64
	Followers int64
	Liked     int64 // 收到的赞(他人点赞该用户帖子)
}

func (s *Store) SocialStats(userID int64) (SocialStats, error) {
	var st SocialStats
	// 设置过的社交值作为「起点基准」:展示 = 设置值 + 之后的真实增量(只增不减);
	// 未设置则按真实统计聚合。
	err := s.DB.QueryRow(`
		SELECT
			CASE WHEN u.stat_following IS NOT NULL THEN
				u.stat_following + MAX(0, (SELECT COUNT(*) FROM follows WHERE follower_id = u.id) - u.stat_following_base)
			ELSE (SELECT COUNT(*) FROM follows WHERE follower_id = u.id) END,
			CASE WHEN u.stat_followers IS NOT NULL THEN
				u.stat_followers + MAX(0, (SELECT COUNT(*) FROM follows WHERE followee_id = u.id) - u.stat_followers_base)
			ELSE (SELECT COUNT(*) FROM follows WHERE followee_id = u.id) END,
			CASE WHEN u.stat_liked IS NOT NULL THEN
				u.stat_liked + MAX(0, (SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id
				 WHERE p.author_id = u.id) - u.stat_liked_base)
			ELSE (SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id
			 WHERE p.author_id = u.id) END
		FROM users u WHERE u.id = ?`, userID).Scan(&st.Following, &st.Followers, &st.Liked)
	return st, err
}

// SetSocialStats 后台设置社交数据起点基准;NullInt64 无效值表示恢复真实统计。
// 设置时记录当时的真实统计为基准,之后真实新增会继续累计到展示值上。
func (s *Store) SetSocialStats(userID int64, following, followers, liked sql.NullInt64) error {
	var rf, rfo, rl int64
	if err := s.DB.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM follows WHERE follower_id = ?),
			(SELECT COUNT(*) FROM follows WHERE followee_id = ?),
			(SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id
			 WHERE p.author_id = ?)`, userID, userID, userID).Scan(&rf, &rfo, &rl); err != nil {
		return err
	}
	arg := func(v sql.NullInt64, real int64) (any, int64) {
		if v.Valid {
			return v.Int64, real
		}
		return nil, 0
	}
	a1, b1 := arg(following, rf)
	a2, b2 := arg(followers, rfo)
	a3, b3 := arg(liked, rl)
	_, err := s.DB.Exec(`UPDATE users
		SET stat_following = ?, stat_following_base = ?,
		    stat_followers = ?, stat_followers_base = ?,
		    stat_liked = ?, stat_liked_base = ?
		WHERE id = ?`,
		a1, b1, a2, b2, a3, b3, userID)
	return err
}

// LikesReceived 用户实际收到的赞(不计后台覆盖,等级经验用它计算)。
func (s *Store) LikesReceived(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id
		  WHERE p.author_id = ?`, userID).Scan(&n)
	return n, err
}

// ListUserThreads 某用户发起过的主题(资料页「TA 的主题」)。
func (s *Store) ListUserThreads(userID int64, limit, offset int) ([]Thread, error) {
	rows, err := s.DB.Query(`SELECT `+threadCols+threadFrom+`
		WHERE t.author_id = ?
		ORDER BY t.is_pinned DESC, t.last_post_at DESC LIMIT ? OFFSET ?`,
		userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// UserActivity 是个人中心「回复 / 帖子」页的条目:一条回复,或主题本身。
type UserActivity struct {
	Kind         string // topic | reply
	ThreadID     int64
	PostID       sql.NullInt64
	ThreadTitle  string
	CategoryID   int64
	CategoryName string
	CreatedAt    int64
	Snippet      string // 回复正文预览(topic 为空)
}

// ListUserReplies 某用户写过的回复(资料页「TA 的回复」)。
func (s *Store) ListUserReplies(userID int64, limit, offset int) ([]UserActivity, error) {
	rows, err := s.DB.Query(`
		SELECT p.thread_id, p.id, t.title, c.id, c.name, p.created_at, p.content_md
		FROM posts p
		JOIN threads t ON t.id = p.thread_id
		JOIN categories c ON c.id = t.category_id
		WHERE p.author_id = ? AND p.is_first = 0
		ORDER BY p.created_at DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserActivity
	for rows.Next() {
		var a UserActivity
		if err := rows.Scan(&a.ThreadID, &a.PostID, &a.ThreadTitle,
			&a.CategoryID, &a.CategoryName, &a.CreatedAt, &a.Snippet); err != nil {
			return nil, err
		}
		a.Kind = "reply"
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListUserActivity 某用户的全部发言(主题+回复,按时间倒序;资料页「TA 的帖子」)。
func (s *Store) ListUserActivity(userID int64, limit, offset int) ([]UserActivity, error) {
	rows, err := s.DB.Query(`
		SELECT kind, thread_id, COALESCE(post_id, 0), title, cat_id, cat_name, created, COALESCE(snippet, '')
		FROM (
			SELECT 'topic' AS kind, t.id AS thread_id, NULL AS post_id, t.title AS title,
			       c.id AS cat_id, c.name AS cat_name, t.created_at AS created, NULL AS snippet
			FROM threads t JOIN categories c ON c.id = t.category_id
			WHERE t.author_id = ?
			UNION ALL
			SELECT 'reply' AS kind, p.thread_id, p.id, t.title, c.id, c.name, p.created_at, p.content_md
			FROM posts p
			JOIN threads t ON t.id = p.thread_id
			JOIN categories c ON c.id = t.category_id
			WHERE p.author_id = ? AND p.is_first = 0
		)
		ORDER BY created DESC LIMIT ? OFFSET ?`, userID, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserActivity
	for rows.Next() {
		var a UserActivity
		var postID int64
		if err := rows.Scan(&a.Kind, &a.ThreadID, &postID, &a.ThreadTitle,
			&a.CategoryID, &a.CategoryName, &a.CreatedAt, &a.Snippet); err != nil {
			return nil, err
		}
		if a.Kind == "reply" && postID > 0 {
			a.PostID = sql.NullInt64{Int64: postID, Valid: true}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LikedThread 我点赞过的文章(资料页「点赞」分区;点赞只作用于首帖文章)。
type LikedThread struct {
	ID           int64
	Title        string
	CategoryID   int64
	CategoryName string
	ReplyCount   int64
	ViewCount    int64
	LikeCount    int64 // 这篇文章收到的赞
	CreatedAt    int64 // 文章发布时间
	ActionAt     int64 // 我点赞的时间
}

// FavThread 我收藏的主题(资料页「收藏」分区)。
type FavThread struct {
	ID           int64
	Title        string
	CategoryID   int64
	CategoryName string
	ReplyCount   int64
	ViewCount    int64
	CreatedAt    int64 // 主题发布时间
	ActionAt     int64 // 我收藏的时间
}

// ListLikedThreads 我点赞过的文章列表(按点赞时间倒序)。
func (s *Store) ListLikedThreads(userID int64, limit, offset int) ([]LikedThread, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.title, c.id, c.name, t.post_count - 1, t.view_count,
		       (SELECT COUNT(*) FROM post_likes pl WHERE pl.post_id = p.id), t.created_at, pl.created_at
		FROM post_likes pl
		JOIN posts p ON p.id = pl.post_id AND p.is_first = 1
		JOIN threads t ON t.id = p.thread_id
		JOIN categories c ON c.id = t.category_id
		WHERE pl.user_id = ?
		ORDER BY pl.created_at DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LikedThread
	for rows.Next() {
		var it LikedThread
		if err := rows.Scan(&it.ID, &it.Title, &it.CategoryID, &it.CategoryName,
			&it.ReplyCount, &it.ViewCount, &it.LikeCount, &it.CreatedAt, &it.ActionAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountLikedThreads 我点赞过的文章总数。
func (s *Store) CountLikedThreads(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM post_likes pl
		JOIN posts p ON p.id = pl.post_id AND p.is_first = 1
		WHERE pl.user_id = ?`, userID).Scan(&n)
	return n, err
}

// ListFavThreads 我收藏的主题列表(按收藏时间倒序)。
func (s *Store) ListFavThreads(userID int64, limit, offset int) ([]FavThread, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.title, c.id, c.name, t.post_count - 1, t.view_count, t.created_at, f.created_at
		FROM favorites f
		JOIN threads t ON t.id = f.thread_id
		JOIN categories c ON c.id = t.category_id
		WHERE f.user_id = ?
		ORDER BY f.created_at DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FavThread
	for rows.Next() {
		var it FavThread
		if err := rows.Scan(&it.ID, &it.Title, &it.CategoryID, &it.CategoryName,
			&it.ReplyCount, &it.ViewCount, &it.CreatedAt, &it.ActionAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CountFavThreads 我收藏的主题总数。
func (s *Store) CountFavThreads(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM favorites WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// ToggleThreadLike 点赞/取消点赞一篇文章(点赞挂在首帖 post_likes 上)。
func (s *Store) ToggleThreadLike(threadID, userID int64) (bool, error) {
	var postID int64
	if err := s.DB.QueryRow(
		`SELECT id FROM posts WHERE thread_id = ? AND is_first = 1`, threadID).Scan(&postID); err != nil {
		return false, err
	}
	var exists int
	if err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM post_likes WHERE post_id = ? AND user_id = ?`, postID, userID).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		_, err := s.DB.Exec(`DELETE FROM post_likes WHERE post_id = ? AND user_id = ?`, postID, userID)
		return false, err
	}
	_, err := s.DB.Exec(
		`INSERT INTO post_likes (post_id, user_id, created_at) VALUES (?, ?, ?)`,
		postID, userID, time.Now().Unix())
	return true, err
}

// ToggleFavorite 收藏/取消收藏一个主题。
func (s *Store) ToggleFavorite(threadID, userID int64) (bool, error) {
	var exists int
	if err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM favorites WHERE thread_id = ? AND user_id = ?`, threadID, userID).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		_, err := s.DB.Exec(`DELETE FROM favorites WHERE thread_id = ? AND user_id = ?`, threadID, userID)
		return false, err
	}
	_, err := s.DB.Exec(
		`INSERT INTO favorites (thread_id, user_id, created_at) VALUES (?, ?, ?)`,
		threadID, userID, time.Now().Unix())
	return true, err
}

// ThreadReacts 帖子页反应数据:文章赞数/是否已赞、收藏数/是否已收藏。
func (s *Store) ThreadReacts(threadID, viewerID int64) (likeCount, favCount int64, liked, faved bool, err error) {
	err = s.DB.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM post_likes WHERE post_id =
				(SELECT id FROM posts WHERE thread_id = ? AND is_first = 1)),
			(SELECT COUNT(*) FROM favorites WHERE thread_id = ?)`, threadID, threadID).Scan(&likeCount, &favCount)
	if err != nil {
		return 0, 0, false, false, err
	}
	if viewerID > 0 {
		err = s.DB.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM post_likes pl
				JOIN posts p ON p.id = pl.post_id
				WHERE pl.user_id = ? AND p.thread_id = ? AND p.is_first = 1)`, viewerID, threadID).Scan(&liked)
		if err != nil {
			return 0, 0, false, false, err
		}
		err = s.DB.QueryRow(`
			SELECT EXISTS(SELECT 1 FROM favorites WHERE user_id = ? AND thread_id = ?)`,
			viewerID, threadID).Scan(&faved)
		if err != nil {
			return 0, 0, false, false, err
		}
	}
	return likeCount, favCount, liked, faved, nil
}

// PostLikes 某帖的点赞数与我是否已赞(回复点赞用;首帖也可用)。
type PostLikes struct {
	Count int64
	Liked bool
}

// PostLikeByID 查单帖点赞状态。
func (s *Store) PostLikeByID(postID, viewerID int64) (PostLikes, error) {
	var pl PostLikes
	var liked int64
	err := s.DB.QueryRow(`
		SELECT COUNT(*), COALESCE(MAX(CASE WHEN pl.user_id = ? THEN 1 ELSE 0 END), 0)
		FROM post_likes pl WHERE pl.post_id = ?`, viewerID, postID).Scan(&pl.Count, &liked)
	pl.Liked = liked != 0
	return pl, err
}

// PostLikesByThread 一次取整楼每帖点赞数与我是否已赞(帖子页渲染用)。
func (s *Store) PostLikesByThread(threadID, viewerID int64) (map[int64]PostLikes, error) {
	rows, err := s.DB.Query(`
		SELECT p.id, COUNT(pl.id),
		       COALESCE(MAX(CASE WHEN pl.user_id = ? THEN 1 ELSE 0 END), 0)
		FROM posts p LEFT JOIN post_likes pl ON pl.post_id = p.id
		WHERE p.thread_id = ? GROUP BY p.id`, viewerID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]PostLikes)
	for rows.Next() {
		var id int64
		var pl PostLikes
		var liked int64
		if err := rows.Scan(&id, &pl.Count, &liked); err != nil {
			return nil, err
		}
		pl.Liked = liked != 0
		out[id] = pl
	}
	return out, rows.Err()
}

// TogglePostLike 点赞/取消点赞某一楼(文章与回复均可;资料页「我的点赞」只记文章)。
func (s *Store) TogglePostLike(postID, userID int64) (bool, error) {
	var exists int
	if err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM post_likes WHERE post_id = ? AND user_id = ?`, postID, userID).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		_, err := s.DB.Exec(`DELETE FROM post_likes WHERE post_id = ? AND user_id = ?`, postID, userID)
		return false, err
	}
	_, err := s.DB.Exec(
		`INSERT INTO post_likes (post_id, user_id, created_at) VALUES (?, ?, ?)`,
		postID, userID, time.Now().Unix())
	return true, err
}

func (s *Store) UpdateUserBio(userID int64, bio string) error {
	_, err := s.DB.Exec(`UPDATE users SET bio = ? WHERE id = ?`, bio, userID)
	return err
}

// UpdateUserName 修改用户名;与既有用户重名(忽略大小写)时返回 ErrDuplicateName。
func (s *Store) UpdateUserName(userID int64, name string) error {
	_, err := s.DB.Exec(`UPDATE users SET name = ? WHERE id = ?`, name, userID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrDuplicateName
		}
		return err
	}
	return nil
}

// UpdateUserBadge 保存称号标签(badge NULL=跟随身份,”=隐藏,非空=自定义)。
func (s *Store) UpdateUserBadge(userID int64, badge sql.NullString) error {
	var v any
	if badge.Valid {
		v = badge.String
	}
	_, err := s.DB.Exec(`UPDATE users SET badge_text = ? WHERE id = ?`, v, userID)
	return err
}

// UpdateUserAvatar 记录头像的访问路径(形如 /uploads/12)。
func (s *Store) UpdateUserAvatar(userID int64, avatarPath string) error {
	_, err := s.DB.Exec(`UPDATE users SET avatar_path = ? WHERE id = ?`, avatarPath, userID)
	return err
}

// UpdateUserPassword 直接写入新密码哈希(管理员重置用)。
func (s *Store) UpdateUserPassword(userID int64, hash string) error {
	_, err := s.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

func (s *Store) CreateSession(userID int64, token, csrfToken string, ttl time.Duration) error {
	now := time.Now().Unix()
	_, err := s.DB.Exec(
		`INSERT INTO sessions (token, user_id, csrf_token, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		token, userID, csrfToken, now, now+int64(ttl.Seconds()))
	return err
}

// GetSessionUser 返回 (user, csrfToken);会话不存在/过期时返回 (nil, "", nil)。
func (s *Store) GetSessionUser(token string) (*User, string, error) {
	u := &User{}
	var csrf string
	now := time.Now().Unix()
	err := s.DB.QueryRow(`
		SELECT u.id, u.name, u.role, COALESCE(u.avatar_path,''), u.badge_text,
		       u.verify_kind, u.verify_title, u.level_override,
		       u.notify_scope, u.notify_freq, u.notify_dm,
		       u.points, u.exp_extra, u.checkin_bonus, u.bonus_until,
		       u.login_name, u.totp_enabled, s.csrf_token
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ?
		  AND (u.banned_until IS NULL OR u.banned_until <= ?)`, token, now, now).
		Scan(&u.ID, &u.Name, &u.Role, &u.AvatarPath, &u.BadgeText,
			&u.VerifyKind, &u.VerifyTitle, &u.LevelOverride,
			&u.NotifyScope, &u.NotifyFreq, &u.NotifyDM,
			&u.Points, &u.ExpExtra, &u.CheckinBonus, &u.BonusUntil,
			&u.LoginName, &u.TOTPEnabled, &csrf)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return u, csrf, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteUserSessions 踢掉该用户的全部会话(改密码/重置密码后调用)。
func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// ---------- 邮箱令牌(注册验证 / 找回密码) ----------

// EmailToken 一次性邮件令牌。
type EmailToken struct {
	Token     string
	UserID    int64
	Kind      string // verify | reset
	Email     string
	ExpiresAt int64
}

// CreateEmailToken 写入一枚令牌;同一用户同类型的旧令牌一并作废,避免旧链接还能用。
// token 由调用方生成(随机源在 auth 包,db 不反向依赖它)。
func (s *Store) CreateEmailToken(token string, userID int64, kind, email string, ttl time.Duration) error {
	now := time.Now().Unix()
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM email_tokens WHERE user_id = ? AND kind = ? AND used_at IS NULL`,
			userID, kind); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO email_tokens
			(token, user_id, kind, email, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			token, userID, kind, email, now, now+int64(ttl.Seconds()))
		return err
	})
}

// EmailTokenOf 取未使用且未过期的令牌;不存在/已用/过期都返回 nil。
func (s *Store) EmailTokenOf(token, kind string) (*EmailToken, error) {
	t := &EmailToken{}
	err := s.DB.QueryRow(`
		SELECT token, user_id, kind, email, expires_at FROM email_tokens
		WHERE token = ? AND kind = ? AND used_at IS NULL AND expires_at > ?`,
		token, kind, time.Now().Unix()).Scan(&t.Token, &t.UserID, &t.Kind, &t.Email, &t.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ConsumeEmailVerify 验证通过:写回邮箱 + 打上验证时间,并作废该令牌。
func (s *Store) ConsumeEmailVerify(token string, userID int64, email string) error {
	now := time.Now().Unix()
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`UPDATE users SET email = ?, email_verified_at = ? WHERE id = ?`,
			email, now, userID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE email_tokens SET used_at = ? WHERE token = ?`, now, token)
		return err
	})
}

// ConsumeEmailReset 重置密码:换哈希 + 作废令牌 + 踢掉全部会话。
func (s *Store) ConsumeEmailReset(token string, userID int64, hash string) error {
	now := time.Now().Unix()
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE email_tokens SET used_at = ? WHERE token = ?`, now, token); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	})
}

// MarkEmailTokenUsed 作废一枚令牌(两步验证的一次性令牌用它)。
func (s *Store) MarkEmailTokenUsed(token string) error {
	_, err := s.DB.Exec(`UPDATE email_tokens SET used_at = ? WHERE token = ?`,
		time.Now().Unix(), token)
	return err
}

// MarkEmailVerified 直接把邮箱标为已验证(管理员后台建号时用,视同管理员已核实)。
func (s *Store) MarkEmailVerified(userID int64) error {
	_, err := s.DB.Exec(`UPDATE users SET email_verified_at = ? WHERE id = ?`,
		time.Now().Unix(), userID)
	return err
}

// VacuumEmailTokens 顺手清理过期令牌(发信时调用即可,无需定时器)。
func (s *Store) VacuumEmailTokens() {
	s.DB.Exec(`DELETE FROM email_tokens WHERE expires_at <= ?`, time.Now().Unix())
}

// ---------- categories ----------

func (s *Store) ListCategories() ([]Category, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, c.slug, c.name, COALESCE(c.description,''),
		       (SELECT COUNT(*) FROM threads t WHERE t.category_id = c.id) AS thread_count,
		       (SELECT MAX(t.last_post_at) FROM threads t WHERE t.category_id = c.id) AS last_post_at
		FROM categories c
		ORDER BY c.position, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.ThreadCount, &c.LastPostAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetCategoryBySlug(slug string) (*Category, error) {
	c := &Category{}
	err := s.DB.QueryRow(
		`SELECT id, slug, name, COALESCE(description,'') FROM categories WHERE slug = ?`, slug).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) GetCategoryByID(id int64) (*Category, error) {
	c := &Category{}
	err := s.DB.QueryRow(
		`SELECT id, slug, name, COALESCE(description,'') FROM categories WHERE id = ?`, id).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Store) CreateCategory(slug, name, description string) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO categories (slug, name, description, position) VALUES (?, ?, ?, (SELECT COALESCE(MAX(position),0)+10 FROM categories))`,
		slug, name, description)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---------- threads ----------

const threadCols = `
	t.id, t.category_id, c.slug, c.name, t.author_id, u.name, COALESCE(u.avatar_path,''),
	u.role, u.verify_kind, u.verify_title, t.title, t.kind, t.min_level, t.price,
	t.is_pinned, t.is_locked,
	t.created_at, t.last_post_at, t.view_count, t.post_count,
	COALESCE((SELECT lu.name FROM posts lp JOIN users lu ON lu.id = lp.author_id
	          WHERE lp.thread_id = t.id ORDER BY lp.id DESC LIMIT 1), u.name),
	(SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id
	  WHERE p.thread_id = t.id AND p.is_first = 1),
	(SELECT COUNT(*) FROM favorites f WHERE f.thread_id = t.id)`

const threadFrom = ` FROM threads t
	JOIN users u ON u.id = t.author_id
	JOIN categories c ON c.id = t.category_id `

func scanThread(row interface{ Scan(...any) error }) (*Thread, error) {
	t := &Thread{}
	var pinned, locked int64
	err := row.Scan(&t.ID, &t.CategoryID, &t.CategorySlug, &t.CategoryName,
		&t.AuthorID, &t.AuthorName, &t.AuthorAvatar,
		&t.AuthorRole, &t.AuthorVerifyKind, &t.AuthorVerify, &t.Title,
		&t.Kind, &t.MinLevel, &t.Price, &pinned, &locked,
		&t.CreatedAt, &t.LastPostAt, &t.ViewCount, &t.PostCount, &t.LastPostBy,
		&t.LikeCount, &t.FavCount)
	if err != nil {
		return nil, err
	}
	t.IsPinned = pinned != 0
	t.IsLocked = locked != 0
	return t, nil
}

func (s *Store) ListThreads(catID int64, limit, offset int) ([]Thread, error) {
	rows, err := s.DB.Query(`SELECT `+threadCols+threadFrom+
		`WHERE t.category_id = ? ORDER BY t.is_pinned DESC, t.last_post_at DESC LIMIT ? OFFSET ?`,
		catID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) CountThreads(catID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM threads WHERE category_id = ?`, catID).Scan(&n)
	return n, err
}

// ListFeedThreads 首页帖子流:可选分类 + 排序(最新/热帖)。
func (s *Store) ListFeedThreads(catSlug string, hot bool, limit, offset int) ([]Thread, error) {
	q := `SELECT ` + threadCols + threadFrom
	var args []any
	if catSlug != "" {
		q += ` WHERE c.slug = ?`
		args = append(args, catSlug)
	}
	if hot {
		q += ` ORDER BY t.is_pinned DESC, t.post_count DESC, t.view_count DESC, t.last_post_at DESC`
	} else {
		q += ` ORDER BY t.is_pinned DESC, t.last_post_at DESC`
	}
	q += ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListAdminThreads 内容管理列表:可选的标题/作者关键词 q + 版块过滤,
// 置顶优先、按最后回复倒序(管理视角的排序,与前台信息流一致好对照)。
func (s *Store) ListAdminThreads(q string, catID int64, limit, offset int) ([]Thread, error) {
	q = strings.TrimSpace(q)
	cond := ` WHERE 1=1`
	var args []any
	if q != "" {
		pat := "%" + escapeLike(q) + "%"
		cond += ` AND (t.title LIKE ? ESCAPE '\' OR u.name LIKE ? ESCAPE '\')`
		args = append(args, pat, pat)
	}
	if catID > 0 {
		cond += ` AND t.category_id = ?`
		args = append(args, catID)
	}
	args = append(args, limit, offset)
	rows, err := s.DB.Query(`SELECT `+threadCols+threadFrom+cond+`
		ORDER BY t.is_pinned DESC, t.last_post_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) CountAdminThreads(q string, catID int64) (int64, error) {
	q = strings.TrimSpace(q)
	cond := ` WHERE 1=1`
	var args []any
	if q != "" {
		pat := "%" + escapeLike(q) + "%"
		cond += ` AND (t.title LIKE ? ESCAPE '\' OR u.name LIKE ? ESCAPE '\')`
		args = append(args, pat, pat)
	}
	if catID > 0 {
		cond += ` AND t.category_id = ?`
		args = append(args, catID)
	}
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM threads t JOIN users u ON u.id = t.author_id`+cond, args...).Scan(&n)
	return n, err
}

func (s *Store) CountFeedThreads(catSlug string) (int64, error) {
	q := `SELECT COUNT(*) FROM threads t JOIN categories c ON c.id = t.category_id`
	var args []any
	if catSlug != "" {
		q += ` WHERE c.slug = ?`
		args = append(args, catSlug)
	}
	var n int64
	err := s.DB.QueryRow(q, args...).Scan(&n)
	return n, err
}

// escapeLike 转义 LIKE 通配符,让用户输入按字面匹配。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// ftsPhrase 把用户输入包成 FTS5 短语查询(引号成对转义),按字面连续子串匹配。
func ftsPhrase(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// SearchThreads 全文搜索主题:标题或任一回复正文命中都返回主题行。
// ≥3 字符走 FTS5 trigram(无空格中文也可做子串匹配);短查询回退 LIKE,
// FTS 语法无法解析的输入同样兜底 LIKE,保证查询不因输入而 500。
func (s *Store) SearchThreads(q string, limit, offset int) ([]Thread, error) {
	var rows *sql.Rows
	var err error
	if utf8.RuneCountInString(q) >= 3 {
		rows, err = s.DB.Query(`SELECT `+threadCols+threadFrom+`
			WHERE t.id IN (SELECT DISTINCT f.thread_id FROM thread_docs f WHERE f.text MATCH ?)
			ORDER BY t.is_pinned DESC, t.last_post_at DESC LIMIT ? OFFSET ?`,
			ftsPhrase(q), limit, offset)
	}
	if rows == nil { // 短查询或 FTS 出错 → LIKE
		pat := "%" + escapeLike(q) + "%"
		rows, err = s.DB.Query(`SELECT `+threadCols+threadFrom+`
			WHERE t.title LIKE ? ESCAPE '\'
			   OR EXISTS (SELECT 1 FROM posts p WHERE p.thread_id = t.id AND p.content_md LIKE ? ESCAPE '\')
			ORDER BY t.is_pinned DESC, t.last_post_at DESC LIMIT ? OFFSET ?`,
			pat, pat, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) CountSearchThreads(q string) (int64, error) {
	var n int64
	var err error
	useFTS := utf8.RuneCountInString(q) >= 3
	if useFTS {
		err = s.DB.QueryRow(`
			SELECT COUNT(DISTINCT t.id) FROM threads t
			JOIN thread_docs f ON f.thread_id = t.id
			WHERE f.text MATCH ?`, ftsPhrase(q)).Scan(&n)
	}
	if err != nil || !useFTS {
		pat := "%" + escapeLike(q) + "%"
		err = s.DB.QueryRow(`
			SELECT COUNT(*) FROM threads t
			WHERE t.title LIKE ? ESCAPE '\'
			   OR EXISTS (SELECT 1 FROM posts p WHERE p.thread_id = t.id AND p.content_md LIKE ? ESCAPE '\')`,
			pat, pat).Scan(&n)
	}
	return n, err
}

// DeleteCategory 删除版块;版块内的主题/回复由外键级联清掉,调用方需先确认过。
// DeleteCategory 删版块。版块里的主题靠外键级联一起消失,所以要先把里面
// 未开奖的抽奖退款干净(同 DeleteThread 的理由)。
func (s *Store) DeleteCategory(id int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT l.thread_id FROM lotteries l
			JOIN threads t ON t.id = l.thread_id
			WHERE t.category_id = ? AND l.status = 'open'`, id)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var tid int64
			if err := rows.Scan(&tid); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, tid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, tid := range ids {
			if err := refundLotteryTx(tx, tid, "版块已删除"); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`DELETE FROM categories WHERE id = ?`, id)
		return err
	})
}

// CountCategories 版块总数(删除时用于保住最后一个版块)。
func (s *Store) CountCategories() (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&n)
	return n, err
}

// MoveThreadsAndDeleteCategory 把版块里的主题整体迁到 toID,然后删掉这个版块。
// 一个事务里做完,避免迁一半失败留下半空版块。
func (s *Store) MoveThreadsAndDeleteCategory(fromID, toID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE threads SET category_id = ? WHERE category_id = ?`,
			toID, fromID); err != nil {
			return err
		}
		// 版主的管辖登记跟着版块一起消失(category_mods 有级联,这里显式删更清楚)
		if _, err := tx.Exec(`DELETE FROM category_mods WHERE category_id = ?`, fromID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM categories WHERE id = ?`, fromID)
		return err
	})
}

func (s *Store) GetThread(id int64) (*Thread, error) {
	t, err := scanThread(s.DB.QueryRow(`SELECT `+threadCols+threadFrom+`WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// NewThread 是建帖时的可选设置(类型 + 阅读门槛 + 抽奖参数)。
type NewThread struct {
	Kind     string // normal | lottery
	MinLevel int    // 观看门槛等级
	Price    int64  // 观看需支付积分
	// 以下仅 kind=lottery 时有意义
	Prize      string
	Winners    int
	Stake      int64
	PayKind    string // item=实物奖(楼主自己发) | points=积分奖(平台自动发)
	Sponsor    int64  // 楼主自掏的奖池积分,建帖时预扣
	MaxEntries int    // 参与人数上限,0=不限
	DrawAt     int64  // 到点自动开奖(0=只能手动开)
}

// CreateThread 建主题 + 首帖(+抽奖设置),同一事务里维护计数。
func (s *Store) CreateThread(catID, authorID int64, title, contentMD, contentHTML string, opt NewThread) (int64, error) {
	var threadID int64
	now := time.Now().Unix()
	kind := opt.Kind
	if kind != "lottery" {
		kind = "normal"
	}
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO threads (category_id, author_id, title, kind, min_level, price,
			                      created_at, last_post_at, post_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			catID, authorID, title, kind, opt.MinLevel, opt.Price, now, now)
		if err != nil {
			return err
		}
		threadID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(
			`INSERT INTO posts (thread_id, author_id, content_md, content_html, is_first, created_at)
			 VALUES (?, ?, ?, ?, 1, ?)`, threadID, authorID, contentMD, contentHTML, now); err != nil {
			return err
		}
		if kind == "lottery" {
			payKind := opt.PayKind
			if payKind != "points" {
				payKind = "item"
			}
			sponsor := opt.Sponsor
			if payKind != "points" {
				sponsor = 0 // 实物奖没有平台奖池,别让表单里的残值漏进来
			}
			var drawArg any
			if opt.DrawAt > 0 {
				drawArg = opt.DrawAt
			}
			if _, err = tx.Exec(
				`INSERT INTO lotteries (thread_id, prize, winners, stake, pool, pay_kind,
				                        sponsor, max_entries, draw_at, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				threadID, opt.Prize, opt.Winners, opt.Stake, sponsor, payKind,
				sponsor, opt.MaxEntries, drawArg, now); err != nil {
				return err
			}
			// 楼主出的奖池当场扣款:余额不足会返回 ErrNotEnoughPoints,整个建帖事务回滚
			if sponsor > 0 {
				if err := addPointsTx(tx, authorID, -sponsor, PointLotFund,
					"抽奖出奖预扣", threadID, 0); err != nil {
					return err
				}
			}
		}
		return err
	})
	return threadID, err
}

// ---------- 付费帖解锁 ----------

// ThreadUnlocked 该用户是否已解锁这个付费帖。
func (s *Store) ThreadUnlocked(threadID, userID int64) (bool, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM thread_unlocks WHERE thread_id = ? AND user_id = ?`,
		threadID, userID).Scan(&n)
	return n > 0, err
}

// UnlockThread 支付积分解锁付费帖:扣读者、给作者、记两笔流水、写解锁记录,一个事务。
// 已解锁时返回 false 且不重复扣费。
func (s *Store) UnlockThread(threadID, userID, authorID, price int64, note string) (bool, error) {
	done := false
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO thread_unlocks (thread_id, user_id, paid, created_at)
			 VALUES (?, ?, ?, ?)`, threadID, userID, price, time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // 已解锁过
		}
		if price > 0 {
			if err := addPointsTx(tx, userID, -price, PointUnlockOut, note, threadID, authorID); err != nil {
				return err
			}
			if err := addPointsTx(tx, authorID, price, PointUnlockIn, note, threadID, userID); err != nil {
				return err
			}
		}
		done = true
		return nil
	})
	return done, err
}

// CountThreadUnlocks 付费帖的解锁人数(作者可见的销量)。
func (s *Store) CountThreadUnlocks(threadID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM thread_unlocks WHERE thread_id = ?`, threadID).Scan(&n)
	return n, err
}

// ---------- 抽奖 ----------

// Lottery 抽奖设置与进度。
type Lottery struct {
	ThreadID   int64
	Prize      string
	Winners    int
	Stake      int64
	Pool       int64
	Status     string // open | drawn | canceled(无人参与,奖池已退回)
	DrawnAt    sql.NullInt64
	PayKind    string // item | points
	Sponsor    int64
	MaxEntries int
	DrawAt     sql.NullInt64
	Entries    int64 // 参与人数(查询时一并带出)
}

func (l *Lottery) Drawn() bool { return l != nil && l.Status == "drawn" }

// Canceled 无人参与被关掉(奖池已退回楼主)。
func (l *Lottery) Canceled() bool { return l != nil && l.Status == "canceled" }

// Over 抽奖是不是已经结束(开过奖或被关掉),用来决定还能不能参与。
func (l *Lottery) Over() bool { return l.Drawn() || l.Canceled() }

// IsPoints 是不是积分奖 —— 只有这种平台会自动把奖池发出去。
func (l *Lottery) IsPoints() bool { return l != nil && l.PayKind == "points" }

// Full 参与人数是否已达上限(先来后到,满了后来的人回复照发但不进名单)。
func (l *Lottery) Full() bool {
	return l != nil && l.MaxEntries > 0 && l.Entries >= int64(l.MaxEntries)
}

// MaxWinners 实际最多能有几个中奖者。积分奖每人至少 1 分,所以中奖人数
// 不可能超过奖池总额;Winners 为 0 表示「不设人数」= 参与者全员分。
func (l *Lottery) MaxWinners() int {
	n := l.Winners
	if n <= 0 || int64(n) > l.Entries {
		n = int(l.Entries)
	}
	if l.IsPoints() && int64(n) > l.Pool {
		n = int(l.Pool)
	}
	return n
}

// LotteryEntry 一条参与记录。
type LotteryEntry struct {
	UserID     int64
	UserName   string
	UserAvatar string
	Stake      int64
	Won        bool
	Prize      int64
	CreatedAt  int64
}

// GetLottery 取某主题的抽奖(不是抽奖帖返回 nil)。
func (s *Store) GetLottery(threadID int64) (*Lottery, error) {
	l := &Lottery{}
	err := s.DB.QueryRow(`
		SELECT thread_id, prize, winners, stake, pool, status, drawn_at,
		       pay_kind, sponsor, max_entries, draw_at,
		       (SELECT COUNT(*) FROM lottery_entries e WHERE e.thread_id = l.thread_id)
		FROM lotteries l WHERE thread_id = ?`, threadID).
		Scan(&l.ThreadID, &l.Prize, &l.Winners, &l.Stake, &l.Pool, &l.Status, &l.DrawnAt,
			&l.PayKind, &l.Sponsor, &l.MaxEntries, &l.DrawAt, &l.Entries)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// DueLotteries 到点该自动开奖的抽奖帖 id(巡检用)。
func (s *Store) DueLotteries(now int64) ([]int64, error) {
	rows, err := s.DB.Query(
		`SELECT thread_id FROM lotteries
		  WHERE status = 'open' AND draw_at IS NOT NULL AND draw_at <= ?
		  ORDER BY thread_id LIMIT 100`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CancelLottery 关掉一场开不了奖的抽奖(没人参与),楼主预扣的奖池原路退回。
// 参与者投入的 stake 不在这里退 —— 没人参与就没有 stake。幂等。
func (s *Store) CancelLottery(threadID int64) (bool, error) {
	done := false
	err := s.withTx(func(tx *sql.Tx) error {
		var status string
		var sponsor, authorID int64
		if err := tx.QueryRow(`SELECT l.status, l.sponsor, t.author_id
			FROM lotteries l JOIN threads t ON t.id = l.thread_id
			WHERE l.thread_id = ?`, threadID).Scan(&status, &sponsor, &authorID); err != nil {
			return err
		}
		if status != "open" {
			return nil
		}
		res, err := tx.Exec(`UPDATE lotteries SET status = 'canceled', drawn_at = ?
			WHERE thread_id = ? AND status = 'open'`, time.Now().Unix(), threadID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if sponsor > 0 {
			if err := addPointsTx(tx, authorID, sponsor, PointLotBack,
				"抽奖无人参与,奖池退回", threadID, 0); err != nil {
				return err
			}
		}
		done = true
		return nil
	})
	return done, err
}

// JoinedLottery 该用户是否已参与。
func (s *Store) JoinedLottery(threadID, userID int64) (bool, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM lottery_entries WHERE thread_id = ? AND user_id = ?`,
		threadID, userID).Scan(&n)
	return n > 0, err
}

// JoinLottery 参与抽奖(回复时自动调用):写参与记录,stake>0 时扣积分进奖池。
// 已参与、已开奖、名额已满都返回 false —— 这三种情况回复照发,只是不进名单
// (把整条回复拦掉太狠)。积分不足返回 ErrNotEnoughPoints。
func (s *Store) JoinLottery(threadID, userID, stake int64, note string) (bool, error) {
	joined := false
	err := s.withTx(func(tx *sql.Tx) error {
		var status string
		var maxEntries int64
		if err := tx.QueryRow(`SELECT status, max_entries FROM lotteries WHERE thread_id = ?`,
			threadID).Scan(&status, &maxEntries); err != nil {
			return err
		}
		if status != "open" {
			return nil
		}
		// 名额在事务里复查,不然并发下会挤进第 31 个人(跟商城库存同一个套路)
		if maxEntries > 0 {
			var n int64
			if err := tx.QueryRow(`SELECT COUNT(*) FROM lottery_entries WHERE thread_id = ?`,
				threadID).Scan(&n); err != nil {
				return err
			}
			if n >= maxEntries {
				return nil
			}
		}
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO lottery_entries (thread_id, user_id, stake, created_at)
			 VALUES (?, ?, ?, ?)`, threadID, userID, stake, time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if stake > 0 {
			if err := addPointsTx(tx, userID, -stake, PointStake, note, threadID, 0); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE lotteries SET pool = pool + ? WHERE thread_id = ?`,
				stake, threadID); err != nil {
				return err
			}
		}
		joined = true
		return nil
	})
	return joined, err
}

// ListLotteryEntries 参与名单(开奖后中奖者排在前面)。
func (s *Store) ListLotteryEntries(threadID int64, limit int) ([]LotteryEntry, error) {
	rows, err := s.DB.Query(`
		SELECT e.user_id, u.name, COALESCE(u.avatar_path,''), e.stake, e.won, e.prize, e.created_at
		FROM lottery_entries e JOIN users u ON u.id = e.user_id
		WHERE e.thread_id = ?
		ORDER BY e.won DESC, e.created_at LIMIT ?`, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LotteryEntry
	for rows.Next() {
		var e LotteryEntry
		var won int64
		if err := rows.Scan(&e.UserID, &e.UserName, &e.UserAvatar, &e.Stake, &won, &e.Prize, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Won = won != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// LotteryEntryIDs 参与者 id 列表(开奖时抽取用)。
func (s *Store) LotteryEntryIDs(threadID int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM lottery_entries WHERE thread_id = ?`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CloseLottery 开奖:标记中奖者、按份额发放奖池积分、把抽奖置为已开奖。
// prizes 与 winners 一一对应(可为 0,即纯实物奖品)。幂等:已开奖直接返回 false。
func (s *Store) CloseLottery(threadID int64, winners []int64, prizes []int64) (bool, error) {
	ok := false
	err := s.withTx(func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRow(`SELECT status FROM lotteries WHERE thread_id = ?`, threadID).Scan(&status); err != nil {
			return err
		}
		if status != "open" {
			return nil
		}
		now := time.Now().Unix()
		for i, uid := range winners {
			prize := int64(0)
			if i < len(prizes) {
				prize = prizes[i]
			}
			if _, err := tx.Exec(
				`UPDATE lottery_entries SET won = 1, prize = ? WHERE thread_id = ? AND user_id = ?`,
				prize, threadID, uid); err != nil {
				return err
			}
			if prize > 0 {
				if err := addPointsTx(tx, uid, prize, PointWin, "抽奖中奖", threadID, 0); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(
			`UPDATE lotteries SET status = 'drawn', drawn_at = ? WHERE thread_id = ?`,
			now, threadID); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}

// ---------- posts ----------

const postCols = `
	p.id, p.thread_id, p.author_id, u.name, COALESCE(u.avatar_path,''), u.role, u.badge_text,
	u.verify_kind, u.verify_title,
	p.content_md, p.content_html,
	p.is_first, p.created_at, p.edited_at`

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	p := &Post{}
	var first int64
	err := row.Scan(&p.ID, &p.ThreadID, &p.AuthorID, &p.AuthorName, &p.AuthorAvatar,
		&p.AuthorRole, &p.AuthorBadge, &p.AuthorVerifyKind, &p.AuthorVerify,
		&p.ContentMD, &p.ContentHTML, &first, &p.CreatedAt, &p.EditedAt)
	if err != nil {
		return nil, err
	}
	p.IsFirst = first != 0
	return p, nil
}

// GetFirstPost 取主题首帖(编辑主题时作为正文载体)。
func (s *Store) GetFirstPost(threadID int64) (*Post, error) {
	p, err := scanPost(s.DB.QueryRow(`SELECT `+postCols+
		` FROM posts p JOIN users u ON u.id = p.author_id
		  WHERE p.thread_id = ? AND p.is_first = 1`, threadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// CountPostsUpTo 统计 id <= postID 的帖子数,用于计算编辑后跳转所在页。
func (s *Store) CountPostsUpTo(threadID, postID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM posts WHERE thread_id = ? AND id <= ?`, threadID, postID).Scan(&n)
	return n, err
}

func (s *Store) ListPosts(threadID int64, limit, offset int) ([]Post, error) {
	rows, err := s.DB.Query(`SELECT `+postCols+
		` FROM posts p JOIN users u ON u.id = p.author_id
		  WHERE p.thread_id = ? ORDER BY p.id LIMIT ? OFFSET ?`, threadID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) CountPosts(threadID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE thread_id = ?`, threadID).Scan(&n)
	return n, err
}

// GetPost 取单帖,用于删除前的权限判断。
func (s *Store) GetPost(id int64) (*Post, error) {
	p, err := scanPost(s.DB.QueryRow(`SELECT `+postCols+
		` FROM posts p JOIN users u ON u.id = p.author_id WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// CreatePost 追加回复并维护主题计数。
func (s *Store) CreatePost(threadID, authorID int64, contentMD, contentHTML string) (int64, error) {
	var postID int64
	now := time.Now().Unix()
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO posts (thread_id, author_id, content_md, content_html, created_at)
			 VALUES (?, ?, ?, ?, ?)`, threadID, authorID, contentMD, contentHTML, now)
		if err != nil {
			return err
		}
		postID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`UPDATE threads SET post_count = post_count + 1, last_post_at = ? WHERE id = ?`, now, threadID)
		return err
	})
	return postID, err
}

// UpdatePost 更新单帖正文并打上编辑时间戳(编辑入口只允许改 content)。
func (s *Store) UpdatePost(postID int64, contentMD, contentHTML string) error {
	_, err := s.DB.Exec(
		`UPDATE posts SET content_md = ?, content_html = ?, edited_at = ? WHERE id = ?`,
		contentMD, contentHTML, time.Now().Unix(), postID)
	return err
}

// UpdateThreadTitle 更新主题标题(随首帖编辑一起提交)。
func (s *Store) UpdateThreadTitle(threadID int64, title string) error {
	_, err := s.DB.Exec(`UPDATE threads SET title = ? WHERE id = ?`, title, threadID)
	return err
}

func (s *Store) SetThreadPinned(threadID int64, pinned bool) error {
	_, err := s.DB.Exec(`UPDATE threads SET is_pinned = ? WHERE id = ?`, pinned, threadID)
	return err
}

func (s *Store) SetThreadLocked(threadID int64, locked bool) error {
	_, err := s.DB.Exec(`UPDATE threads SET is_locked = ? WHERE id = ?`, locked, threadID)
	return err
}

func (s *Store) SetUserRole(userID int64, role string) error {
	_, err := s.DB.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, userID)
	return err
}

// VerifyRequest 认证申请(官方/厂商/作者),管理员通过后写入 users.verify_kind/verify_title。
type VerifyRequest struct {
	ID             int64
	UserID         int64
	Kind           string // 官方 | 厂商 | 作者(兼容旧数据:官号/认证作者)
	Subject        string // 认证文案:具体对象/创作方向(申请人自述)
	Note           string // 补充说明
	Status         string // pending | approved | rejected
	CreatedAt      int64
	HandledAt      sql.NullInt64
	HandledBy      sql.NullInt64
	UserName       string
	UserRole       string
	UserAvatar     sql.NullString
	UserVerify     sql.NullString
	UserVerifyKind sql.NullString // 申请人当前认证分类(审批列表头像 V 用)
	HandledByName  sql.NullString
}

// VerifiedUser 已认证成员(后台认证页的「当前已认证」列表)。
type VerifiedUser struct {
	ID         int64
	Name       string
	AvatarPath string
	Role       string
	Kind       sql.NullString
	Title      sql.NullString
}

// ListVerifiedUsers 列出所有带认证的成员(不含只靠身份自动显示的管理员/版主)。
func (s *Store) ListVerifiedUsers() ([]VerifiedUser, error) {
	rows, err := s.DB.Query(`
		SELECT id, name, COALESCE(avatar_path,''), role, verify_kind, verify_title
		FROM users
		WHERE (verify_kind IS NOT NULL AND verify_kind <> '')
		   OR (verify_title IS NOT NULL AND verify_title <> '')
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifiedUser
	for rows.Next() {
		var v VerifiedUser
		if err := rows.Scan(&v.ID, &v.Name, &v.AvatarPath, &v.Role, &v.Kind, &v.Title); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateVerifyRequest 提交认证申请;同一用户存在 pending 时返回 false。
func (s *Store) CreateVerifyRequest(userID int64, kind, subject, note string) (bool, error) {
	var n int64
	if err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM verify_requests WHERE user_id = ? AND status = 'pending'`,
		userID).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := s.DB.Exec(`INSERT INTO verify_requests
		(user_id, kind, subject, note, status, created_at)
		VALUES (?, ?, ?, ?, 'pending', ?)`,
		userID, kind, subject, note, time.Now().Unix())
	return err == nil, err
}

// ListVerifyRequests 后台申请列表:待审在前,其余按提交时间倒序。
func (s *Store) ListVerifyRequests() ([]VerifyRequest, error) {
	rows, err := s.DB.Query(`
		SELECT vr.id, vr.user_id, vr.kind, vr.subject, vr.note, vr.status,
		       vr.created_at, vr.handled_at, vr.handled_by,
		       u.name, u.role, u.avatar_path, u.verify_kind, u.verify_title, h.name
		FROM verify_requests vr
		JOIN users u ON u.id = vr.user_id
		LEFT JOIN users h ON h.id = vr.handled_by
		ORDER BY (vr.status = 'pending') DESC, vr.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifyRequest
	for rows.Next() {
		var r VerifyRequest
		if err := rows.Scan(&r.ID, &r.UserID, &r.Kind, &r.Subject, &r.Note, &r.Status,
			&r.CreatedAt, &r.HandledAt, &r.HandledBy,
			&r.UserName, &r.UserRole, &r.UserAvatar, &r.UserVerifyKind, &r.UserVerify,
			&r.HandledByName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingVerify 某用户是否有待审申请。
func (s *Store) PendingVerify(userID int64) (bool, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM verify_requests WHERE user_id = ? AND status = 'pending'`,
		userID).Scan(&n)
	return n > 0, err
}

// ResolveVerify 处理申请:通过则把分类/文案写进用户并**删掉这条申请**
// (认证结果已经落在 users 上,列表里再留一条已通过记录没有意义);
// 拒绝则留档为 rejected,便于知道拒过谁。幂等:已处理的再次操作直接跳过。
func (s *Store) ResolveVerify(reqID, adminID int64, approve bool) error {
	return s.withTx(func(tx *sql.Tx) error {
		var status, kind, subject string
		var userID int64
		err := tx.QueryRow(`SELECT status, kind, subject, user_id FROM verify_requests WHERE id = ?`,
			reqID).Scan(&status, &kind, &subject, &userID)
		if err != nil {
			return err
		}
		if status != "pending" {
			return nil
		}
		if !approve {
			if _, err := tx.Exec(`UPDATE verify_requests
				SET status = 'rejected', handled_at = ?, handled_by = ? WHERE id = ?`,
				time.Now().Unix(), adminID, reqID); err != nil {
				return err
			}
		}
		if approve {
			if _, err := tx.Exec(`UPDATE users SET verify_kind = ?, verify_title = ? WHERE id = ?`,
				kind, subject, userID); err != nil {
				return err
			}
			_, err := tx.Exec(`DELETE FROM verify_requests WHERE id = ?`, reqID)
			return err
		}
		return nil
	})
}

// SetVerify 设置用户认证:kind=分类(官方/厂商/作者),title=显示文案。
// kind 与 title 都为空表示取消认证(管理员/版主回到按身份自动显示)。
func (s *Store) SetVerify(userID int64, kind, title string) error {
	kind = strings.TrimSpace(kind)
	title = strings.TrimSpace(title)
	var k, t any
	if kind != "" {
		k = kind
	}
	if title != "" {
		t = title
	}
	if kind == "" && title == "" {
		_, err := s.DB.Exec(`UPDATE users SET verify_kind = NULL, verify_title = NULL WHERE id = ?`, userID)
		return err
	}
	_, err := s.DB.Exec(`UPDATE users SET verify_kind = ?, verify_title = ? WHERE id = ?`, k, t, userID)
	return err
}

// SetLevelOverride 管理员手动指定等级(0..6);lvValid=false 表示回到按经验自动。
func (s *Store) SetLevelOverride(userID int64, lv int, set bool) error {
	if !set {
		_, err := s.DB.Exec(`UPDATE users SET level_override = NULL WHERE id = ?`, userID)
		return err
	}
	_, err := s.DB.Exec(`UPDATE users SET level_override = ? WHERE id = ?`, lv, userID)
	return err
}

// RemoveModCategory 撤销某版块的版主登记;若已无任何管辖版块则整体降为普通用户。
func (s *Store) RemoveModCategory(userID, categoryID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM category_mods WHERE user_id = ? AND category_id = ?`,
			userID, categoryID); err != nil {
			return err
		}
		var n int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM category_mods WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			_, err := tx.Exec(`UPDATE users SET role = 'user' WHERE id = ? AND role = 'mod'`, userID)
			return err
		}
		return nil
	})
}

// AddModCategory 把用户登记为某版块的版主(role 置 mod;管理员自动全站,不做登记)。
// 同一用户可以管辖多个版块,重复登记同一版块幂等。
func (s *Store) AddModCategory(userID, categoryID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET role = 'mod' WHERE id = ? AND role <> 'admin'`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO category_mods (user_id, category_id) VALUES (?, ?)`,
			userID, categoryID)
		return err
	})
}

// DemoteMod 撤销版主身份并清空其管辖版块记录(role 恢复 user)。
func (s *Store) DemoteMod(userID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET role = 'user' WHERE id = ? AND role = 'mod'`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM category_mods WHERE user_id = ?`, userID)
		return err
	})
}

// ModCategories 某版主登记的管辖版块。
func (s *Store) ModCategories(userID int64) ([]Category, error) {
	rows, err := s.DB.Query(`
		SELECT c.id, c.slug, c.name, COALESCE(c.description,''),
		       (SELECT COUNT(*) FROM threads t WHERE t.category_id = c.id),
		       NULL
		FROM category_mods cm JOIN categories c ON c.id = cm.category_id
		WHERE cm.user_id = ? ORDER BY c.position, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.ThreadCount, &c.LastPostAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// IsModOf 用户是否为该版块的版主(管理员隐式通过,调用方自行判断 IsAdmin)。
func (s *Store) IsModOf(userID, categoryID int64) (bool, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM category_mods WHERE user_id = ? AND category_id = ?`,
		userID, categoryID).Scan(&n)
	return n > 0, err
}

// BanUser 封禁至 until(unix 秒);管理员等自身不在此列,由调用方校验。
func (s *Store) BanUser(userID, until int64) error {
	_, err := s.DB.Exec(`UPDATE users SET banned_until = ? WHERE id = ?`, until, userID)
	return err
}

func (s *Store) UnbanUser(userID int64) error {
	_, err := s.DB.Exec(`UPDATE users SET banned_until = NULL WHERE id = ?`, userID)
	return err
}

// ---------- notifications ----------

type Notification struct {
	ID          int64
	ActorID     int64
	ActorName   string
	ActorAvatar string
	Type        string // reply | mention
	ThreadID    int64
	PostID      sql.NullInt64
	ReadAt      sql.NullInt64
	CreatedAt   int64
	ThreadTitle string
}

// CreateNotification 写入一条通知。接收者的通知偏好在这里统一兜住:
// 不接收该类型时直接跳过并返回 false,调用方据此决定是否推送实时信号。
func (s *Store) CreateNotification(userID, actorID int64, ntype string, threadID, postID int64) (bool, error) {
	var scope string
	err := s.DB.QueryRow(`SELECT notify_scope FROM users WHERE id = ?`, userID).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !(&User{NotifyScope: scope}).WantsNotify(ntype) {
		return false, nil
	}
	var postArg any
	if postID > 0 {
		postArg = postID
	}
	_, err = s.DB.Exec(
		`INSERT INTO notifications (user_id, actor_id, type, thread_id, post_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, actorID, ntype, threadID, postArg, time.Now().Unix())
	return err == nil, err
}

// UserIDsByName 批量把用户名映射为 id(提及用;去重)。
func (s *Store) UserIDsByName(names []string) (map[string]int64, error) {
	out := make(map[string]int64)
	if len(names) == 0 {
		return out, nil
	}
	seen := make(map[string]bool, len(names))
	var uniq []string
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			uniq = append(uniq, n)
		}
	}
	if len(uniq) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(uniq))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(uniq))
	for i, n := range uniq {
		args[i] = n
	}
	rows, err := s.DB.Query(
		`SELECT name, id FROM users WHERE name IN (`+placeholders+`) COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

func (s *Store) CountUnreadNotifications(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

func (s *Store) ListNotifications(userID int64, limit int) ([]Notification, error) {
	rows, err := s.DB.Query(`
		SELECT n.id, n.actor_id, u.name, COALESCE(u.avatar_path,''), n.type,
		       n.thread_id, n.post_id, n.read_at, n.created_at, t.title
		FROM notifications n
		JOIN users u ON u.id = n.actor_id
		LEFT JOIN threads t ON t.id = n.thread_id
		WHERE n.user_id = ?
		ORDER BY n.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.ActorID, &n.ActorName, &n.ActorAvatar, &n.Type,
			&n.ThreadID, &n.PostID, &n.ReadAt, &n.CreatedAt, &n.ThreadTitle); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) MarkNotificationsRead(userID int64) error {
	_, err := s.DB.Exec(
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`,
		time.Now().Unix(), userID)
	return err
}

// MarkNotificationRead 把单条通知标为已读(仅限本人;不存在视为幂等成功)。
func (s *Store) MarkNotificationRead(userID, id int64) error {
	_, err := s.DB.Exec(
		`UPDATE notifications SET read_at = ? WHERE user_id = ? AND id = ? AND read_at IS NULL`,
		time.Now().Unix(), userID, id)
	return err
}

// DeletePost 删除单帖(非首帖)并递减计数;首帖的删除走 DeleteThread。
func (s *Store) DeletePost(postID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		var threadID int64
		err := tx.QueryRow(`SELECT thread_id FROM posts WHERE id = ?`, postID).Scan(&threadID)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM posts WHERE id = ?`, postID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		_, err = tx.Exec(
			`UPDATE threads SET post_count = MAX(post_count - 1, 0) WHERE id = ?`, threadID)
		return err
	})
}

// DeleteThread 删除整个主题(posts / 抽奖 / 参与记录靠外键级联)。
// 主题上还挂着没开奖的抽奖时先把钱退干净:奖池退楼主、投入逐笔退参与者,
// 否则这些积分会跟着帖子一起蒸发。
func (s *Store) DeleteThread(threadID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if err := refundLotteryTx(tx, threadID, "抽奖帖已删除"); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM threads WHERE id = ?`, threadID)
		return err
	})
}

// refundLotteryTx 把某主题上未开奖的抽奖退款。已开奖的不动 —— 奖已经发出去了。
// 不是抽奖帖时直接返回 nil。point_logs.thread_id 没有外键,所以流水不会跟着帖子删掉。
func refundLotteryTx(tx *sql.Tx, threadID int64, why string) error {
	var status string
	var sponsor, authorID int64
	err := tx.QueryRow(`SELECT l.status, l.sponsor, t.author_id
		FROM lotteries l JOIN threads t ON t.id = l.thread_id
		WHERE l.thread_id = ?`, threadID).Scan(&status, &sponsor, &authorID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return nil
	}
	if sponsor > 0 {
		if err := addPointsTx(tx, authorID, sponsor, PointLotBack,
			why+",奖池退回", threadID, 0); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT user_id, stake FROM lottery_entries
		WHERE thread_id = ? AND stake > 0`, threadID)
	if err != nil {
		return err
	}
	type back struct{ uid, amt int64 }
	var list []back
	for rows.Next() {
		var b back
		if err := rows.Scan(&b.uid, &b.amt); err != nil {
			rows.Close()
			return err
		}
		list = append(list, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, b := range list {
		if err := addPointsTx(tx, b.uid, b.amt, PointLotBack,
			why+",投入退回", threadID, 0); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 私信 ----------

// DMConversation 是私信列表/会话页里的一条会话:对方信息 + 最后一句 + 未读数。
type DMConversation struct {
	ID         int64
	PeerID     int64
	PeerName   string
	PeerAvatar string
	PeerRole   string
	PeerKind   sql.NullString // 对方认证分类(头像 V)
	PeerVerify sql.NullString // 对方认证文案
	LastAt     int64
	LastBody   string
	LastFromMe bool
	Unread     int64
}

// DMMessage 是会话里的一条消息(kind=redpack 时是红包)。
type DMMessage struct {
	ID        int64
	SenderID  int64
	Body      string
	CreatedAt int64
	ReadAt    sql.NullInt64
	Kind      string // text | redpack
	Amount    int64
	RPStatus  string // open | claimed | refunded(发送者撤回) | expired(超时自动退回)
	RPAt      sql.NullInt64
}

// IsRedpack 该条是不是红包。
func (m DMMessage) IsRedpack() bool { return m.Kind == "redpack" }

// RPRevoked 是不是被发送者主动撤回的。这种气泡要降级成不显示金额的占位 ——
// 撤回时最该藏的就是金额(尤其是发错人)。超时退回不算,那种照旧显示金额。
func (m DMMessage) RPRevoked() bool { return m.Kind == "redpack" && m.RPStatus == "refunded" }

// RPOpen 红包是否还没被领走/退回。
func (m DMMessage) RPOpen() bool { return m.Kind == "redpack" && m.RPStatus == "open" }

// dmPair 把两个用户 id 归一成 (小, 大),保证同一对人只有一条会话。
func dmPair(x, y int64) (int64, int64) {
	if x < y {
		return x, y
	}
	return y, x
}

// DMThreadFor 取两人之间的会话,没有就建一条。
func (s *Store) DMThreadFor(userID, peerID int64) (int64, error) {
	a, b := dmPair(userID, peerID)
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM dm_threads WHERE user_a = ? AND user_b = ?`, a, b).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := time.Now().Unix()
	res, err := s.DB.Exec(
		`INSERT INTO dm_threads (user_a, user_b, created_at, last_at) VALUES (?, ?, ?, ?)`,
		a, b, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DMPeer 返回该会话里 viewer 的对话对象 id;viewer 不是会话参与者时返回 0。
func (s *Store) DMPeer(threadID, viewerID int64) (int64, error) {
	var a, b int64
	err := s.DB.QueryRow(`SELECT user_a, user_b FROM dm_threads WHERE id = ?`, threadID).Scan(&a, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	switch viewerID {
	case a:
		return b, nil
	case b:
		return a, nil
	}
	return 0, nil
}

// SendDM 追加一条私信并把会话顶到最新。
func (s *Store) SendDM(threadID, senderID int64, body string) (int64, error) {
	var id int64
	now := time.Now().Unix()
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO dm_messages (thread_id, sender_id, body, created_at) VALUES (?, ?, ?, ?)`,
			threadID, senderID, body, now)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE dm_threads SET last_at = ? WHERE id = ?`, now, threadID)
		return err
	})
	return id, err
}

// SendRedpack 发一个私信红包:先从发送者扣积分,再写一条 redpack 消息。
// 积分不足返回 ErrNotEnoughPoints,消息不会落库。
func (s *Store) SendRedpack(threadID, senderID, amount int64, note string) (int64, error) {
	var id int64
	now := time.Now().Unix()
	err := s.withTx(func(tx *sql.Tx) error {
		if err := addPointsTx(tx, senderID, -amount, PointRedpackOut, note, 0, 0); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO dm_messages
			(thread_id, sender_id, body, created_at, kind, amount, rp_status)
			VALUES (?, ?, '', ?, 'redpack', ?, 'open')`, threadID, senderID, now, amount)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE dm_threads SET last_at = ? WHERE id = ?`, now, threadID)
		return err
	})
	return id, err
}

// ClaimRedpack 领取红包:只有收件人能领,且只能领一次。
// 返回 (金额, 是否领到);已被领过/已退回时返回 (0, false)。
func (s *Store) ClaimRedpack(msgID, threadID, claimerID int64, note string) (int64, bool, error) {
	var amount int64
	done := false
	err := s.withTx(func(tx *sql.Tx) error {
		var senderID int64
		var status string
		err := tx.QueryRow(`SELECT sender_id, amount, rp_status FROM dm_messages
			WHERE id = ? AND thread_id = ? AND kind = 'redpack'`, msgID, threadID).
			Scan(&senderID, &amount, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != "open" || senderID == claimerID {
			return nil
		}
		res, err := tx.Exec(
			`UPDATE dm_messages SET rp_status = 'claimed', rp_at = ? WHERE id = ? AND rp_status = 'open'`,
			time.Now().Unix(), msgID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // 并发下被别的请求先领了
		}
		if err := addPointsTx(tx, claimerID, amount, PointRedpackIn, note, 0, senderID); err != nil {
			return err
		}
		done = true
		return nil
	})
	return amount, done, err
}

// RefundRedpack 撤回未领取的红包,积分退还发送者。
// 若这条红包是会话里唯一的消息,连消息带会话一起删 —— 发错人的场合不该在陌生人
// 的私信列表里留痕。已有往来时只改状态,气泡降级成不显示金额的占位。
// 返回 (是否撤回成功, 会话是否已被删掉)。
func (s *Store) RefundRedpack(msgID, threadID, senderID int64, note string) (bool, bool, error) {
	done, gone := false, false
	err := s.withTx(func(tx *sql.Tx) error {
		var amount int64
		var status string
		err := tx.QueryRow(`SELECT amount, rp_status FROM dm_messages
			WHERE id = ? AND thread_id = ? AND sender_id = ? AND kind = 'redpack'`,
			msgID, threadID, senderID).Scan(&amount, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if status != "open" {
			return nil
		}
		var others int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM dm_messages WHERE thread_id = ? AND id <> ?`,
			threadID, msgID).Scan(&others); err != nil {
			return err
		}
		// 并发保护同原来:靠 rp_status = 'open' 的影响行数判断,领取和撤回抢不到一起
		var res sql.Result
		if others == 0 {
			res, err = tx.Exec(`DELETE FROM dm_messages WHERE id = ? AND rp_status = 'open'`, msgID)
		} else {
			res, err = tx.Exec(
				`UPDATE dm_messages SET rp_status = 'refunded', rp_at = ? WHERE id = ? AND rp_status = 'open'`,
				time.Now().Unix(), msgID)
		}
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if others == 0 {
			if _, err := tx.Exec(`DELETE FROM dm_threads WHERE id = ?`, threadID); err != nil {
				return err
			}
			gone = true
		}
		if err := addPointsTx(tx, senderID, amount, PointRedpackBack, note, 0, 0); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, gone, err
}

// ExpiredRedpack 一笔超时退回的红包,调用方拿它去推实时刷新。
type ExpiredRedpack struct {
	ThreadID int64
	SenderID int64
	PeerID   int64
}

// ExpireRedpacks 把 createdBefore 之前还没人领的红包退回发送者,状态记 expired。
// 跟手动撤回不同:超时是被动的、对方早看过金额了,所以不删消息也不删会话。
// 一次最多 500 笔,剩下的下一轮再扫。
func (s *Store) ExpireRedpacks(createdBefore int64) ([]ExpiredRedpack, error) {
	type cand struct {
		msgID, threadID, senderID, amount, peerID int64
		peerName                                  string
	}
	rows, err := s.DB.Query(`
		SELECT m.id, m.thread_id, m.sender_id, m.amount,
		       CASE WHEN t.user_a = m.sender_id THEN t.user_b ELSE t.user_a END AS peer_id,
		       COALESCE(p.name, '')
		FROM dm_messages m
		JOIN dm_threads t ON t.id = m.thread_id
		LEFT JOIN users p ON p.id = CASE WHEN t.user_a = m.sender_id THEN t.user_b ELSE t.user_a END
		WHERE m.kind = 'redpack' AND m.rp_status = 'open' AND m.created_at < ?
		ORDER BY m.id LIMIT 500`, createdBefore)
	if err != nil {
		return nil, err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.msgID, &c.threadID, &c.senderID, &c.amount,
			&c.peerID, &c.peerName); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ExpiredRedpack
	for _, c := range cands {
		ok := false
		if err := s.withTx(func(tx *sql.Tx) error {
			res, err := tx.Exec(`UPDATE dm_messages SET rp_status = 'expired', rp_at = ?
				WHERE id = ? AND rp_status = 'open'`, time.Now().Unix(), c.msgID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil // 刚好被领走或手动撤回了
			}
			note := "红包超时退回"
			if c.peerName != "" {
				note = "发给 " + c.peerName + " 的红包超时退回"
			}
			if err := addPointsTx(tx, c.senderID, c.amount, PointRedpackBack, note, 0, 0); err != nil {
				return err
			}
			ok = true
			return nil
		}); err != nil {
			return out, err
		}
		if ok {
			out = append(out, ExpiredRedpack{
				ThreadID: c.threadID, SenderID: c.senderID, PeerID: c.peerID})
		}
	}
	return out, nil
}

const dmConvSelect = `
	SELECT t.id,
	       CASE WHEN t.user_a = ? THEN t.user_b ELSE t.user_a END AS peer_id,
	       p.name, COALESCE(p.avatar_path,''), p.role, p.verify_kind, p.verify_title,
	       t.last_at,
	       COALESCE((SELECT CASE
	                          WHEN m.kind <> 'redpack' THEN m.body
	                          WHEN m.rp_status = 'refunded' THEN '撤回了一条消息'
	                          -- amount 存的是「分」,这里按两位小数展开再去掉末尾的 0,
	                          -- 跟 Go 侧 db.FormatPoints 的口径保持一致
	                          ELSE '[红包 ' ||
	                               rtrim(rtrim(printf('%.2f', m.amount / 100.0), '0'), '.') ||
	                               ' 积分]' END
	                  FROM dm_messages m WHERE m.thread_id = t.id
	                  ORDER BY m.id DESC LIMIT 1), ''),
	       COALESCE((SELECT m.sender_id = ? FROM dm_messages m WHERE m.thread_id = t.id
	                  ORDER BY m.id DESC LIMIT 1), 0),
	       (SELECT COUNT(*) FROM dm_messages m
	         WHERE m.thread_id = t.id AND m.sender_id <> ? AND m.read_at IS NULL)
	FROM dm_threads t
	JOIN users p ON p.id = CASE WHEN t.user_a = ? THEN t.user_b ELSE t.user_a END`

func scanDMConv(row interface{ Scan(...any) error }) (*DMConversation, error) {
	c := &DMConversation{}
	var fromMe int64
	err := row.Scan(&c.ID, &c.PeerID, &c.PeerName, &c.PeerAvatar, &c.PeerRole,
		&c.PeerKind, &c.PeerVerify, &c.LastAt, &c.LastBody, &fromMe, &c.Unread)
	if err != nil {
		return nil, err
	}
	c.LastFromMe = fromMe != 0
	return c, nil
}

// ListDMConversations 我的会话列表:有过消息的会话按最后一句时间倒序。
func (s *Store) ListDMConversations(userID int64, limit, offset int) ([]DMConversation, error) {
	rows, err := s.DB.Query(dmConvSelect+`
		WHERE (t.user_a = ? OR t.user_b = ?)
		  AND EXISTS (SELECT 1 FROM dm_messages m WHERE m.thread_id = t.id)
		ORDER BY t.last_at DESC LIMIT ? OFFSET ?`,
		userID, userID, userID, userID, userID, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DMConversation
	for rows.Next() {
		c, err := scanDMConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) CountDMConversations(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM dm_threads t
		WHERE (t.user_a = ? OR t.user_b = ?)
		  AND EXISTS (SELECT 1 FROM dm_messages m WHERE m.thread_id = t.id)`,
		userID, userID).Scan(&n)
	return n, err
}

// GetDMConversation 取单个会话(viewer 必须是参与者,否则返回 nil)。
func (s *Store) GetDMConversation(threadID, viewerID int64) (*DMConversation, error) {
	c, err := scanDMConv(s.DB.QueryRow(dmConvSelect+`
		WHERE t.id = ? AND (t.user_a = ? OR t.user_b = ?)`,
		viewerID, viewerID, viewerID, viewerID, threadID, viewerID, viewerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListDMMessages 会话内的消息(按时间正序,取最近 limit 条)。
func (s *Store) ListDMMessages(threadID int64, limit int) ([]DMMessage, error) {
	rows, err := s.DB.Query(`
		SELECT id, sender_id, body, created_at, read_at, kind, amount, rp_status, rp_at FROM (
			SELECT id, sender_id, body, created_at, read_at, kind, amount, rp_status, rp_at
			FROM dm_messages WHERE thread_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id`, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DMMessage
	for rows.Next() {
		var m DMMessage
		if err := rows.Scan(&m.ID, &m.SenderID, &m.Body, &m.CreatedAt, &m.ReadAt,
			&m.Kind, &m.Amount, &m.RPStatus, &m.RPAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkDMRead 把会话里对方发来的未读消息标为已读。
func (s *Store) MarkDMRead(threadID, userID int64) error {
	_, err := s.DB.Exec(
		`UPDATE dm_messages SET read_at = ?
		  WHERE thread_id = ? AND sender_id <> ? AND read_at IS NULL`,
		time.Now().Unix(), threadID, userID)
	return err
}

// CountUnreadDM 我收到的未读私信总数(顶栏角标)。
func (s *Store) CountUnreadDM(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`
		SELECT COUNT(*) FROM dm_messages m
		JOIN dm_threads t ON t.id = m.thread_id
		WHERE m.sender_id <> ? AND m.read_at IS NULL
		  AND (t.user_a = ? OR t.user_b = ?)`, userID, userID, userID).Scan(&n)
	return n, err
}

// NotifyDMEnabled 对方是否开着私信提醒(免打扰时不推送、不亮角标)。
func (s *Store) NotifyDMEnabled(userID int64) (bool, error) {
	var on bool
	err := s.DB.QueryRow(`SELECT notify_dm FROM users WHERE id = ?`, userID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// ---------- 积分 ----------

// 积分流水类型。
const (
	PointCheckin  = "checkin"       // 每日签到
	PointTipOut   = "tip_out"       // 打赏支出
	PointTipIn    = "tip_in"        // 打赏收入
	PointAdmin    = "admin"         // 管理员手动调整
	PointUnlockOut = "unlock_out"   // 解锁付费帖支出
	PointUnlockIn  = "unlock_in"    // 付费帖作者收入
	PointStake       = "lottery_stake" // 参与抽奖投入
	PointWin         = "lottery_win"   // 抽奖中奖
	PointLotFund     = "lottery_fund"  // 抽奖出奖预扣(楼主自掏奖池)
	PointLotBack     = "lottery_back"  // 抽奖奖池退回(无人参与/帖子删了)
	PointShop      = "shop"          // 商城兑换
	PointRedpackOut = "redpack_out"  // 发出私信红包
	PointRedpackIn  = "redpack_in"   // 领取私信红包
	PointRedpackBack = "redpack_back" // 未领取红包退回
	PointRename      = "rename"       // 修改显示名
)

// ErrNotEnoughPoints 余额不足;调用方据此给出「积分不够」的提示。
var ErrNotEnoughPoints = errors.New("积分不足")

// PointLog 一条积分流水(带对方名字,列表直接用)。
type PointLog struct {
	ID        int64
	Delta     int64
	Balance   int64
	Kind      string
	Note      string
	ThreadID  sql.NullInt64
	PeerID    sql.NullInt64
	PeerName  sql.NullString
	CreatedAt int64
}

// addPointsTx 在给定事务里加减积分并记一笔流水。
// delta 为负时用「余额足够」作为更新条件,避免并发扣成负数。
func addPointsTx(tx *sql.Tx, userID, delta int64, kind, note string, threadID, peerID int64) error {
	var res sql.Result
	var err error
	if delta < 0 {
		res, err = tx.Exec(`UPDATE users SET points = points + ? WHERE id = ? AND points >= ?`,
			delta, userID, -delta)
	} else {
		res, err = tx.Exec(`UPDATE users SET points = points + ? WHERE id = ?`, delta, userID)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if delta < 0 {
			return ErrNotEnoughPoints
		}
		return sql.ErrNoRows
	}
	var balance int64
	if err := tx.QueryRow(`SELECT points FROM users WHERE id = ?`, userID).Scan(&balance); err != nil {
		return err
	}
	var tArg, pArg any
	if threadID > 0 {
		tArg = threadID
	}
	if peerID > 0 {
		pArg = peerID
	}
	_, err = tx.Exec(`INSERT INTO point_logs
		(user_id, delta, balance, kind, note, thread_id, peer_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, delta, balance, kind, note, tArg, pArg, time.Now().Unix())
	return err
}

// AdjustPoints 单方加减积分(管理员调整、商城扣费等)。
func (s *Store) AdjustPoints(userID, delta int64, kind, note string, threadID, peerID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		return addPointsTx(tx, userID, delta, kind, note, threadID, peerID)
	})
}

// TransferPoints 从一个人转给另一个人(打赏、付费帖分成),两笔流水一个事务。
func (s *Store) TransferPoints(fromID, toID, amount int64, outKind, inKind, note string, threadID int64) error {
	if amount <= 0 {
		return errors.New("积分数需大于 0")
	}
	return s.withTx(func(tx *sql.Tx) error {
		if err := addPointsTx(tx, fromID, -amount, outKind, note, threadID, toID); err != nil {
			return err
		}
		return addPointsTx(tx, toID, amount, inKind, note, threadID, fromID)
	})
}

// Points 查某人的积分余额。
func (s *Store) Points(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT points FROM users WHERE id = ?`, userID).Scan(&n)
	return n, err
}

// ListPointLogs 积分流水(倒序)。
func (s *Store) ListPointLogs(userID int64, limit, offset int) ([]PointLog, error) {
	rows, err := s.DB.Query(`
		SELECT l.id, l.delta, l.balance, l.kind, l.note, l.thread_id, l.peer_id, p.name, l.created_at
		FROM point_logs l LEFT JOIN users p ON p.id = l.peer_id
		WHERE l.user_id = ? ORDER BY l.id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PointLog
	for rows.Next() {
		var l PointLog
		if err := rows.Scan(&l.ID, &l.Delta, &l.Balance, &l.Kind, &l.Note,
			&l.ThreadID, &l.PeerID, &l.PeerName, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) CountPointLogs(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM point_logs WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// ---------- 签到 ----------

// CheckedIn 今天是否已签到。
func (s *Store) CheckedIn(userID int64, day string) (bool, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkins WHERE user_id = ? AND day = ?`,
		userID, day).Scan(&n)
	return n > 0, err
}

// Checkin 签到:一天一次,加积分与经验。已签到返回 false(幂等,不报错)。
func (s *Store) Checkin(userID int64, day string, points, exp int64) (bool, error) {
	done := false
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT OR IGNORE INTO checkins (user_id, day, points, exp, created_at)
			VALUES (?, ?, ?, ?, ?)`, userID, day, points, exp, time.Now().Unix())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil // 今天签过了
		}
		if _, err := tx.Exec(`UPDATE users SET exp_extra = exp_extra + ? WHERE id = ?`,
			exp, userID); err != nil {
			return err
		}
		if err := addPointsTx(tx, userID, points, PointCheckin, "每日签到", 0, 0); err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
}

// CheckinCount 累计签到天数。
func (s *Store) CheckinCount(userID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM checkins WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// ThreadTipTotal 某主题收到的打赏总额(展示在反应条上)。
func (s *Store) ThreadTipTotal(threadID int64) (int64, error) {
	var n sql.NullInt64
	err := s.DB.QueryRow(
		`SELECT SUM(delta) FROM point_logs WHERE thread_id = ? AND kind = ?`,
		threadID, PointTipIn).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// ---------- 勋章 ----------

// Badge 一枚勋章。展示文案就是 Name(佩戴后写进 users.badge_text)。
type Badge struct {
	ID      int64
	Name    string
	Note    string
	Owners  int64 // 已持有人数(后台列表用)
	Owned   bool  // 当前用户是否持有(个人视角查询时填)
	Wearing bool  // 当前用户是否正佩戴
}

// ListBadges 全部勋章(后台管理用,带持有人数)。
func (s *Store) ListBadges() ([]Badge, error) {
	rows, err := s.DB.Query(`
		SELECT b.id, b.name, b.note,
		       (SELECT COUNT(*) FROM user_badges ub WHERE ub.badge_id = b.id)
		FROM badges b ORDER BY b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Badge
	for rows.Next() {
		var b Badge
		if err := rows.Scan(&b.ID, &b.Name, &b.Note, &b.Owners); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UserBadges 某人持有的勋章(标出正在佩戴的那枚)。
func (s *Store) UserBadges(userID int64) ([]Badge, error) {
	rows, err := s.DB.Query(`
		SELECT b.id, b.name, b.note, (u.badge_id IS NOT NULL AND u.badge_id = b.id)
		FROM user_badges ub
		JOIN badges b ON b.id = ub.badge_id
		JOIN users u ON u.id = ub.user_id
		WHERE ub.user_id = ? ORDER BY ub.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Badge
	for rows.Next() {
		var b Badge
		if err := rows.Scan(&b.ID, &b.Name, &b.Note, &b.Wearing); err != nil {
			return nil, err
		}
		b.Owned = true
		out = append(out, b)
	}
	return out, rows.Err()
}

// BadgesWithOwnership 全部勋章 + 某人是否持有(后台弹窗里发放/撤销用)。
func (s *Store) BadgesWithOwnership(userID int64) ([]Badge, error) {
	rows, err := s.DB.Query(`
		SELECT b.id, b.name, b.note,
		       EXISTS(SELECT 1 FROM user_badges ub WHERE ub.badge_id = b.id AND ub.user_id = ?)
		FROM badges b ORDER BY b.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Badge
	for rows.Next() {
		var b Badge
		if err := rows.Scan(&b.ID, &b.Name, &b.Note, &b.Owned); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBadge 新建勋章;同名返回 ErrDuplicateName。
func (s *Store) CreateBadge(name, note string) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO badges (name, note, created_at) VALUES (?, ?, ?)`,
		name, note, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrDuplicateName
		}
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteBadge 删除勋章:持有记录级联清掉,正佩戴它的人回落到身份标签。
func (s *Store) DeleteBadge(badgeID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`UPDATE users SET badge_id = NULL, badge_text = NULL WHERE badge_id = ?`, badgeID); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM badges WHERE id = ?`, badgeID)
		return err
	})
}

// GrantBadge 发放勋章(重复发放幂等)。
func (s *Store) GrantBadge(userID, badgeID int64, source string) error {
	_, err := s.DB.Exec(
		`INSERT OR IGNORE INTO user_badges (user_id, badge_id, source, created_at)
		 VALUES (?, ?, ?, ?)`, userID, badgeID, source, time.Now().Unix())
	return err
}

// RevokeBadge 收回勋章;若正佩戴则同时取下。
func (s *Store) RevokeBadge(userID, badgeID int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM user_badges WHERE user_id = ? AND badge_id = ?`, userID, badgeID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`UPDATE users SET badge_id = NULL, badge_text = NULL WHERE id = ? AND badge_id = ?`,
			userID, badgeID)
		return err
	})
}

// HasBadge 是否持有某勋章。
func (s *Store) HasBadge(userID, badgeID int64) (bool, error) {
	var n int64
	err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM user_badges WHERE user_id = ? AND badge_id = ?`,
		userID, badgeID).Scan(&n)
	return n > 0, err
}

// WearBadge 佩戴勋章:把勋章名写进 badge_text(供既有渲染逻辑直接用)。
// badgeID<=0 时按 mode 处理:hide=隐藏标签,其余=跟随身份。
func (s *Store) WearBadge(userID, badgeID int64, hide bool) error {
	if badgeID <= 0 {
		var text any
		if hide {
			text = ""
		}
		_, err := s.DB.Exec(
			`UPDATE users SET badge_id = NULL, badge_text = ? WHERE id = ?`, text, userID)
		return err
	}
	_, err := s.DB.Exec(`
		UPDATE users SET badge_id = ?,
		       badge_text = (SELECT name FROM badges WHERE id = ?)
		WHERE id = ? AND EXISTS(
			SELECT 1 FROM user_badges WHERE user_id = ? AND badge_id = ?)`,
		badgeID, badgeID, userID, userID, badgeID)
	return err
}

// ---------- 积分商城 ----------

// ShopItem 商城商品。
type ShopItem struct {
	ID       int64
	Kind     string // badge=勋章 | checkin=签到加成 | custom=自定义(兑换后由管理员线下发放)
	Name     string
	Note     string
	Price    int64
	BadgeID  sql.NullInt64
	Bonus    int64
	Days     int64
	Stock    int64 // -1 表示不限量
	Active   bool
	Owned    bool // kind=badge 且当前用户已持有
	SoldOut  bool
}

// ShopOrder 一条兑换记录。
type ShopOrder struct {
	ID        int64
	Name      string
	Price     int64
	CreatedAt int64
}

func scanShopItem(row interface{ Scan(...any) error }) (*ShopItem, error) {
	it := &ShopItem{}
	var active int64
	err := row.Scan(&it.ID, &it.Kind, &it.Name, &it.Note, &it.Price,
		&it.BadgeID, &it.Bonus, &it.Days, &it.Stock, &active)
	if err != nil {
		return nil, err
	}
	it.Active = active != 0
	it.SoldOut = it.Stock == 0
	return it, nil
}

const shopItemCols = `id, kind, name, note, price, badge_id, bonus, days, stock, active`

// ListShopItems 商品列表。onlyActive=true 时只返回上架且未售罄的。
func (s *Store) ListShopItems(onlyActive bool, viewerID int64) ([]ShopItem, error) {
	q := `SELECT ` + shopItemCols + ` FROM shop_items`
	if onlyActive {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY sort, id`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopItem
	for rows.Next() {
		it, err := scanShopItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if viewerID > 0 {
		for i := range out {
			if out[i].Kind == "badge" && out[i].BadgeID.Valid {
				owned, err := s.HasBadge(viewerID, out[i].BadgeID.Int64)
				if err != nil {
					return nil, err
				}
				out[i].Owned = owned
			}
		}
	}
	return out, nil
}

func (s *Store) GetShopItem(id int64) (*ShopItem, error) {
	it, err := scanShopItem(s.DB.QueryRow(
		`SELECT `+shopItemCols+` FROM shop_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return it, nil
}

// CreateShopItem 新建商品。
func (s *Store) CreateShopItem(it ShopItem) (int64, error) {
	var badgeArg any
	if it.BadgeID.Valid {
		badgeArg = it.BadgeID.Int64
	}
	res, err := s.DB.Exec(`INSERT INTO shop_items
		(kind, name, note, price, badge_id, bonus, days, stock, active, sort, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		it.Kind, it.Name, it.Note, it.Price, badgeArg, it.Bonus, it.Days, it.Stock,
		time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateShopItem 改商品的可编辑字段。kind 故意不可改 —— 勋章商品换成签到加成
// 会让已有兑换记录对不上,要换类型就删了重建。历史订单存的是下单时的名称与价格
// 快照(shop_orders),所以改这里不会篡改账目。
func (s *Store) UpdateShopItem(it ShopItem) error {
	var badgeArg any
	if it.BadgeID.Valid {
		badgeArg = it.BadgeID.Int64
	}
	_, err := s.DB.Exec(`UPDATE shop_items
		SET name = ?, note = ?, price = ?, badge_id = ?, bonus = ?, days = ?, stock = ?
		WHERE id = ?`,
		it.Name, it.Note, it.Price, badgeArg, it.Bonus, it.Days, it.Stock, it.ID)
	return err
}

// SetShopItemActive 上架/下架。
func (s *Store) SetShopItemActive(id int64, active bool) error {
	_, err := s.DB.Exec(`UPDATE shop_items SET active = ? WHERE id = ?`, active, id)
	return err
}

func (s *Store) DeleteShopItem(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM shop_items WHERE id = ?`, id)
	return err
}

// ErrShopSoldOut 商品已售罄。
var ErrShopSoldOut = errors.New("已兑换完")

// ErrShopOwned 勋章类商品已持有,不必重复兑换。
var ErrShopOwned = errors.New("已经拥有了")

// RedeemShopItem 兑换:扣积分 → 发勋章或加签到加成 → 记订单 → 扣库存,一个事务。
func (s *Store) RedeemShopItem(userID int64, it ShopItem) error {
	return s.withTx(func(tx *sql.Tx) error {
		// 库存与持有校验放在事务里,避免并发超卖/重复兑换
		var stock int64
		var active int64
		if err := tx.QueryRow(`SELECT stock, active FROM shop_items WHERE id = ?`, it.ID).
			Scan(&stock, &active); err != nil {
			return err
		}
		if active == 0 {
			return ErrShopSoldOut
		}
		if stock == 0 {
			return ErrShopSoldOut
		}
		if it.Kind == "badge" && it.BadgeID.Valid {
			var n int64
			if err := tx.QueryRow(
				`SELECT COUNT(*) FROM user_badges WHERE user_id = ? AND badge_id = ?`,
				userID, it.BadgeID.Int64).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrShopOwned
			}
		}
		if err := addPointsTx(tx, userID, -it.Price, PointShop, "兑换「"+it.Name+"」", 0, 0); err != nil {
			return err
		}
		switch it.Kind {
		case "badge":
			if it.BadgeID.Valid {
				if _, err := tx.Exec(
					`INSERT OR IGNORE INTO user_badges (user_id, badge_id, source, created_at)
					 VALUES (?, ?, 'shop', ?)`,
					userID, it.BadgeID.Int64, time.Now().Unix()); err != nil {
					return err
				}
			}
		case "checkin":
			// 加成额度不累加:买 N 次就每天多拿 N 倍会通胀,而且 checkin_bonus /
			// bonus_until 各只有一列,装不下两份并存的加成。
			// 规则:生效中的取较高档,已过期的直接替换;有期限的按天顺延。
			now := time.Now().Unix()
			var curBonus int64
			var curUntil sql.NullInt64
			if err := tx.QueryRow(`SELECT checkin_bonus, bonus_until FROM users WHERE id = ?`,
				userID).Scan(&curBonus, &curUntil); err != nil {
				return err
			}
			// bonus_until 为 NULL 表示不限期,所以只有「有到期时间且已过」才算失效
			active := curBonus > 0 && !(curUntil.Valid && curUntil.Int64 <= now)
			bonus := it.Bonus
			if active && curBonus > bonus {
				bonus = curBonus
			}
			var until any
			if it.Days > 0 {
				base := now
				if active && curUntil.Valid && curUntil.Int64 > now {
					base = curUntil.Int64
				}
				until = base + it.Days*86400
			}
			if _, err := tx.Exec(
				`UPDATE users SET checkin_bonus = ?, bonus_until = ? WHERE id = ?`,
				bonus, until, userID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO shop_orders (user_id, item_id, name, price, created_at)
			VALUES (?, ?, ?, ?, ?)`, userID, it.ID, it.Name, it.Price, time.Now().Unix()); err != nil {
			return err
		}
		if stock > 0 {
			if _, err := tx.Exec(`UPDATE shop_items SET stock = stock - 1 WHERE id = ?`, it.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// AdminShopOrder 后台看到的兑换记录(带兑换人)。
type AdminShopOrder struct {
	ID        int64
	UserID    int64
	UserName  string
	Name      string
	Price     int64
	CreatedAt int64
}

// ListAllShopOrders 全站最近的兑换记录(自定义商品要靠它线下发货)。
func (s *Store) ListAllShopOrders(limit int) ([]AdminShopOrder, error) {
	rows, err := s.DB.Query(`
		SELECT o.id, o.user_id, u.name, o.name, o.price, o.created_at
		FROM shop_orders o JOIN users u ON u.id = o.user_id
		ORDER BY o.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminShopOrder
	for rows.Next() {
		var o AdminShopOrder
		if err := rows.Scan(&o.ID, &o.UserID, &o.UserName, &o.Name, &o.Price, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListShopOrders 我的兑换记录。
func (s *Store) ListShopOrders(userID int64, limit int) ([]ShopOrder, error) {
	rows, err := s.DB.Query(
		`SELECT id, name, price, created_at FROM shop_orders
		  WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShopOrder
	for rows.Next() {
		var o ShopOrder
		if err := rows.Scan(&o.ID, &o.Name, &o.Price, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---------- uploads ----------

type Upload struct {
	ID        int64
	UserID    int64
	Path      string // 相对上传目录的文件名
	Mime      string
	Size      int64
	CreatedAt int64
}

func (s *Store) CreateUpload(userID int64, path, mime string, size int64) (int64, error) {
	res, err := s.DB.Exec(
		`INSERT INTO uploads (user_id, path, mime, size, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, path, mime, size, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetUpload(id int64) (*Upload, error) {
	u := &Upload{}
	err := s.DB.QueryRow(
		`SELECT id, user_id, path, mime, size, created_at FROM uploads WHERE id = ?`, id).
		Scan(&u.ID, &u.UserID, &u.Path, &u.Mime, &u.Size, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) DeleteUpload(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM uploads WHERE id = ?`, id)
	return err
}

func (s *Store) IncrThreadViews(threadID int64) {
	s.DB.Exec(`UPDATE threads SET view_count = view_count + 1 WHERE id = ?`, threadID)
}

// ---------- misc ----------

// VacuumSessions 顺手清理过期会话,登录/登出时调用即可,无需定时器。
func (s *Store) VacuumSessions() {
	s.DB.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
}

// ---------- 站点设置 ----------

// Site 是前台展示用的站点信息,site_settings 键值表的强类型视图。
// 空值即“未设置”:Name/Footer 由 Site() 兜默认值,其余为空表示不展示。
type Site struct {
	Name         string // 站点名:顶栏品牌文字 + 浏览器标题后缀
	Tagline      string // 品牌副标题:顶栏站点名右侧小字(窄屏隐藏)
	Footer       string // 页脚文案
	IconPath     string // 站点图标 /uploads/{id},同时用作 favicon
	Announcement string // 顶部滚动横幅公告
}

const (
	SiteDefaultName   = "BBS"
	SiteDefaultFooter = "Powered by chaguan"
)

// site_settings 里的键名(只在本包内使用,handlers 走强类型方法)。
const (
	keySiteName     = "site_name"
	keySiteTagline  = "site_tagline"
	keySiteFooter   = "site_footer"
	keySiteIcon     = "site_icon"
	keyAnnouncement = "announcement"
)

// Site 一次读出全部站点设置。每个页面渲染都会调用它,故只走一条查询;
// 读失败时返回的仍是带默认值的结构,页面不会出现空品牌。
func (s *Store) Site() (Site, error) {
	site := Site{Name: SiteDefaultName, Footer: SiteDefaultFooter}
	rows, err := s.DB.Query(`SELECT key, value FROM site_settings`)
	if err != nil {
		return site, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return site, err
		}
		switch k {
		case keySiteName:
			if v != "" {
				site.Name = v
			}
		case keySiteTagline:
			site.Tagline = v
		case keySiteFooter:
			if v != "" {
				site.Footer = v
			}
		case keySiteIcon:
			site.IconPath = v
		case keyAnnouncement:
			site.Announcement = v
		}
	}
	return site, rows.Err()
}

// SetSiteInfo 保存后台「站点设置」表单里的文字项(一个事务)。
// 传空串表示恢复默认(名称/页脚)或关闭展示(副标题/公告)。
func (s *Store) SetSiteInfo(name, tagline, footer, announcement string) error {
	return s.setSettings(map[string]string{
		keySiteName:     name,
		keySiteTagline:  tagline,
		keySiteFooter:   footer,
		keyAnnouncement: announcement,
	})
}

// SetSiteIcon 保存站点图标路径;空串表示回到内置图标。
func (s *Store) SetSiteIcon(path string) error {
	return s.setSettings(map[string]string{keySiteIcon: path})
}

func (s *Store) setSettings(kv map[string]string) error {
	return s.withTx(func(tx *sql.Tx) error {
		for k, v := range kv {
			if _, err := tx.Exec(`INSERT INTO site_settings (key, value) VALUES (?, ?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- 邮件发信 / 人机验证设置 ----------

// Security 是后台「邮件」「安全」两页的设置项,同样落在 site_settings 里。
// 口令类字段(SMTPPass / TurnstileSecret)只在服务端使用,不回显到页面。
type Security struct {
	SMTPHost   string
	SMTPPort   string // 常见 587(STARTTLS)/ 465(SSL)
	SMTPUser   string
	SMTPPass   string
	SMTPFrom   string // 发件人地址
	SMTPSecure string // starttls | ssl | none

	EmailRegister bool // 注册必须填邮箱并点验证链接

	TurnstileSite   string
	TurnstileSecret string
	TurnstileOn     bool
}

// MailReady 发信配置是否齐全(缺一项就当没配)。
func (sec Security) MailReady() bool {
	return sec.SMTPHost != "" && sec.SMTPPort != "" && sec.SMTPFrom != ""
}

// EmailRegisterOn 邮件注册是否真正生效:开关打开且发信配置齐全。
// 没配 SMTP 就打开开关会把所有人挡在注册门外,所以这里带上前置条件。
func (sec Security) EmailRegisterOn() bool { return sec.EmailRegister && sec.MailReady() }

// CaptchaOn 人机验证是否真正生效:开关打开且两个密钥都填了。
func (sec Security) CaptchaOn() bool {
	return sec.TurnstileOn && sec.TurnstileSite != "" && sec.TurnstileSecret != ""
}

const (
	keySMTPHost      = "smtp_host"
	keySMTPPort      = "smtp_port"
	keySMTPUser      = "smtp_user"
	keySMTPPass      = "smtp_pass"
	keySMTPFrom      = "smtp_from"
	keySMTPSecure    = "smtp_secure"
	keyEmailRegister = "email_register"
	keyTSSite        = "turnstile_site"
	keyTSSecret      = "turnstile_secret"
	keyTSOn          = "turnstile_on"
)

// Security 读出邮件与人机验证设置。
func (s *Store) Security() (Security, error) {
	sec := Security{SMTPPort: "587", SMTPSecure: "starttls"}
	rows, err := s.DB.Query(`SELECT key, value FROM site_settings`)
	if err != nil {
		return sec, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return sec, err
		}
		switch k {
		case keySMTPHost:
			sec.SMTPHost = v
		case keySMTPPort:
			if v != "" {
				sec.SMTPPort = v
			}
		case keySMTPUser:
			sec.SMTPUser = v
		case keySMTPPass:
			sec.SMTPPass = v
		case keySMTPFrom:
			sec.SMTPFrom = v
		case keySMTPSecure:
			if v != "" {
				sec.SMTPSecure = v
			}
		case keyEmailRegister:
			sec.EmailRegister = v == "1"
		case keyTSSite:
			sec.TurnstileSite = v
		case keyTSSecret:
			sec.TurnstileSecret = v
		case keyTSOn:
			sec.TurnstileOn = v == "1"
		}
	}
	return sec, rows.Err()
}

// SetMailSettings 保存 SMTP 与邮件注册开关。pass 传空串表示「不修改口令」。
func (s *Store) SetMailSettings(host, port, user, pass, from, secure string, emailRegister bool) error {
	kv := map[string]string{
		keySMTPHost:      host,
		keySMTPPort:      port,
		keySMTPUser:      user,
		keySMTPFrom:      from,
		keySMTPSecure:    secure,
		keyEmailRegister: boolFlag(emailRegister),
	}
	if pass != "" {
		kv[keySMTPPass] = pass
	}
	return s.setSettings(kv)
}

// ClearMailPass 清空已保存的 SMTP 口令。
func (s *Store) ClearMailPass() error { return s.setSettings(map[string]string{keySMTPPass: ""}) }

// SetCaptchaSettings 保存 Turnstile 配置。secret 传空串表示「不修改密钥」。
func (s *Store) SetCaptchaSettings(site, secret string, on bool) error {
	kv := map[string]string{keyTSSite: site, keyTSOn: boolFlag(on)}
	if secret != "" {
		kv[keyTSSecret] = secret
	}
	return s.setSettings(kv)
}

// ClearCaptchaSecret 清空已保存的 Turnstile 密钥。
func (s *Store) ClearCaptchaSecret() error {
	return s.setSettings(map[string]string{keyTSSecret: ""})
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
