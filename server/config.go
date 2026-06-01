package main

import (
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Config 服务端配置
type Config struct {
	Listen string       `yaml:"listen"`
	Token  string       `yaml:"token"`
	Root   string       `yaml:"root"`
	Filter FilterConfig `yaml:"filter"`
}

// FilterConfig 文件过滤配置
type FilterConfig struct {
	// 排除的文件后缀（小写，含点）
	ExcludeExt []string `yaml:"exclude_ext"`
	// 排除的文件名/相对路径正则
	ExcludePattern []string `yaml:"exclude_pattern"`
	// 文件大小上限（字节），0 表示不限制
	MaxSize int64 `yaml:"max_size"`
}

// Filter 过滤规则（编译后的）
type Filter struct {
	cfg            FilterConfig
	excludeExt     map[string]struct{}
	excludeRegexps []*regexp.Regexp
}

// LoadConfig 从文件加载配置
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Listen: ":8080",
		Token:  "",
		Root:   ".",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// NewFilter 构造过滤器
func NewFilter(cfg FilterConfig) (*Filter, error) {
	f := &Filter{
		cfg:        cfg,
		excludeExt: make(map[string]struct{}),
	}
	for _, e := range cfg.ExcludeExt {
		f.excludeExt[e] = struct{}{}
	}
	for _, p := range cfg.ExcludePattern {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		f.excludeRegexps = append(f.excludeRegexps, re)
	}
	return f, nil
}

// Allow 判断文件是否允许同步
// relPath 是相对 root 的路径
func (f *Filter) Allow(relPath string, size int64) bool {
	if f == nil {
		return true
	}
	if f.cfg.MaxSize > 0 && size > f.cfg.MaxSize {
		return false
	}
	ext := getExt(relPath)
	if _, ok := f.excludeExt[ext]; ok {
		return false
	}
	for _, re := range f.excludeRegexps {
		if re.MatchString(relPath) {
			return false
		}
	}
	return true
}

func getExt(p string) string {
	for i := len(p) - 1; i >= 0 && p[i] != '/'; i-- {
		if p[i] == '.' {
			return lower(p[i:])
		}
	}
	return ""
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
