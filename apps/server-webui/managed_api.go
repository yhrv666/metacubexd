package main

import (
	"errors"
	"io"
	"net/http"
	"os"
)

const configLimit = 8 << 20

func readConfig(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Cannot read startup configuration")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("Startup configuration must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, configLimit+1))
	if err != nil || len(data) > configLimit {
		return nil, errors.New("Startup configuration exceeds read limit")
	}
	return data, nil
}

func (p *panel) previewManaged(w http.ResponseWriter, r *http.Request) {
	if p.baseConfig == "" {
		problem(w, 503, "Managed configuration is unavailable in monitor-only mode")
		return
	}
	var managed managedConfig
	if !readJSON(w, r, 512<<10, &managed) {
		return
	}
	base, err := readConfig(p.baseConfig)
	if err != nil {
		problem(w, 503, err.Error())
		return
	}
	_, managed, err = makeManaged(base, managed, managedConfig{})
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	respond(w, 200, map[string]any{"chains": managed.preview(), "rules": managed.Rules})
}
