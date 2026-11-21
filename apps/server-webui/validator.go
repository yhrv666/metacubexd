package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type validator func(context.Context, []byte) error

var errValidation = errors.New("Candidate did not pass the configured Mihomo validator")
var imageID = regexp.MustCompile("^sha256:[a-f0-9]{64}$")

func validatorClient(socket string) validator {
	client := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}
	return func(ctx context.Context, data []byte) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://validator/validate", bytes.NewReader(data))
		if err != nil {
			return errValidation
		}
		res, err := client.Do(req)
		if err != nil {
			return errors.New("Could not reach the fixed Mihomo validator")
		}
		defer res.Body.Close()
		if res.StatusCode != 204 {
			return errValidation
		}
		return nil
	}
}

func startValidator(listener net.Listener, validate validator) error {
	slot := make(chan struct{}, 1)
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, MaxHeaderBytes: 4096, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/validate" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		select {
		case slot <- struct{}{}:
			defer func() { <-slot }()
		default:
			w.WriteHeader(429)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, configLimit))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		if validate(ctx, data) != nil {
			w.WriteHeader(422)
			return
		}
		w.WriteHeader(204)
	})}
	return server.Serve(listener)
}

func fixedValidator(home, binary, docker, image, entrypoint string) (validator, error) {
	if !filepath.IsAbs(home) {
		return nil, errors.New("Validator home must be absolute")
	}
	if binary != "" && docker != "" || binary == "" && docker == "" || binary != "" && !filepath.IsAbs(binary) || docker != "" && (!filepath.IsAbs(docker) || !imageID.MatchString(image) || !strings.HasPrefix(entrypoint, "/")) {
		return nil, errors.New("Select an absolute kernel binary or absolute Docker binary with the running image ID")
	}
	return func(ctx context.Context, data []byte) error {
		work, err := os.MkdirTemp("", "mcxd-check-")
		if err != nil {
			return errValidation
		}
		defer os.RemoveAll(work)
		if cloneKernelHome(home, work) != nil || atomicWrite(filepath.Join(work, "mcxd-candidate.yaml"), data) != nil {
			return errValidation
		}
		if binary != "" {
			cmd := exec.CommandContext(ctx, binary, "-t", "-d", work, "-f", filepath.Join(work, "mcxd-candidate.yaml"))
			cmd.Dir = work
			if cmd.Run() != nil {
				return errValidation
			}
			return nil
		}
		name := "mcxd-check-" + revisionID()
		owner := strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid())
		cmd := exec.CommandContext(ctx, docker, "run", "--rm", "--pull=never", "--name", name, "--network=none", "--read-only", "--user", owner, "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=256m", "--log-driver=none", "--mount", "type=bind,source="+work+",target=/config", "--entrypoint", entrypoint, image, "-t", "-d", "/config", "-f", "/config/mcxd-candidate.yaml")
		if cmd.Run() != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = exec.CommandContext(cleanup, docker, "rm", "-f", name).Run()
			return errValidation
		}
		return nil
	}, nil
}

func cloneKernelHome(source, target string) error {
	var total int64
	return filepath.WalkDir(source, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, err := filepath.Rel(source, path)
		if err != nil || name == "." {
			return err
		}
		if strings.HasPrefix(item.Name(), ".") && item.IsDir() {
			return fs.SkipDir
		}
		if strings.HasPrefix(item.Name(), ".") || strings.HasSuffix(item.Name(), ".log") || strings.HasSuffix(item.Name(), ".db") {
			return nil
		}
		dest := filepath.Join(target, name)
		if item.IsDir() {
			return os.Mkdir(dest, 0700)
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		if total > 256<<20 {
			return errValidation
		}
		return copyKernelFile(path, dest, info.Size())
	})
}

func copyKernelFile(source, target string, size int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	written, err := io.Copy(output, io.LimitReader(input, size+1))
	if err != nil || written != size {
		return errValidation
	}
	return nil
}
