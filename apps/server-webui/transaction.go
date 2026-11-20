package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/gofrs/flock"
)

var (
	errConflict = errors.New("Configuration version changed. Reload the page and review the current version")
	errDrift    = errors.New("Startup file changed outside the panel. Restore consistency before writing")
	errBusy     = errors.New("A configuration operation is already in progress")
	errFrozen   = errors.New("Writes frozen: rollback could not restore and verify disk and Mihomo runtime")
	errState    = errors.New("Managed state is unavailable or damaged")
	errRollback = errors.New("Candidate failed to apply or verify. Previous configuration was restored and verified")
)

type revision struct {
	ID            string
	Created       time.Time
	Base          []byte
	Config        []byte
	Managed       managedConfig
	Subscriptions []subscription
}

type stateIndex struct {
	History []string
	Pending string
	Frozen  bool
}

type instance interface {
	Validate(context.Context, []byte) error
	Reload(context.Context) error
	Verify(context.Context, []byte) error
}

type manager struct {
	dir      string
	config   string
	instance instance
}

func revisionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func digest(data []byte) [32]byte { return sha256.Sum256(data) }

func (m *manager) use(run func() error) error {
	lock := flock.New(filepath.Join(m.dir, "write.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return errState
	}
	if !ok {
		return errBusy
	}
	defer lock.Close()
	return run()
}

func (m *manager) open() error {
	if !filepath.IsAbs(m.dir) || !filepath.IsAbs(m.config) {
		return errors.New("Managed state and startup file must use absolute paths")
	}
	info, err := os.Lstat(m.config)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Startup file must be a regular file in the mounted directory")
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return errState
	}
	info, err = os.Lstat(m.dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errState
	}
	return m.use(func() error {
		state, err := m.readIndex()
		if errors.Is(err, os.ErrNotExist) {
			data, err := readConfig(m.config)
			if err != nil {
				return err
			}
			initial := revision{ID: revisionID(), Created: time.Now().UTC(), Base: data, Config: data}
			if err := m.saveRevision(initial); err != nil {
				return err
			}
			return m.saveIndex(stateIndex{History: []string{initial.ID}})
		}
		if err != nil {
			return err
		}
		if state.Pending != "" && !state.Frozen {
			prior, err := m.readRevision(state.History[0])
			if err != nil {
				return err
			}
			_ = m.restore(state, prior)
		}
		return nil
	})
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func atomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mcxd-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(body); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func readPrivateJSON(path string, max int64, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(data)) > max || json.Unmarshal(data, target) != nil {
		return errState
	}
	return nil
}

func writePrivateJSON(path string, item any) error {
	data, err := json.Marshal(item)
	if err != nil || atomicWrite(path, data) != nil {
		return errState
	}
	return nil
}

func (m *manager) readIndex() (stateIndex, error) {
	var index stateIndex
	if err := readPrivateJSON(filepath.Join(m.dir, "index.json"), 4096, &index); err != nil {
		return index, err
	}
	if len(index.History) == 0 || len(index.History) > 10 {
		return index, errState
	}
	return index, nil
}

func (m *manager) saveIndex(index stateIndex) error {
	return writePrivateJSON(filepath.Join(m.dir, "index.json"), index)
}

func (m *manager) revisionPath(id string) string { return filepath.Join(m.dir, id+".revision.json") }

func (m *manager) saveRevision(r revision) error {
	return writePrivateJSON(m.revisionPath(r.ID), r)
}

func (m *manager) readRevision(id string) (revision, error) {
	var r revision
	if _, err := hex.DecodeString(id); err != nil || len(id) != 32 {
		return r, errState
	}
	if err := readPrivateJSON(m.revisionPath(id), 48<<20, &r); err != nil || r.ID != id || len(r.Config) > configLimit || len(r.Base) > configLimit {
		return r, errState
	}
	return r, nil
}

func (m *manager) current() (stateIndex, revision, error) {
	state, err := m.readIndex()
	if err != nil {
		return state, revision{}, errState
	}
	r, err := m.readRevision(state.History[0])
	return state, r, err
}

func (m *manager) diskMatches(data []byte) bool {
	info, err := os.Lstat(m.config)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	current, err := readConfig(m.config)
	return err == nil && digest(data) == digest(current)
}

func (m *manager) apply(ctx context.Context, version, action string, build func(stateIndex, revision) (revision, error)) error {
	return m.use(func() error {
		state, prior, err := m.current()
		if err != nil {
			return err
		}
		if state.Frozen || state.Pending != "" {
			return errFrozen
		}
		if prior.ID != version {
			return errConflict
		}
		if !m.diskMatches(prior.Config) {
			return errDrift
		}
		candidate, err := build(state, prior)
		if err != nil {
			return err
		}
		if len(candidate.Config) > configLimit || len(candidate.Base) > configLimit {
			return errors.New("Candidate configuration is too large")
		}
		if action == "subscription-save" && bytes.Equal(candidate.Config, prior.Config) {
			return m.commit(state, candidate, action)
		}
		if err := m.instance.Validate(ctx, candidate.Config); err != nil {
			m.audit(action, "validation-failed")
			return err
		}
		if !m.diskMatches(prior.Config) {
			return errDrift
		}
		candidate.ID, candidate.Created = revisionID(), time.Now().UTC()
		if err := m.saveRevision(candidate); err != nil {
			return err
		}
		state.Pending = candidate.ID
		if err := m.saveIndex(state); err != nil {
			return err
		}

		// Complete or undo a journaled write even if the browser disconnects.
		operation, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := m.activate(operation, candidate.Config); err != nil {
			return m.restore(state, prior)
		}
		if err := m.commit(state, candidate, action); err != nil {
			return m.restore(state, prior)
		}
		return nil
	})
}

func (m *manager) commit(state stateIndex, candidate revision, action string) error {
	if candidate.ID == "" {
		candidate.ID, candidate.Created = revisionID(), time.Now().UTC()
		if err := m.saveRevision(candidate); err != nil {
			return err
		}
	}
	committed := stateIndex{History: append([]string{candidate.ID}, state.History...)}
	if len(committed.History) > 10 {
		committed.History = committed.History[:10]
	}
	if err := m.saveIndex(committed); err != nil {
		return err
	}
	m.prune(committed.History)
	m.audit(action, "verified")
	return nil
}

func (m *manager) activate(ctx context.Context, expected []byte) error {
	if err := atomicWrite(m.config, expected); err != nil {
		return errState
	}
	if err := m.instance.Reload(ctx); err != nil {
		return err
	}
	if err := m.instance.Verify(ctx, expected); err != nil {
		return err
	}
	if !m.diskMatches(expected) {
		return errDrift
	}
	return nil
}

func (m *manager) restore(state stateIndex, prior revision) error {
	// Avoid overwriting an external edit discovered during crash recovery.
	pending, err := m.readRevision(state.Pending)
	if err != nil || (!m.diskMatches(prior.Config) && !m.diskMatches(pending.Config)) {
		state.Frozen = true
		_ = m.saveIndex(state)
		m.audit("restore", "frozen")
		return errFrozen
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := m.activate(ctx, prior.Config); err != nil {
		state.Frozen = true
		_ = m.saveIndex(state)
		m.audit("restore", "frozen")
		return errFrozen
	}
	state.Pending = ""
	if err := m.saveIndex(state); err != nil {
		return errFrozen
	}
	m.prune(state.History)
	m.audit("restore", "verified")
	return errRollback
}

func (m *manager) prune(keep []string) {
	entries, _ := os.ReadDir(m.dir)
	for _, entry := range entries {
		name := entry.Name()
		id := bytes.TrimSuffix([]byte(name), []byte(".revision.json"))
		if len(name) != len(id) && !slices.Contains(keep, string(id)) {
			_ = os.Remove(filepath.Join(m.dir, name))
		}
	}
}

type auditEvent struct {
	Time   time.Time
	Action string
	Result string
}

func (m *manager) audit(action, result string) {
	path := filepath.Join(m.dir, "audit.json")
	events := m.recentAudit()
	now := time.Now().UTC()
	events = append(events, auditEvent{now, action, result})
	if len(events) > 10000 {
		events = events[len(events)-10000:]
	}
	_ = writePrivateJSON(path, events)
}

func (m *manager) recentAudit() []auditEvent {
	var events []auditEvent
	_ = readPrivateJSON(filepath.Join(m.dir, "audit.json"), 4<<20, &events)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)
	return slices.DeleteFunc(events, func(e auditEvent) bool { return e.Time.Before(cutoff) })
}
