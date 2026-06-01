package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if err := cfg.ResolveLocal(); err != nil {
		log.Fatalf("解析本地路径失败: %v", err)
	}
	// 确保本地根目录存在
	if err := os.MkdirAll(cfg.Local.Root, 0755); err != nil {
		log.Fatalf("创建本地根目录失败: %v", err)
	}

	remote := NewRemoteClient(cfg.Remote.URL, cfg.Remote.Token)
	meta, err := NewMetaStore(cfg.Local.MetaFile)
	if err != nil {
		log.Fatalf("加载元信息失败: %v", err)
	}
	syncer := NewSyncer(cfg, remote, meta)

	// 先做一次健康检查
	if err := ping(remote); err != nil {
		log.Printf("警告：连接远程服务端失败: %v (启动后仍可继续使用)", err)
	}

	srv := NewLocalServer(cfg, remote, syncer)
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	log.Printf("文件同步客户端启动")
	log.Printf("  监听地址 : %s", cfg.Listen)
	log.Printf("  远程地址 : %s", cfg.Remote.URL)
	log.Printf("  本地目录 : %s", cfg.Local.Root)
	log.Printf("  元信息   : %s", cfg.Local.MetaFile)
	log.Printf("打开浏览器访问: http://localhost%s", cfg.Listen)

	idleConnClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		<-sigint
		log.Printf("正在关闭...")
		_ = meta.Save()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func ping(c *RemoteClient) error {
	req, _ := http.NewRequest("GET", c.base+"/health", nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &pingError{code: resp.StatusCode}
	}
	return nil
}

type pingError struct{ code int }

func (e *pingError) Error() string { return "health status " + itoa(e.code) }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	buf := [20]byte{}
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
