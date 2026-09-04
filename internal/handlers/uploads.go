// 图片上传与回访。文件落在 BBS_UPLOADS 目录,uploads 表记录元数据,
// 对外只暴露 /uploads/{id} 稳定 URL(内部文件名随机,不直接暴露路径)。
package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bbs/internal/auth"
)

const maxImageBytes = 5 << 20 // 单张 ≤5MB

var imageExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// uploadImage POST /uploads:登录用户上传图片,成功返回 {"url":"/uploads/{id}"}。
func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(w, r)
	if user == nil {
		return
	}
	if err := r.ParseMultipartForm(maxImageBytes + 1<<20); err != nil {
		http.Error(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	id, ok := s.saveImageUpload(w, r, "file", user.ID)
	if !ok {
		return
	}
	writeJSON(w, map[string]string{"url": "/uploads/" + strconv.FormatInt(id, 10)})
}

// serveUpload GET /uploads/{id}:按上传记录回访文件(公开,供帖子/头像引用)。
func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, err := s.store.GetUpload(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if u == nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(filepath.Join(s.uploads, filepath.Base(u.Path)))
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", u.Mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, filepath.Base(u.Path), time.Unix(u.CreatedAt, 0), file)
}

// saveImageUpload 读取表单图片字段,校验类型与大小后落盘并登记;失败时已写响应。
func (s *Server) saveImageUpload(w http.ResponseWriter, r *http.Request, field string, userID int64) (int64, bool) {
	f, hdr, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		http.Error(w, "请选择图片", http.StatusBadRequest)
		return 0, false
	}
	if err != nil {
		s.serverError(w, err)
		return 0, false
	}
	defer f.Close()
	if hdr.Size <= 0 || hdr.Size > maxImageBytes {
		http.Error(w, "图片大小需在 1B–5MB 之间", http.StatusRequestEntityTooLarge)
		return 0, false
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	mime := http.DetectContentType(head[:n])
	ext, allowed := imageExt[mime]
	if !allowed {
		http.Error(w, "仅支持图片(jpg/png/gif/webp)", http.StatusBadRequest)
		return 0, false
	}
	data := head[:n]
	if hdr.Size > int64(n) {
		rest := make([]byte, hdr.Size-int64(n))
		if _, err := io.ReadFull(f, rest); err != nil {
			s.serverError(w, err)
			return 0, false
		}
		data = append(data, rest...)
	}
	name := auth.NewToken()[:32] + ext
	if err := os.WriteFile(filepath.Join(s.uploads, name), data, 0o644); err != nil {
		s.serverError(w, err)
		return 0, false
	}
	id, err := s.store.CreateUpload(userID, name, mime, hdr.Size)
	if err != nil {
		os.Remove(filepath.Join(s.uploads, name))
		s.serverError(w, err)
		return 0, false
	}
	return id, true
}

// removeUploadFile 删除某次上传的文件与记录(换头像时清理旧图,失败静默)。
func (s *Server) removeUploadFile(id int64) {
	u, err := s.store.GetUpload(id)
	if err != nil || u == nil {
		return
	}
	os.Remove(filepath.Join(s.uploads, filepath.Base(u.Path)))
	s.store.DeleteUpload(id)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// uploadPathID 把 avatar_path 里的 "/uploads/{id}" 解析成上传记录 id。
func uploadPathID(p string) (int64, bool) {
	rest, ok := strings.CutPrefix(p, "/uploads/")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}
