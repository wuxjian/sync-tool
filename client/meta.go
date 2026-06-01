package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// FileMeta 本地保存的文件元信息
type FileMeta struct {
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime"`
	SHA256 string `json:"sha256,omitempty"`
}

// MetaStore 元信息存储
type MetaStore struct {
	path string
	mu   sync.Mutex
	data map[string]FileMeta
}

// NewMetaStore 加载元信息文件，文件不存在则初始化空
func NewMetaStore(path string) (*MetaStore, error) {
	store := &MetaStore{
		path: path,
		data: make(map[string]FileMeta),
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return store, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&store.data); err != nil {
		// 解析失败不致命，重置
		store.data = make(map[string]FileMeta)
	}
	return store, nil
}

// Get 获取元信息
func (m *MetaStore) Get(rel string) (FileMeta, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[normalizeKey(rel)]
	return v, ok
}

// Set 设置元信息
func (m *MetaStore) Set(rel string, meta FileMeta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[normalizeKey(rel)] = meta
}

// Delete 删除元信息
func (m *MetaStore) Delete(rel string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, normalizeKey(rel))
}

// Save 原子保存到磁盘
func (m *MetaStore) Save() error {
	m.mu.Lock()
	clone := make(map[string]FileMeta, len(m.data))
	for k, v := range m.data {
		clone[k] = v
	}
	m.mu.Unlock()

	// 确保父目录存在
	if dir := filepath.Dir(m.path); dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	tmp := m.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(clone); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, m.path)
}

// Keys 返回所有 key 列表
func (m *MetaStore) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.data))
	for k := range m.data {
		out = append(out, k)
	}
	return out
}

func normalizeKey(p string) string {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' {
			c = '/'
		}
		if c == '/' && len(out) > 0 && out[len(out)-1] == '/' {
			continue
		}
		out = append(out, c)
	}
	// 去掉前缀 "/"
	if len(out) > 0 && out[0] == '/' {
		out = out[1:]
	}
	return string(out)
}
