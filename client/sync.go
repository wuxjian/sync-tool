package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileStatus 标记远端文件相对本地的同步状态
type FileStatus struct {
	FileInfo
	Status     string `json:"status"` // "new" | "modified" | "same" | "dir"
	LocalSize  int64  `json:"local_size,omitempty"`
	LocalMTime int64  `json:"local_mtime,omitempty"`
}

// LocalFile 本地文件（用于展示本地树）
type LocalFile struct {
	RelPath string `json:"rel_path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	Exists  bool   `json:"exists"`
	SHA256  string `json:"sha256,omitempty"`
}

// SyncEvent 同步过程事件
type SyncEvent struct {
	Type     string `json:"type"` // "start", "skip", "download", "verify", "done", "error", "progress"
	RelPath  string `json:"rel_path"`
	Total    int64  `json:"total,omitempty"`
	Current  int64  `json:"current,omitempty"`
	Message  string `json:"message,omitempty"`
}

// SyncStatus 全局同步状态
type SyncStatus struct {
	mu        sync.Mutex
	running   bool
	total     int64
	completed int64
	skipped   int64
	failed    int64
	startedAt time.Time
	events    []SyncEvent
	maxEvents int
}

// Syncer 同步器
type Syncer struct {
	cfg     *Config
	remote  *RemoteClient
	meta    *MetaStore
	status  *SyncStatus
	hashWG  sync.WaitGroup

	cancelMu sync.Mutex
	cancel   context.CancelFunc // 当前运行任务的 cancel；运行时非空
}

func NewSyncer(cfg *Config, remote *RemoteClient, meta *MetaStore) *Syncer {
	return &Syncer{
		cfg:    cfg,
		remote: remote,
		meta:   meta,
		status: &SyncStatus{
			maxEvents: 500,
		},
	}
}

// GetStatus 复制当前状态给前端
func (s *Syncer) GetStatus() map[string]any {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	return map[string]any{
		"running":    s.status.running,
		"total":      s.status.total,
		"completed":  s.status.completed,
		"skipped":    s.status.skipped,
		"failed":     s.status.failed,
		"started_at": s.status.startedAt,
		"events":     append([]SyncEvent(nil), s.status.events...),
	}
}

// ComputeDiff 拉取远端目录树并与本地 meta 对比，标记每个文件的状态
// depth=0 表示无限深度
func (s *Syncer) ComputeDiff(ctx context.Context, rel string, depth int) ([]FileStatus, error) {
	rel = sanitizePath(rel)
	items, err := s.remote.Tree(rel, depth)
	if err != nil {
		return nil, err
	}
	out := make([]FileStatus, 0, len(items))
	for _, it := range items {
		if it.IsDir {
			// 目录不参与 diff 计算，只保留信息
			out = append(out, FileStatus{FileInfo: it, Status: "dir"})
			continue
		}
		_, hasMeta := s.meta.Get(it.RelPath)
		fs := FileStatus{FileInfo: it}
		// 磁盘实际状态（用于展示本地 size/mtime，并判断是否 modified）
		diskInfo, diskErr := os.Stat(filepath.Join(s.cfg.Local.Root, filepath.FromSlash(it.RelPath)))
		if !hasMeta && diskErr != nil {
			// meta 也没有，磁盘也没有 -> 真·新文件
			fs.Status = "new"
			out = append(out, fs)
			continue
		}
		if diskErr == nil {
			fs.LocalSize = diskInfo.Size()
			fs.LocalMTime = diskInfo.ModTime().Unix()
		}
		// 判定：磁盘 size+mtime 与远端一致 -> same，否则 modified
		if diskErr == nil && diskInfo.Size() == it.Size && diskInfo.ModTime().Unix() == it.MTime {
			fs.Status = "same"
		} else {
			fs.Status = "modified"
		}
		out = append(out, fs)
	}
	return out, nil
}

// LocalTree 从本地 meta 构造文件树
// 返回的文件状态永远是 "local"（已同步过），用于前端展示
func (s *Syncer) LocalTree() ([]LocalFile, error) {
	keys := s.meta.Keys()
	out := make([]LocalFile, 0, len(keys))
	for _, k := range keys {
		m, ok := s.meta.Get(k)
		if !ok {
			continue
		}
		rel := k
		name := rel
		if i := lastIndexSlash(rel); i >= 0 {
			name = rel[i+1:]
		}
		full := filepath.Join(s.cfg.Local.Root, filepath.FromSlash(rel))
		_, statErr := os.Stat(full)
		out = append(out, LocalFile{
			RelPath: rel,
			Name:    name,
			Size:    m.Size,
			MTime:   m.MTime,
			Exists:  statErr == nil,
			SHA256:  m.SHA256,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
}

func lastIndexSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func (s *Syncer) addEvent(ev SyncEvent) {
	s.status.mu.Lock()
	s.status.events = append(s.status.events, ev)
	if len(s.status.events) > s.status.maxEvents {
		// 保留尾部
		s.status.events = s.status.events[len(s.status.events)-s.status.maxEvents:]
	}
	s.status.mu.Unlock()
}

// IsRunning 是否在同步中
func (s *Syncer) IsRunning() bool {
	s.status.mu.Lock()
	defer s.status.mu.Unlock()
	return s.status.running
}

// Sync 启动一次同步
// paths 是要同步的相对路径列表（可能是目录或文件）
// ctx 作为父上下文（其取消会传导至本次任务），并可通过 Cancel() 主动取消
func (s *Syncer) Sync(ctx context.Context, paths []string) error {
	if s.IsRunning() {
		return errors.New("已有同步任务在运行中")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancelMu.Lock()
	s.cancel = cancel
	s.cancelMu.Unlock()

	s.status.mu.Lock()
	s.status.running = true
	s.status.total = 0
	s.status.completed = 0
	s.status.skipped = 0
	s.status.failed = 0
	s.status.startedAt = time.Now()
	s.status.events = s.status.events[:0]
	s.status.mu.Unlock()

	go func() {
		cancelled := false
		defer func() {
			s.cancelMu.Lock()
			s.cancel = nil
			s.cancelMu.Unlock()
			cancel() // 释放 ctx
			s.status.mu.Lock()
			s.status.running = false
			s.status.mu.Unlock()
			s.hashWG.Wait()
			_ = s.meta.Save()
			if cancelled {
				s.addEvent(SyncEvent{Type: "cancelled", Message: "同步已取消"})
			} else {
				s.addEvent(SyncEvent{Type: "done", Message: "同步结束"})
			}
		}()

		// 1. 展开路径为文件列表
		files, err := s.expandPaths(runCtx, paths)
		if err != nil {
			if runCtx.Err() != nil {
				cancelled = true
			} else {
				s.addEvent(SyncEvent{Type: "error", Message: "展开路径失败: " + err.Error()})
			}
			return
		}
		s.status.mu.Lock()
		s.status.total = int64(len(files))
		s.status.mu.Unlock()

		s.addEvent(SyncEvent{Type: "start", Message: fmt.Sprintf("共 %d 个文件", len(files))})

		// 2. 逐个比对并下载
		for _, f := range files {
			if runCtx.Err() != nil {
				cancelled = true
				return
			}
			if err := s.syncOne(runCtx, f); err != nil {
				if runCtx.Err() != nil {
					cancelled = true
					return
				}
				s.status.mu.Lock()
				s.status.failed++
				s.status.mu.Unlock()
				s.addEvent(SyncEvent{Type: "error", RelPath: f.RelPath, Message: err.Error()})
			}
		}
	}()
	return nil
}

// Cancel 请求取消当前正在运行的同步任务（幂等：未运行时不报错）
func (s *Syncer) Cancel() {
	s.cancelMu.Lock()
	c := s.cancel
	s.cancelMu.Unlock()
	if c != nil {
		c()
	}
}

// expandPaths 将用户选择的路径展开为具体文件列表
func (s *Syncer) expandPaths(ctx context.Context, paths []string) ([]FileInfo, error) {
	// 大量文件（>20）时直接取整棵树，避免逐个请求
	if len(paths) > 20 {
		return s.remote.Tree("", 0)
	}
	var out []FileInfo
	for _, p := range paths {
		rel := sanitizePath(p)
		if rel == "" {
			items, err := s.remote.Tree("", 0)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			continue
		}
		isDir, err := s.isRemoteDir(ctx, rel)
		if err != nil {
			isDir = false
		}
		if isDir {
			items, err := s.remote.Tree(rel, 0)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
		} else {
			fi, err := s.remote.HashInfo(rel)
			if err != nil {
				s.addEvent(SyncEvent{Type: "error", RelPath: rel, Message: "获取文件信息失败: " + err.Error()})
				continue
			}
			out = append(out, *fi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
}

func (s *Syncer) isRemoteDir(ctx context.Context, rel string) (bool, error) {
	parent := parentDir(rel)
	name := filepath.Base(rel)
	items, err := s.remote.ListDir(parent)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if it.Name == name {
			return it.IsDir, nil
		}
	}
	return false, fmt.Errorf("path not found: %s", rel)
}

func parentDir(p string) string {
	p = sanitizePath(p)
	if i := lastSlash(p); i >= 0 {
		return p[:i]
	}
	return ""
}

func lastSlash(p string) int {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return i
		}
	}
	return -1
}

// syncOne 同步单个文件
func (s *Syncer) syncOne(ctx context.Context, fi FileInfo) error {
	local := filepath.Join(s.cfg.Local.Root, filepath.FromSlash(fi.RelPath))

	// 0. 目录条目：创建本地目录
	if fi.IsDir {
		if err := os.MkdirAll(local, 0755); err != nil {
			return err
		}
		s.status.mu.Lock()
		s.status.completed++
		s.status.mu.Unlock()
		s.addEvent(SyncEvent{Type: "skip", RelPath: fi.RelPath, Message: "创建目录"})
		return nil
	}

	// 1. 以磁盘文件实际状态为准判断是否需要下载
	// 信任磁盘而不是 meta，因为用户可能手动改过本地文件
	if info, err := os.Stat(local); err == nil {
		if info.Size() == fi.Size && info.ModTime().Unix() == fi.MTime {
			// 磁盘和远端一致，跳过；同时把 meta 校准
			s.meta.Set(fi.RelPath, FileMeta{Size: fi.Size, MTime: fi.MTime})
			s.status.mu.Lock()
			s.status.skipped++
			s.status.completed++
			s.status.mu.Unlock()
			s.addEvent(SyncEvent{Type: "skip", RelPath: fi.RelPath, Message: "大小+mtime一致"})
			return nil
		}
	}

	// 2. 准备本地目录
	if err := os.MkdirAll(filepath.Dir(local), 0755); err != nil {
		return err
	}

	// 3. 下载（带 ctx 控制）
	throttle := newProgressThrottle(100 * time.Millisecond)
	s.addEvent(SyncEvent{Type: "download", RelPath: fi.RelPath, Total: fi.Size})
	err := s.remote.DownloadToFile(ctx, fi.RelPath, local, fi.Size, func(d int64) {
		if throttle.allow() {
			s.addEvent(SyncEvent{Type: "progress", RelPath: fi.RelPath, Total: fi.Size, Current: d})
		}
	})
	if err != nil {
		// 失败删除半成品
		_ = os.Remove(local)
		return err
	}

	// 4. 验证大小
	if info, err := os.Stat(local); err == nil {
		if fi.Size > 0 && info.Size() != fi.Size {
			_ = os.Remove(local)
			return fmt.Errorf("下载后大小不匹配: 期望 %d, 实际 %d", fi.Size, info.Size())
		}
		// 修正 mtime
		_ = os.Chtimes(local, time.Unix(fi.MTime, 0), time.Unix(fi.MTime, 0))
	}

	// 5. 先同步写入基本元信息（size+mtime），保证下次比对能命中跳过
	s.meta.Set(fi.RelPath, FileMeta{Size: fi.Size, MTime: fi.MTime})

	// 6. 异步算 SHA256 并 update meta，遵循 ctx 取消
	s.hashWG.Add(1)
	go func(rel string, p string, size, mtime int64) {
		defer s.hashWG.Done()
		if ctx.Err() != nil {
			return
		}
		h, err := hashLocalFile(p)
		if err == nil {
			s.meta.Set(rel, FileMeta{Size: size, MTime: mtime, SHA256: h})
		}
	}(fi.RelPath, local, fi.Size, fi.MTime)

	s.status.mu.Lock()
	s.status.completed++
	s.status.mu.Unlock()
	s.addEvent(SyncEvent{Type: "verify", RelPath: fi.RelPath, Message: "完成"})
	return nil
}

// progressThrottle 限制回调频率（最后一次调用必触发，避免收尾丢数据）
type progressThrottle struct {
	minInterval time.Duration
	last        time.Time
}

func newProgressThrottle(d time.Duration) *progressThrottle {
	return &progressThrottle{minInterval: d}
}

func (p *progressThrottle) allow() bool {
	now := time.Now()
	if now.Sub(p.last) >= p.minInterval {
		p.last = now
		return true
	}
	return false
}

func hashLocalFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
