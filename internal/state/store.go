package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Snapshot struct {
	Builds      []BuildRecord      `json:"builds"`
	Pushes      []PushRecord       `json:"pushes"`
	Activations []ActivationRecord `json:"activations"`
}

type BuildRecord struct {
	ReleaseID string `json:"release_id"`
	Path      string `json:"path"`
	BuiltAt   string `json:"built_at"`
}

type PushRecord struct {
	ReleaseID  string `json:"release_id"`
	RemotePath string `json:"remote_path"`
	PushedAt   string `json:"pushed_at"`
}

type ActivationRecord struct {
	ReleaseID   string `json:"release_id"`
	ActivatedAt string `json:"activated_at"`
	Reason      string `json:"reason"`
}

type Store struct {
	file string
	mu   sync.Mutex
}

func New(file string) *Store {
	return &Store{file: file}
}

func (s *Store) Snapshot(_ context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) RecordBuild(_ context.Context, record BuildRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.load()
	if err != nil {
		return err
	}
	snapshot.Builds = append(snapshot.Builds, record)
	return s.save(snapshot)
}

func (s *Store) RecordPush(_ context.Context, record PushRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.load()
	if err != nil {
		return err
	}
	snapshot.Pushes = append(snapshot.Pushes, record)
	return s.save(snapshot)
}

func (s *Store) RecordActivation(_ context.Context, record ActivationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, err := s.load()
	if err != nil {
		return err
	}
	snapshot.Activations = append(snapshot.Activations, record)
	return s.save(snapshot)
}

func (s *Store) load() (Snapshot, error) {
	raw, err := os.ReadFile(s.file)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, nil
		}
		return Snapshot{}, err
	}

	var snapshot Snapshot
	if len(raw) == 0 {
		return Snapshot{}, nil
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) save(snapshot Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.file, raw, 0o644)
}

func LatestBuild(snapshot Snapshot) (BuildRecord, bool) {
	if len(snapshot.Builds) == 0 {
		return BuildRecord{}, false
	}
	return snapshot.Builds[len(snapshot.Builds)-1], true
}

func PreviousActivation(snapshot Snapshot) (ActivationRecord, bool) {
	if len(snapshot.Activations) < 2 {
		return ActivationRecord{}, false
	}
	return snapshot.Activations[len(snapshot.Activations)-2], true
}
