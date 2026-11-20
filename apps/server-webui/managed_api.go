package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"time"
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

type managedDraft struct {
	id, version string
	expires     time.Time
	managed     managedConfig
}

type revisionSummary struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
}

func (p *panel) manage(w http.ResponseWriter, r *http.Request, key, route string) {
	switch {
	case r.Method == http.MethodGet && route == "state":
		p.managedState(w)
	case r.Method == http.MethodPost && route == "preview":
		p.previewManaged(w, r, key)
	case r.Method == http.MethodPost && (route == "publish" || route == "rollback"):
		p.applyManaged(w, r, key, route)
	default:
		problem(w, 404, "Capability unavailable")
	}
}

func (p *panel) managedState(w http.ResponseWriter) {
	if p.manager == nil {
		respond(w, 200, map[string]any{"writable": false, "canPreview": p.baseConfig != ""})
		return
	}
	m := p.manager
	err := m.use(func() error {
		state, r, err := m.current()
		if err != nil {
			return err
		}
		var history []revisionSummary
		for _, id := range state.History[1:] {
			prior, err := m.readRevision(id)
			if err != nil {
				return err
			}
			history = append(history, revisionSummary{prior.ID, prior.Created})
		}
		diagnostic := ""
		if state.Frozen || state.Pending != "" {
			diagnostic = errFrozen.Error()
		} else if !m.diskMatches(r.Config) {
			diagnostic = errDrift.Error()
		}
		respond(w, 200, map[string]any{"writable": diagnostic == "", "canPreview": true, "version": r.ID, "chains": r.Managed.preview(), "rules": r.Managed.Rules, "history": history, "diagnostic": diagnostic})
		return nil
	})
	if err != nil {
		managedProblem(w, err)
	}
}

func (p *panel) previewManaged(w http.ResponseWriter, r *http.Request, key string) {
	if p.baseConfig == "" {
		problem(w, 503, "Managed configuration is unavailable in monitor-only mode")
		return
	}
	var request struct {
		managedConfig
		Version string `json:"version,omitempty"`
	}
	if !readJSON(w, r, 512<<10, &request) {
		return
	}
	var base []byte
	var previous managedConfig
	if p.manager == nil {
		var err error
		base, err = readConfig(p.baseConfig)
		if err != nil {
			managedProblem(w, err)
			return
		}
	} else {
		err := p.manager.use(func() error {
			_, prior, err := p.manager.current()
			if err != nil {
				return err
			}
			if prior.ID != request.Version {
				return errConflict
			}
			base, previous = prior.Base, prior.Managed
			return nil
		})
		if err != nil {
			managedProblem(w, err)
			return
		}
	}
	_, managed, err := makeManaged(base, request.managedConfig, previous)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	result := map[string]any{"chains": managed.preview(), "rules": managed.Rules}
	if p.manager != nil {
		draft := managedDraft{revisionID(), request.Version, time.Now().Add(5 * time.Minute), managed}
		p.mu.Lock()
		if session, ok := p.sessions[key]; ok && session.ctx.Err() == nil {
			p.drafts[key] = draft
		}
		p.mu.Unlock()
		result["draft"] = draft.id
	}
	respond(w, 200, result)
}

func (p *panel) applyManaged(w http.ResponseWriter, r *http.Request, key, action string) {
	if p.manager == nil {
		problem(w, 503, "Managed writes are disabled")
		return
	}
	var request struct{ Version, ID string }
	if !readJSON(w, r, 4096, &request) {
		return
	}
	var draft managedDraft
	if action == "publish" {
		p.mu.Lock()
		draft = p.drafts[key]
		p.mu.Unlock()
		if draft.id == "" || draft.id != request.ID || draft.version != request.Version || time.Now().After(draft.expires) {
			managedProblem(w, errConflict)
			return
		}
	}
	err := p.manager.apply(r.Context(), request.Version, action, func(state stateIndex, prior revision) (revision, error) {
		if action == "rollback" {
			if !slices.Contains(state.History[1:], request.ID) {
				return revision{}, errConflict
			}
			return p.manager.readRevision(request.ID)
		}
		candidate, normalized, err := makeManaged(prior.Base, draft.managed, prior.Managed)
		return revision{Base: prior.Base, Config: candidate, Managed: normalized}, err
	})
	if err != nil {
		managedProblem(w, err)
		return
	}
	if action == "publish" {
		p.mu.Lock()
		if p.drafts[key].id == draft.id {
			delete(p.drafts, key)
		}
		p.mu.Unlock()
	}
	w.WriteHeader(204)
}

func managedProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errConflict), errors.Is(err, errBusy), errors.Is(err, errDrift):
		problem(w, 409, err.Error())
	case errors.Is(err, errFrozen), errors.Is(err, errState):
		problem(w, 503, err.Error())
	default:
		problem(w, 422, err.Error())
	}
}
