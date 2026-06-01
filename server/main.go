package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfgFile := resolveServerConfig(*cfgPath)
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	absRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		log.Fatalf("解析 root 路径失败: %v", err)
	}
	cfg.Root = absRoot

	filter, err := NewFilter(cfg.Filter)
	if err != nil {
		log.Fatalf("初始化过滤器失败: %v", err)
	}

	srv := NewServer(cfg, filter)
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // 大文件下载不设上限
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("文件同步服务端启动")
	log.Printf("  监听地址: %s", cfg.Listen)
	log.Printf("  根目录  : %s", cfg.Root)
	log.Printf("  Token   : %s", maskToken(cfg.Token))

	idleConnClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint
		log.Printf("正在关闭服务...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
		close(idleConnClosed)
	}()

	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("服务异常退出: %v", err)
	}
	<-idleConnClosed
	log.Printf("已退出")
}

func resolveServerConfig(name string) string {
	if _, err := os.Stat(name); err == nil {
		return name
	}
	exec, err := os.Executable()
	if err != nil {
		return name
	}
	alt := filepath.Join(filepath.Dir(exec), name)
	if _, err := os.Stat(alt); err == nil {
		return alt
	}
	return name
}

func maskToken(t string) string {
	if t == "" {
		return "(未设置，不鉴权)"
	}
	if len(t) <= 4 {
		return "****"
	}
	return t[:2] + "****" + t[len(t)-2:]
}
