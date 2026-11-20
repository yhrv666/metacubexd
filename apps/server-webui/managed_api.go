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
	case r.Method == http.MethodPost && slices.Contains([]string{
		"subscription-import", "subscription-save", "subscription-refresh", "subscription-remove",
		"subscription-node-add", "subscription-node-remove",
	}, route):
		p.updateSubscription(w, r, route)
	default:
		problem(w, 404, "Capability unavailable")
	}
}

func (p *panel) managedState(w http.ResponseWriter) {
	if p.manager == nil {
		var nodes []proxyChoice
		if p.baseConfig != "" {
			base, err := readConfig(p.baseConfig)
			if err == nil {
				nodes, err = proxyChoices(base)
			}
			if err != nil {
				managedProblem(w, err)
				return
			}
		}
		respond(w, 200, map[string]any{"writable": false, "canPreview": p.baseConfig != "", "nodes": nodes})
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
		audit := m.recentAudit()
		if len(audit) > 100 {
			audit = audit[len(audit)-100:]
		}
		nodes, err := proxyChoices(r.Base)
		if err != nil {
			return err
		}
		respond(w, 200, map[string]any{"writable": diagnostic == "", "canPreview": true, "version": r.ID, "chains": r.Managed.preview(), "rules": r.Managed.Rules, "history": history, "diagnostic": diagnostic, "nodes": nodes, "subscriptions": summarizeSubscriptions(r.Subscriptions), "audit": audit})
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
			base, err = subscriptionLayer(prior.Base, prior.Subscriptions)
			previous = prior.Managed
			return err
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
		time.AfterFunc(time.Until(draft.expires), func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.drafts[key].id == draft.id {
				delete(p.drafts, key)
			}
		})
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
		return composeRevision(prior, draft.managed)
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

func (p *panel) updateSubscription(w http.ResponseWriter, r *http.Request, action string) {
	if p.manager == nil {
		problem(w, 503, "Managed writes are disabled")
		return
	}
	var request struct{ Version, ID, URL, Import, Name string }
	if !readJSON(w, r, 16<<10, &request) {
		return
	}
	if (request.ID != "" || action != "subscription-import") && !identifier.MatchString(request.ID) {
		problem(w, 422, "Invalid subscription ID")
		return
	}
	err := p.manager.apply(r.Context(), request.Version, action, func(_ stateIndex, prior revision) (revision, error) {
		index := slices.IndexFunc(prior.Subscriptions, func(s subscription) bool { return s.ID == request.ID })
		if action == "subscription-import" || action == "subscription-save" {
			if _, err := subscriptionURL(request.URL); err != nil {
				return revision{}, err
			}
			if request.ID == "" {
				index = slices.IndexFunc(prior.Subscriptions, func(s subscription) bool { return s.URL == request.URL })
				request.ID = nextNumberedID("sub-", func(id string) bool {
					return slices.ContainsFunc(prior.Subscriptions, func(s subscription) bool { return s.ID == id })
				})
			}
			if index < 0 {
				if len(prior.Subscriptions) >= 8 {
					return revision{}, errors.New("Subscription limit exceeded")
				}
				index = len(prior.Subscriptions)
				prior.Subscriptions = append(prior.Subscriptions, subscription{ID: request.ID})
			}
			prior.Subscriptions[index].URL = request.URL
		}
		if index < 0 {
			return revision{}, errors.New("Subscription does not exist")
		}
		item := &prior.Subscriptions[index]
		switch action {
		case "subscription-import", "subscription-refresh":
			data, err := p.fetchSubscription(r.Context(), item.URL)
			if err != nil {
				return revision{}, err
			}
			if _, err := subscriptionNodes(data); err != nil {
				return revision{}, err
			}
			item.Cached = data
			item.Refreshed = time.Now().UTC()
		case "subscription-node-add", "subscription-node-remove":
			if err := changeSubscriptionNode(item, request.Import, request.Name, action == "subscription-node-remove"); err != nil {
				return revision{}, err
			}
		case "subscription-remove":
			prior.Subscriptions = slices.Delete(prior.Subscriptions, index, index+1)
		}
		n := 0
		for _, item := range prior.Subscriptions {
			n += len(item.Cached)
		}
		if n > 8<<20 {
			return revision{}, errors.New("Combined subscription cache is too large")
		}
		return composeRevision(prior, prior.Managed)
	})
	if err != nil {
		managedProblem(w, err)
		return
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
