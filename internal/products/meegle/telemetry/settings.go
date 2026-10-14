// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const settingsFilename = "settings.json"

type LocalSettings struct {
	Enabled    bool   `json:"enabled"`
	Generation uint64 `json:"generation,omitempty"`
}

type SettingsStore struct {
	path              string
	lockDir           string
	disclosurePath    string
	disclosureLockDir string
}

func NewSettingsStore(root string) *SettingsStore {
	return &SettingsStore{
		path: filepath.Join(root, settingsFilename), lockDir: filepath.Join(root, ".settings-lock"),
		disclosurePath: filepath.Join(root, "disclosure.json"), disclosureLockDir: filepath.Join(root, ".disclosure-lock"),
	}
}

func (s *SettingsStore) Load() (LocalSettings, error) {
	settings := LocalSettings{Enabled: true}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if err != nil {
		return LocalSettings{}, err
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return LocalSettings{}, err
	}
	return settings, nil
}

func (s *SettingsStore) SetEnabled(enabled bool) error {
	return s.withLock(s.lockDir, func() error {
		settings, err := s.Load()
		if err != nil {
			return err
		}
		settings.Enabled = enabled
		settings.Generation++
		return s.save(settings)
	})
}

func (s *SettingsStore) DisclosureDue(version int) (bool, error) {
	shownVersion, err := s.loadDisclosureVersion()
	if err != nil {
		return false, err
	}
	return version > shownVersion, nil
}

func (s *SettingsStore) MarkDisclosureShown(version int) error {
	return s.withLock(s.disclosureLockDir, func() error {
		shownVersion, err := s.loadDisclosureVersion()
		if err != nil {
			return err
		}
		if version <= shownVersion {
			return nil
		}
		data, err := json.Marshal(version)
		if err != nil {
			return err
		}
		return atomicWriteFileBuffered(s.disclosurePath, data, 0600)
	})
}

func (s *SettingsStore) loadDisclosureVersion() (int, error) {
	data, err := os.ReadFile(s.disclosurePath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var version int
	if err := json.Unmarshal(data, &version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *SettingsStore) withLock(lockDir string, operation func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	release, err := NewProcessLock(lockDir).Acquire(ctx)
	if err != nil {
		return errors.New("telemetry settings are busy")
	}
	defer release()
	return operation()
}

func (s *SettingsStore) save(settings LocalSettings) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return atomicWriteFile(s.path, data, 0600)
}

func EffectiveEnabled(local LocalSettings, remote Policy) bool {
	return local.Enabled && remote.Enabled
}
