package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed all:web
var assets embed.FS

func credential(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || (info.Mode().Perm()&0077 != 0 && os.PathSeparator == '/') {
		return "", errors.New("credential file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	value := strings.TrimRight(string(data), "\r\n")
	if err != nil || value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("credential file is invalid")
	}
	return value, nil
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18443", "loopback address to serve")
	controller := flag.String("controller", "http://127.0.0.1:9090", "fixed Mihomo controller URL")
	adminFile := flag.String("admin-file", "", "private file containing the administrator password")
	controllerFile := flag.String("controller-secret-file", "", "private file containing the controller secret")
	baseConfig := flag.String("base-config", "", "fixed Mihomo startup config for managed previews")
	flag.Parse()

	if host, _, err := net.SplitHostPort(*listen); err != nil || !loopback(host) {
		log.Fatal("listen address must be loopback")
	}
	origin, err := url.Parse(*controller)
	if err != nil || origin.Hostname() == "" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		log.Fatal("controller must be a fixed HTTP origin")
	}
	password, err := credential(*adminFile)
	if err != nil || len(password) < 16 {
		log.Fatal("admin password file must contain at least 16 characters")
	}
	secret, err := credential(*controllerFile)
	if err != nil {
		log.Fatal("controller secret file is invalid")
	}
	web, err := fs.Sub(assets, "web")
	if err != nil {
		log.Fatal("embedded web assets are unavailable")
	}
	if _, err := fs.Stat(web, "index.html"); err != nil {
		log.Fatal("build the server UI before building the binary")
	}
	app := newPanel(sha256.Sum256([]byte(password)), newKernel(origin, secret), http.FileServerFS(web))
	app.baseConfig = *baseConfig
	server := &http.Server{Addr: *listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	log.Printf("server panel listening on %s", *listen)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
