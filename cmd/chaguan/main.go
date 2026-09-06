// chaguan — Go + SQLite 单二进制论坛。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	// 时区数据编进二进制:签到按服务器本地日期算,容器里(distroless/scratch)
	// 没有 /usr/share/zoneinfo 也能正确解析 TZ=Asia/Shanghai
	_ "time/tzdata"

	"chaguan/internal/db"
	"chaguan/internal/handlers"
	"chaguan/web"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDur 读时长环境变量("2s"、"24h"),非法值回落默认。
func envDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// healthcheck 请求本机 /healthz,正常返回 0(distroless 里没有 curl,探活用二进制自己)。
func healthcheck(port string) int {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: 状态码", resp.StatusCode)
		return 1
	}
	return 0
}

// applyTZ 按 TZ 装载时区并覆盖 time.Local(全局副作用)。
func applyTZ() {
	name := os.Getenv("TZ")
	if name == "" {
		return
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("时区 %q 加载失败,继续用 %s: %v", name, time.Local, err)
		return
	}
	time.Local = loc
}

func main() {
	probe := flag.Bool("healthcheck", false, "请求本机 /healthz 后退出(容器探活用)")
	flag.Parse()

	port := envOr("PORT", "8080")
	if *probe {
		os.Exit(healthcheck(port))
	}
	applyTZ()
	dbPath := envOr("CHAGUAN_DB", "data/chaguan.db")
	uploadsDir := envOr("CHAGUAN_UPLOADS", "uploads")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		log.Fatalf("创建上传目录失败: %v", err)
	}

	store, err := db.Open("file:" + dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	if err := store.Migrate(); err != nil {
		log.Fatalf("迁移失败: %v", err)
	}

	rend, err := web.NewRenderer()
	if err != nil {
		log.Fatalf("解析模板失败: %v", err)
	}

	srv := &http.Server{
		Addr: ":" + port,
		Handler: handlers.Routes(store, rend, uploadsDir, handlers.Options{
			RedpackTTL: envDur("CHAGUAN_RP_TTL", 24*time.Hour),
			SweepEvery: envDur("CHAGUAN_SWEEP", time.Minute),
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("chaguan 已启动: http://localhost:%s (db: %s, 时区: %s)", port, dbPath, time.Local)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("监听失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("关闭时出错: %v", err)
	}
}
