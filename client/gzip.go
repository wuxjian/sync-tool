package main

import (
	"compress/gzip"
	"io"
)

// newGzipReader 简单包装，避免循环 import
func newGzipReader(r io.Reader) (io.ReadCloser, error) {
	return gzip.NewReader(r)
}
