package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testInstance struct {
	config          string
	running         []byte
	failures        int
	validationError bool
	arrived, resume chan struct{}
}

func (f *testInstance) Validate(_ context.Context, _ []byte) error {
	if f.arrived != nil {
		close(f.arrived)
		<-f.resume
	}
	if f.validationError {
		return errValidation
	}
	return nil
}

func (f *testInstance) Reload(_ context.Context) error {
	if f.failures > 0 {
		f.failures--
		return errors.New("Synthetic reload failure")
	}
	var err error
	f.running, err = os.ReadFile(f.config)
	return err
}

func (f *testInstance) Verify(_ context.Context, expected []byte) error {
	if string(f.running) != string(expected) {
		return errors.New("Synthetic runtime mismatch")
	}
	return nil
}

func setupManager(t *testing.T) (*manager, *testInstance, revision) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte(testBase), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := &testInstance{config: path, running: []byte(testBase)}
	m := &manager{dir: filepath.Join(root, "private"), config: path, instance: fixture}
	if err := m.open(); err != nil {
		t.Fatal(err)
	}
	_, r, err := m.current()
	if err != nil {
		t.Fatal(err)
	}
	return m, fixture, r
}

func sampleRevision(_ stateIndex, prior revision) (revision, error) {
	candidate := append([]byte("# candidate\n"), prior.Config...)
	return revision{Base: prior.Base, Config: candidate}, nil
}

func TestPublishPersistsAndRejectsStaleOrExternalChanges(t *testing.T) {
	m, fixture, r := setupManager(t)
	if err := m.apply(t.Context(), r.ID, "publish", sampleRevision); err != nil {
		t.Fatal(err)
	}
	if err := m.open(); err != nil {
		t.Fatal(err)
	}
	state, current, err := m.current()
	if err != nil || current.ID == r.ID || len(state.History) != 2 || string(current.Config) != string(fixture.running) {
		t.Fatal("published state missing after restart")
	}
	if err := m.apply(t.Context(), r.ID, "publish", sampleRevision); !errors.Is(err, errConflict) {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.config, []byte("external: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.apply(t.Context(), current.ID, "publish", sampleRevision); !errors.Is(err, errDrift) {
		t.Fatal(err)
	}
}

func TestConcurrentAndInvalidPublish(t *testing.T) {
	m, fixture, r := setupManager(t)
	fixture.arrived, fixture.resume = make(chan struct{}), make(chan struct{})
	fixture.validationError = true
	result := make(chan error, 1)
	go func() { result <- m.apply(context.Background(), r.ID, "publish", sampleRevision) }()
	<-fixture.arrived
	err := m.apply(t.Context(), r.ID, "publish", sampleRevision)
	close(fixture.resume)
	if !errors.Is(err, errBusy) {
		t.Fatalf("parallel writer: %v", err)
	}
	if err := <-result; !errors.Is(err, errValidation) || !m.diskMatches(r.Config) {
		t.Fatalf("validation changed startup file: %v", err)
	}
}

func TestReloadFailureAndFailedRestore(t *testing.T) {
	m, fixture, r := setupManager(t)
	fixture.failures = 1
	if err := m.apply(t.Context(), r.ID, "publish", sampleRevision); !errors.Is(err, errRollback) || !m.diskMatches(r.Config) || string(fixture.running) != testBase {
		t.Fatalf("rollback: %v", err)
	}
	fixture.failures = 2
	if err := m.apply(t.Context(), r.ID, "publish", sampleRevision); !errors.Is(err, errFrozen) {
		t.Fatalf("freeze: %v", err)
	}
	if err := m.open(); err != nil {
		t.Fatal(err)
	}
	if err := m.apply(t.Context(), r.ID, "publish", sampleRevision); !errors.Is(err, errFrozen) {
		t.Fatal("restart removed freeze")
	}
}

func TestInterruptedPublishRecoversOnOpen(t *testing.T) {
	m, fixture, r := setupManager(t)
	state, _, _ := m.current()
	candidate, _ := sampleRevision(state, r)
	candidate.ID = revisionID()
	state.Pending = candidate.ID
	for _, step := range []func() error{
		func() error { return m.saveRevision(candidate) },
		func() error { return m.saveIndex(state) },
		func() error { return m.activate(t.Context(), candidate.Config) },
		m.open,
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	state, current, err := m.current()
	if err != nil || current.ID != r.ID || state.Pending != "" || !m.diskMatches(r.Config) || string(fixture.running) != testBase {
		t.Fatal("failed to recover journaled publish")
	}
}
