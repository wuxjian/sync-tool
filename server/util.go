package main

import (
	"path/filepath"
)

// absResolve 跨平台解析绝对路径
func absResolve(p string) (string, error) {
	return filepath.Abs(p)
}
