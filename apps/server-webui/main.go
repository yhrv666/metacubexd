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
	"path/filepath"
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
	stateDir := flag.String("state-dir", "", "private managed state directory; enables writes")
	probeFile := flag.String("probe-file", "", "operator-supplied runtime probe specification")
	validatorSocket := flag.String("validator-socket", "", "fixed Unix socket for the validator")
	validatorService := flag.Bool("validator-service", false, "serve the isolated validator")
	kernelHome := flag.String("kernel-home", "", "validator source directory for kernel data")
	kernelBinary := flag.String("kernel-binary", "", "absolute path to the fixed host Mihomo binary")
	dockerBinary := flag.String("docker-binary", "", "absolute path to the Docker CLI for the validator")
	dockerImage := flag.String("docker-image", "", "immutable image ID of the target instance")
	flag.Parse()
	if *validatorService {
		validate, err := fixedValidator(*kernelHome, *kernelBinary, *dockerBinary, *dockerImage)
		if err != nil || !filepath.IsAbs(*validatorSocket) {
			log.Fatal("invalid fixed validator settings")
		}
		listener, err := net.Listen("unix", *validatorSocket)
		if err != nil {
			log.Fatal("could not open the validator socket")
		}
		defer listener.Close()
		if os.Chmod(*validatorSocket, 0660) != nil {
			log.Fatal("could not restrict the validator socket")
		}
		log.Fatal(startValidator(listener, validate))
	}

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
	if *stateDir != "" {
		var probes probeSpec
		if !filepath.IsAbs(*probeFile) || readPrivateJSON(*probeFile, 16<<10, &probes) != nil || !probes.valid() || !filepath.IsAbs(*validatorSocket) {
			log.Fatal("write mode requires fixed validation and runtime probe settings")
		}
		app.manager = &manager{dir: *stateDir, config: *baseConfig, instance: &fixedInstance{kernel: app.kernel, validate: validatorClient(*validatorSocket), probes: probes}}
		if err := app.manager.open(); err != nil {
			log.Fatal("could not initialize managed configuration state: ", err)
		}
	}
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
