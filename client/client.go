package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// RemoteClient 远程服务端 HTTP 客户端
type RemoteClient struct {
	base   string
	token  string
	http   *http.Client
}

func NewRemoteClient(base, token string) *RemoteClient {
	return &RemoteClient{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http: &http.Client{
			Timeout: 0, // 大文件不超时
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// FileInfo 与服务端结构对应
type FileInfo struct {
	RelPath string `json:"rel_path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"`
	IsDir   bool   `json:"is_dir"`
	SHA256  string `json:"sha256,omitempty"`
}

// DirEntry 目录项
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

func (c *RemoteClient) do(req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

func (c *RemoteClient) buildURL(p string, q url.Values) string {
	u := c.base + p
	if q != nil {
		u += "?" + q.Encode()
	}
	return u
}

// ListDir 列出目录
func (c *RemoteClient) ListDir(rel string) ([]DirEntry, error) {
	q := url.Values{}
	if rel != "" {
		q.Set("path", rel)
	}
	req, _ := http.NewRequest("GET", c.buildURL("/api/list", q), nil)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("list: status %d", resp.StatusCode)
	}
	var out struct {
		Path  string     `json:"path"`
		Items []DirEntry `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Tree 拉取递归目录树
func (c *RemoteClient) Tree(rel string, depth int) ([]FileInfo, error) {
	q := url.Values{}
	if rel != "" {
		q.Set("path", rel)
	}
	if depth > 0 {
		q.Set("depth", strconv.Itoa(depth))
	}
	url := c.buildURL("/api/tree", q)
	req, _ := http.NewRequest("GET", url, nil)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tree: status %d", resp.StatusCode)
	}
	var out struct {
		Path  string     `json:"path"`
		Items []FileInfo `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// HashInfo 拉取文件 hash + 元信息
func (c *RemoteClient) HashInfo(rel string) (*FileInfo, error) {
	q := url.Values{}
	q.Set("path", rel)
	req, _ := http.NewRequest("GET", c.buildURL("/api/hash", q), nil)
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("hash: status %d", resp.StatusCode)
	}
	var out FileInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadRange 返回 (reader, contentLength, contentEncoding, error)
// 调用方负责关闭 reader
// - start >= 0 时使用 Range 请求
// - end == 0 表示下载到文件结尾；end > 0 表示具体结束位置
// - 接收 gzip 压缩（无 Range 时才压缩）
func (c *RemoteClient) DownloadRange(rel string, start, end int64) (io.ReadCloser, int64, string, error) {
	q := url.Values{}
	q.Set("path", rel)
	req, _ := http.NewRequest("GET", c.buildURL("/api/download", q), nil)
	if start >= 0 {
		// 总是发送 Range 头：end==0 意味着到结尾，服务端按 bytes=start- 解析
		if end > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		}
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.do(req)
	if err != nil {
		return nil, 0, "", err
	}
	if resp.StatusCode != 200 && resp.StatusCode != 206 {
		resp.Body.Close()
		return nil, 0, "", fmt.Errorf("download: status %d", resp.StatusCode)
	}
	cl, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return resp.Body, cl, resp.Header.Get("Content-Encoding"), nil
}

// DownloadWhole 整体下载（小文件）
func (c *RemoteClient) DownloadWhole(rel string) (io.ReadCloser, int64, string, error) {
	return c.DownloadRange(rel, -1, 0)
}

// DownloadToFile 完整下载到文件
func (c *RemoteClient) DownloadToFile(rel, dst string, expectedSize int64, progress func(downloaded int64)) error {
	// 先检查本地是否需要断点续传
	var start int64 = 0
	if info, err := os.Stat(dst); err == nil {
		if expectedSize > 0 && info.Size() < expectedSize {
			start = info.Size()
		} else if expectedSize > 0 && info.Size() == expectedSize {
			// 大小一致就跳过
			return nil
		}
	}

	reader, contentLen, encoding, err := c.DownloadRange(rel, start, 0)
	if err != nil {
		return err
	}
	defer reader.Close()

	// 如果是 gzip，包装 reader
	var src io.Reader = reader
	if encoding == "gzip" {
		gz, err := newGzipReader(reader)
		if err != nil {
			return err
		}
		defer gz.Close()
		src = gz
	}

	// 打开本地文件（追加模式）
	flags := os.O_CREATE | os.O_WRONLY
	if start > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(dst, flags, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// 计算预期总大小（如果服务端使用 gzip，这里无法直接得到）
	// 对于断点续传 + gzip 场景需要服务端支持 head 探测，为简单起见
	// 我们在 expectedSize>0 时使用 expectedSize - start 作为剩余字节数
	remaining := int64(-1)
	if encoding == "" && contentLen > 0 {
		remaining = contentLen
	} else if expectedSize > 0 {
		remaining = expectedSize - start
	}

	buf := make([]byte, 32*1024)
	var downloaded int64 = 0
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			downloaded += int64(n)
			if progress != nil {
				if remaining > 0 {
					progress(downloaded)
				} else {
					progress(downloaded)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

// sanitizePath 清理路径中的非法字符
func sanitizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	return strings.TrimPrefix(p, "/")
}
