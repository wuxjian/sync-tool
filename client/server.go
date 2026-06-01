package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
)

//go:embed all:web
var webFS embed.FS

// LocalServer 客户端内置的本地 HTTP 服务
// 提供：HTML 页面、内部 API（浏览/同步/状态）
type LocalServer struct {
	cfg    *Config
	remote *RemoteClient
	syncer *Syncer
	mux    *http.ServeMux
}

func NewLocalServer(cfg *Config, remote *RemoteClient, syncer *Syncer) *LocalServer {
	s := &LocalServer{cfg: cfg, remote: remote, syncer: syncer, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *LocalServer) Handler() http.Handler {
	return s.mux
}

func (s *LocalServer) routes() {
	// 静态资源
	sub, _ := fs.Sub(webFS, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))

	// 内部 API
	s.mux.HandleFunc("/api/tree", s.handleTree)
	s.mux.HandleFunc("/api/list", s.handleList)
	s.mux.HandleFunc("/api/info", s.handleInfo)
	s.mux.HandleFunc("/api/diff", s.handleDiff)
	s.mux.HandleFunc("/api/local/tree", s.handleLocalTree)
	s.mux.HandleFunc("/api/sync", s.handleSync)
	s.mux.HandleFunc("/api/sync/cancel", s.handleSyncCancel)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/config", s.handleConfig)
}

func (s *LocalServer) handleTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rel := q.Get("path")
	depth, _ := strconv.Atoi(q.Get("depth"))
	items, err := s.remote.Tree(rel, depth)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"path": rel, "items": items})
}

func (s *LocalServer) handleList(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	items, err := s.remote.ListDir(rel)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"path": rel, "items": items})
}

func (s *LocalServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	fi, err := s.remote.HashInfo(rel)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, fi)
}

func (s *LocalServer) handleDiff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rel := q.Get("path")
	depth, _ := strconv.Atoi(q.Get("depth"))
	items, err := s.syncer.ComputeDiff(r.Context(), rel, depth)
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"path": rel, "items": items})
}

func (s *LocalServer) handleLocalTree(w http.ResponseWriter, r *http.Request) {
	items, err := s.syncer.LocalTree()
	if err != nil {
		writeJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

type syncRequest struct {
	Paths []string `json:"paths"`
}

func (s *LocalServer) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	var req syncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid json"})
		return
	}
	if len(req.Paths) == 0 {
		writeJSON(w, 400, map[string]any{"error": "paths is required"})
		return
	}
	// 清理 + 去重（保序）
	seen := make(map[string]struct{}, len(req.Paths))
	paths := make([]string, 0, len(req.Paths))
	for _, p := range req.Paths {
		p = sanitizePath(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		writeJSON(w, 400, map[string]any{"error": "paths is required"})
		return
	}
	if err := s.syncer.Sync(r.Context(), paths); err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "同步任务已启动"})
}

func (s *LocalServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.syncer.GetStatus())
}

// handleSyncCancel 取消当前同步任务（幂等）
func (s *LocalServer) handleSyncCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.syncer.IsRunning() {
		writeJSON(w, 200, map[string]any{"ok": true, "message": "当前没有正在运行的同步任务"})
		return
	}
	s.syncer.Cancel()
	writeJSON(w, 200, map[string]any{"ok": true, "message": "已请求取消"})
}

func (s *LocalServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	// 返回部分安全配置给前端展示
	writeJSON(w, 200, map[string]any{
		"remote_url": s.cfg.Remote.URL,
		"local_root": s.cfg.Local.Root,
		"meta_file":  s.cfg.Local.MetaFile,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
