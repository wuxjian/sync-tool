package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Server 持有共享状态
type Server struct {
	cfg    *Config
	filter *Filter
	mux    *http.ServeMux
}

func NewServer(cfg *Config, f *Filter) *Server {
	s := &Server{cfg: cfg, filter: f, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/list", s.auth(s.handleList))
	s.mux.HandleFunc("/api/info", s.auth(s.handleInfo))
	s.mux.HandleFunc("/api/hash", s.auth(s.handleHash))
	s.mux.HandleFunc("/api/tree", s.auth(s.handleTree))
	s.mux.HandleFunc("/api/download", s.auth(s.handleDownload))
}

// auth token 认证中间件
// 支持两种方式：Authorization: Bearer <token> 或 ?token=<token>
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			// 未配置 token 则不鉴权
			next(w, r)
			return
		}
		tok := extractToken(r)
		if tok != s.cfg.Token {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r)
	}
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(h[len("Bearer "):])
		}
	}
	return r.URL.Query().Get("token")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"root": s.cfg.Root,
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	items, err := ListDir(s.cfg.Root, rel, s.filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  filepath.ToSlash(rel),
		"items": items,
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	fi, err := StatFile(s.cfg.Root, rel)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fi)
}

func (s *Server) handleHash(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safeJoin(s.cfg.Root, rel)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	hash, err := HashFile(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rel_path": filepath.ToSlash(rel),
		"size":     info.Size(),
		"mtime":    info.ModTime().Unix(),
		"sha256":   hash,
	})
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	depthStr := r.URL.Query().Get("depth")
	depth := 0
	if depthStr != "" {
		if d, err := strconv.Atoi(depthStr); err == nil {
			depth = d
		}
	}
	items, err := WalkTree(s.cfg.Root, rel, s.filter, depth, 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  filepath.ToSlash(rel),
		"items": items,
	})
}

// handleDownload 提供文件下载
// - 支持 Range 断点续传
// - 支持 Accept-Encoding: gzip 压缩
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	full, err := safeJoin(s.cfg.Root, rel)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "cannot download a directory")
		return
	}
	if s.filter != nil && !s.filter.Allow(filepath.ToSlash(rel), info.Size()) {
		writeError(w, http.StatusForbidden, "file is filtered")
		return
	}

	f, err := os.Open(full)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	// 计算 ETag：size + mtime + sha256(可选)
	// 为避免每次都算大文件 hash，这里用 size-mtime 作为弱 ETag
	etag := fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().Unix())
	w.Header().Set("ETag", etag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))

	// 嗅探 content-type
	ct := mime.TypeByExtension(filepath.Ext(full))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(full)))

	// 解析 Range
	start, end, useRange, err := parseRange(r.Header.Get("Range"), info.Size())
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size()))
		writeError(w, http.StatusRequestedRangeNotSatisfiable, err.Error())
		return
	}

	var writer io.Writer = w
	var gzWriter *gzip.Writer
	supportsGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")

	if useRange {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, info.Size()))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		if supportsGzip {
			w.Header().Set("Content-Encoding", "gzip")
			gzWriter = gzip.NewWriter(w)
			writer = gzWriter
		} else {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		}
	}

	// 写入内容
	if useRange {
		_, _ = f.Seek(start, io.SeekStart)
		_, _ = io.CopyN(writer, f, end-start+1)
	} else {
		if supportsGzip {
			// 边读边压缩
			buf := make([]byte, 32*1024)
			_, _ = io.CopyBuffer(gzWriter, f, buf)
		} else {
			_, _ = io.Copy(writer, f)
		}
	}
	if gzWriter != nil {
		_ = gzWriter.Close()
	}
}

func parseRange(header string, size int64) (start, end int64, ok bool, err error) {
	if header == "" {
		return 0, size - 1, false, nil
	}
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false, fmt.Errorf("invalid range header")
	}
	spec := strings.TrimPrefix(header, prefix)
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf("invalid range spec")
	}
	if parts[0] == "" {
		// suffix length: bytes=-N
		n, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, false, fmt.Errorf("invalid range suffix")
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, nil
	}
	s, e1 := strconv.ParseInt(parts[0], 10, 64)
	if e1 != nil || s < 0 || s >= size {
		return 0, 0, false, fmt.Errorf("invalid range start")
	}
	if parts[1] == "" {
		return s, size - 1, true, nil
	}
	e, e2 := strconv.ParseInt(parts[1], 10, 64)
	if e2 != nil || e < s || e >= size {
		return 0, 0, false, fmt.Errorf("invalid range end")
	}
	return s, e, true, nil
}

// 工具函数
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
