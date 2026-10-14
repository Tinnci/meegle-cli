// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDisclosureDoesNotWaitForConsentWrite(t *testing.T) {
	root := t.TempDir()
	store := NewSettingsStore(root)
	release, err := NewProcessLock(filepath.Join(root, ".settings-lock")).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := store.MarkDisclosureShown(1); err != nil {
		t.Fatalf("disclosure blocked by consent write: %v", err)
	}
	if due, err := store.DisclosureDue(1); err != nil || due {
		t.Fatalf("DisclosureDue(1) = (%v, %v), want (false, nil)", due, err)
	}
}

func TestHardDisableDoesNotWaitForDisclosureWrite(t *testing.T) {
	root := t.TempDir()
	store := NewSettingsStore(root)
	release, err := NewProcessLock(filepath.Join(root, ".disclosure-lock")).Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := store.SetEnabled(false); err != nil {
		t.Fatalf("disable blocked by disclosure write: %v", err)
	}
	settings, err := NewSettingsStore(root).Load()
	if err != nil || settings.Enabled || settings.Generation != 1 {
		t.Fatalf("reloaded consent = (%+v, %v), want disabled generation 1", settings, err)
	}
}

func TestLostDisclosureMarkerCannotReenableTelemetry(t *testing.T) {
	root := t.TempDir()
	store := NewSettingsStore(root)
	if err := store.MarkDisclosureShown(2); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "disclosure.json")); err != nil {
		t.Fatal(err)
	}
	reloaded := NewSettingsStore(root)
	if due, err := reloaded.DisclosureDue(2); err != nil || !due {
		t.Fatalf("DisclosureDue(2) after marker loss = (%v, %v), want (true, nil)", due, err)
	}
	settings, err := reloaded.Load()
	if err != nil || settings.Enabled || settings.Generation != 1 {
		t.Fatalf("reloaded consent = (%+v, %v), want disabled generation 1", settings, err)
	}
}

func TestDisclosureFailureDoesNotBlockHardDisable(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "unreadable"}[directory], func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "disclosure.json")
			var err error
			if directory {
				err = os.Mkdir(marker, 0700)
			} else {
				err = os.WriteFile(marker, []byte("invalid"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			store := NewSettingsStore(root)
			if _, err := store.DisclosureDue(1); err == nil {
				t.Fatal("unreadable disclosure must not authorize collection")
			}
			if err := store.MarkDisclosureShown(1); err == nil {
				t.Fatal("unreadable disclosure must not be acknowledged")
			}
			if err := store.SetEnabled(false); err != nil {
				t.Fatal(err)
			}
			settings, err := NewSettingsStore(root).Load()
			if err != nil || settings.Enabled || settings.Generation != 1 {
				t.Fatalf("reloaded consent = (%+v, %v), want disabled generation 1", settings, err)
			}
		})
	}
}

func TestLocalSettingsDefaultEnabledAndDisableIsPermanent(t *testing.T) {
	store := NewSettingsStore(t.TempDir())

	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled {
		t.Fatal("telemetry must default to enabled")
	}

	if err := store.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		settings, err = store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if settings.Enabled {
			t.Fatal("local disable must survive reload and cannot expire")
		}
	}

	remote := DefaultPolicy()
	remote.Enabled = true
	if EffectiveEnabled(settings, remote) {
		t.Fatal("remote policy must not re-enable a local hard-disable")
	}

	if err := store.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	settings, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	remote.Enabled = false
	if EffectiveEnabled(settings, remote) {
		t.Fatal("remote policy may disable telemetry after the user enables it")
	}
}

func TestDisclosureUpdateCannotReenableConcurrentHardDisable(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	for iteration := 0; iteration < 50; iteration++ {
		if err := store.SetEnabled(true); err != nil {
			t.Fatal(err)
		}
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			if err := store.MarkDisclosureShown(iteration + 1); err != nil {
				t.Errorf("MarkDisclosureShown: %v", err)
			}
		}()
		go func() {
			defer group.Done()
			if err := store.SetEnabled(false); err != nil {
				t.Errorf("SetEnabled(false): %v", err)
			}
		}()
		group.Wait()
		settings, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if settings.Enabled {
			t.Fatalf("iteration %d: disclosure write re-enabled telemetry", iteration)
		}
	}
}

func TestDisclosureVersionPersistsIndependently(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	if due, err := store.DisclosureDue(1); err != nil || !due {
		t.Fatalf("DisclosureDue(1) = (%v, %v), want (true, nil)", due, err)
	}
	if err := store.MarkDisclosureShown(1); err != nil {
		t.Fatal(err)
	}
	if due, err := store.DisclosureDue(1); err != nil || due {
		t.Fatalf("DisclosureDue(1) after mark = (%v, %v), want (false, nil)", due, err)
	}
	if due, err := store.DisclosureDue(2); err != nil || !due {
		t.Fatalf("DisclosureDue(2) = (%v, %v), want (true, nil)", due, err)
	}
	if err := store.MarkDisclosureShown(2); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDisclosureShown(1); err != nil {
		t.Fatal(err)
	}
	if due, err := NewSettingsStore(filepath.Dir(store.path)).DisclosureDue(2); err != nil || due {
		t.Fatalf("DisclosureDue(2) after older mark = (%v, %v), want (false, nil)", due, err)
	}
}
