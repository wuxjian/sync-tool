package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// FileInfo 单个文件信息
type FileInfo struct {
	// 相对 root 的路径，使用正斜杠
	RelPath  string `json:"rel_path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"` // unix 秒
	IsDir    bool   `json:"is_dir"`
	SHA256   string `json:"sha256,omitempty"`
}

// DirEntry 目录项
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// safeJoin 安全拼接路径，防止越界访问
// root 必须是绝对路径
func safeJoin(root, rel string) (string, error) {
	if rel == "" {
		return root, nil
	}
	// 将 \ 转为 /，避免 Windows 风格路径
	rel = filepath.ToSlash(rel)
	clean := filepath.Clean("/" + rel)
	full := filepath.Join(root, clean)
	// 解析后必须仍以 root 为前缀
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if absFull != absRoot && !hasPrefix(absFull, absRoot+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	return absFull, nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ListDir 列出目录内容
func ListDir(root, rel string, f *Filter) ([]DirEntry, error) {
	full, err := safeJoin(root, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		// 隐藏文件直接跳过（与服务端 filter 配合）
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		relPath := joinRel(rel, e.Name())
		if f != nil && !e.IsDir() && !f.Allow(relPath, info.Size()) {
			continue
		}
		out = append(out, DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// StatFile 获取单个文件信息
func StatFile(root, rel string) (*FileInfo, error) {
	full, err := safeJoin(root, rel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	fi := &FileInfo{
		RelPath: filepath.ToSlash(rel),
		Name:    st.Name(),
		Size:    st.Size(),
		MTime:   st.ModTime().Unix(),
		IsDir:   st.IsDir(),
	}
	return fi, nil
}

// HashFile 计算文件 SHA256（流式）
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
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

// WalkTree 递归生成文件树
// maxDepth 为 0 表示只列出当前目录
func WalkTree(root, rel string, f *Filter, maxDepth, curDepth int) ([]FileInfo, error) {
	full, err := safeJoin(root, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		relPath := joinRel(rel, e.Name())
		if e.IsDir() {
			out = append(out, FileInfo{
				RelPath: filepath.ToSlash(relPath),
				Name:    e.Name(),
				Size:    info.Size(),
				MTime:   info.ModTime().Unix(),
				IsDir:   true,
			})
			if maxDepth == 0 || curDepth < maxDepth {
				children, err := WalkTree(root, relPath, f, maxDepth, curDepth+1)
				if err == nil {
					out = append(out, children...)
				}
			}
		} else {
			if f != nil && !f.Allow(relPath, info.Size()) {
				continue
			}
			out = append(out, FileInfo{
				RelPath: filepath.ToSlash(relPath),
				Name:    e.Name(),
				Size:    info.Size(),
				MTime:   info.ModTime().Unix(),
				IsDir:   false,
			})
		}
	}
	return out, nil
}

func joinRel(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}
