package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config 客户端配置
type Config struct {
	Listen string       `yaml:"listen"`
	Remote RemoteConfig `yaml:"remote"`
	Local  LocalConfig  `yaml:"local"`
}

// RemoteConfig 远程服务端配置
type RemoteConfig struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// LocalConfig 本地配置
type LocalConfig struct {
	// 本地同步根目录
	Root string `yaml:"root"`
	// 元信息文件路径（相对于 root 或绝对路径）
	MetaFile string `yaml:"meta_file"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		Listen: ":9090",
		Local: LocalConfig{
			Root:     "./synced",
			MetaFile: "./meta.json",
		},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ResolveLocal 将相对路径转为绝对路径
func (c *Config) ResolveLocal() error {
	abs, err := filepath.Abs(c.Local.Root)
	if err != nil {
		return err
	}
	c.Local.Root = abs

	if !filepath.IsAbs(c.Local.MetaFile) {
		c.Local.MetaFile = filepath.Join(c.Local.Root, c.Local.MetaFile)
	}
	return nil
}
