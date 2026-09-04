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

// Open 打开数据库并应用 pragmas。dsn 形如 "file:/data/bbs.db"。
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
	ID           int64
	Name         string
	Email        string
	PasswordHash string
	Role         string // user | mod | admin
	AvatarPath   string
	Bio          string
	CreatedAt    int64
	BannedUntil  sql.NullInt64
	BadgeText    sql.NullString // NULL=跟随身份; ''=隐藏; 非空=自定义称号
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
	ID           int64
	CategoryID   int64
	CategorySlug string
	CategoryName string
	AuthorID     int64
	AuthorName   string
	AuthorAvatar string
	Title        string
	IsPinned     bool
	IsLocked     bool
	CreatedAt    int64
	LastPostAt   int64
	ViewCount    int64
	PostCount    int64 // 含首帖
	LastPostBy   string
}

// ReplyCount 是用户视角的"回复数"(不含首帖)。
func (t *Thread) ReplyCount() int64 { return t.PostCount - 1 }

type Post struct {
	ID           int64
	ThreadID     int64
	AuthorID     int64
	AuthorName   string
	AuthorAvatar string
	AuthorRole   string
	AuthorBadge  sql.NullString
	ContentMD    string
	ContentHTML  string
	IsFirst      bool
	CreatedAt    int64
	EditedAt     sql.NullInt64
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
		`INSERT INTO users (name, email, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, emailArg, passwordHash, role, time.Now().Unix())
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
	COALESCE(bio,''), created_at, banned_until, badge_text`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.PasswordHash, &u.Role, &u.AvatarPath,
		&u.Bio, &u.CreatedAt, &u.BannedUntil, &u.BadgeText)
	return u, err
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
	err := s.DB.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM follows WHERE follower_id = ?),
			(SELECT COUNT(*) FROM follows WHERE followee_id = ?),
			(SELECT COUNT(*) FROM post_likes pl JOIN posts p ON p.id = pl.post_id WHERE p.author_id = ?)`,
		userID, userID, userID).Scan(&st.Following, &st.Followers, &st.Liked)
	return st, err
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
		SELECT u.id, u.name, u.role, COALESCE(u.avatar_path,''), u.badge_text, s.csrf_token
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ?
		  AND (u.banned_until IS NULL OR u.banned_until <= ?)`, token, now, now).
		Scan(&u.ID, &u.Name, &u.Role, &u.AvatarPath, &u.BadgeText, &csrf)
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
	t.title, t.is_pinned, t.is_locked,
	t.created_at, t.last_post_at, t.view_count, t.post_count,
	COALESCE((SELECT lu.name FROM posts lp JOIN users lu ON lu.id = lp.author_id
	          WHERE lp.thread_id = t.id ORDER BY lp.id DESC LIMIT 1), u.name)`

const threadFrom = ` FROM threads t
	JOIN users u ON u.id = t.author_id
	JOIN categories c ON c.id = t.category_id `

func scanThread(row interface{ Scan(...any) error }) (*Thread, error) {
	t := &Thread{}
	var pinned, locked int64
	err := row.Scan(&t.ID, &t.CategoryID, &t.CategorySlug, &t.CategoryName,
		&t.AuthorID, &t.AuthorName, &t.AuthorAvatar,
		&t.Title, &pinned, &locked, &t.CreatedAt, &t.LastPostAt, &t.ViewCount, &t.PostCount, &t.LastPostBy)
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
		q += ` ORDER BY t.is_pinned DESC, t.post_count DESC, t.last_post_at DESC`
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

// DeleteCategory 删除空版块;非空时调用方需拒绝(删除会级联清空主题)。
func (s *Store) DeleteCategory(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM categories WHERE id = ?`, id)
	return err
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

// CreateThread 建主题 + 首帖,同一事务里维护计数。
func (s *Store) CreateThread(catID, authorID int64, title, contentMD, contentHTML string) (int64, error) {
	var threadID int64
	now := time.Now().Unix()
	err := s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO threads (category_id, author_id, title, created_at, last_post_at, post_count)
			 VALUES (?, ?, ?, ?, ?, 1)`, catID, authorID, title, now, now)
		if err != nil {
			return err
		}
		threadID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT INTO posts (thread_id, author_id, content_md, content_html, is_first, created_at)
			 VALUES (?, ?, ?, ?, 1, ?)`, threadID, authorID, contentMD, contentHTML, now)
		return err
	})
	return threadID, err
}

// ---------- posts ----------

const postCols = `
	p.id, p.thread_id, p.author_id, u.name, COALESCE(u.avatar_path,''), u.role, u.badge_text,
	p.content_md, p.content_html,
	p.is_first, p.created_at, p.edited_at`

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	p := &Post{}
	var first int64
	err := row.Scan(&p.ID, &p.ThreadID, &p.AuthorID, &p.AuthorName, &p.AuthorAvatar,
		&p.AuthorRole, &p.AuthorBadge, &p.ContentMD, &p.ContentHTML,
		&first, &p.CreatedAt, &p.EditedAt)
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

func (s *Store) CreateNotification(userID, actorID int64, ntype string, threadID, postID int64) error {
	var postArg any
	if postID > 0 {
		postArg = postID
	}
	_, err := s.DB.Exec(
		`INSERT INTO notifications (user_id, actor_id, type, thread_id, post_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, actorID, ntype, threadID, postArg, time.Now().Unix())
	return err
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

// DeleteThread 删除整个主题(posts 级联)。
func (s *Store) DeleteThread(threadID int64) error {
	_, err := s.DB.Exec(`DELETE FROM threads WHERE id = ?`, threadID)
	return err
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
