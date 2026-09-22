package servicetrends

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/freezxp/syslogc/backend/internal/metadata"
	"github.com/freezxp/syslogc/backend/internal/metricstore"
)

// SettingStore is the part of the metadata store this package needs.
type SettingStore interface {
	Setting(ctx context.Context, key string) (*metadata.Setting, error)
	SetSetting(ctx context.Context, s *metadata.Setting) error
}

// LoadCatalog reads the stored catalog, falling back to the built-in one
// when nothing has been saved yet. A stored catalog that no longer validates
// is returned with its error so the caller can report it rather than
// silently counting the wrong thing.
func LoadCatalog(ctx context.Context, store SettingStore) (Catalog, error) {
	if store == nil {
		return DefaultCatalog(), nil
	}
	setting, err := store.Setting(ctx, metadata.SettingServiceCatalog)
	if errors.Is(err, metadata.ErrNotFound) {
		return DefaultCatalog(), nil
	}
	if err != nil {
		return Catalog{}, err
	}
	var c Catalog
	if err := json.Unmarshal(setting.Value, &c); err != nil {
		return Catalog{}, fmt.Errorf("the stored service catalog is not readable: %w", err)
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("the stored service catalog is invalid: %w", err)
	}
	return c, nil
}

// SaveCatalog stores a validated catalog, recording who changed it.
func SaveCatalog(ctx context.Context, store SettingStore, c Catalog, by *uuid.UUID) error {
	if err := c.Validate(); err != nil {
		return err
	}
	value, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return store.SetSetting(ctx, &metadata.Setting{
		Key: metadata.SettingServiceCatalog, Value: value, UpdatedBy: by})
}

// LoadState reads how far each resolution has been recorded.
func LoadState(ctx context.Context, store SettingStore) (State, error) {
	setting, err := store.Setting(ctx, metadata.SettingServiceTrendState)
	if errors.Is(err, metadata.ErrNotFound) {
		return State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(setting.Value, &raw); err != nil {
		// A state that cannot be read is not worth failing over: recording
		// starts again from the backfill window, and re-recorded samples
		// replace themselves.
		return State{}, nil
	}
	out := State{}
	for k, v := range raw {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			continue
		}
		out[k] = t.UTC()
	}
	return out, nil
}

// SaveState stores how far each resolution has been recorded.
func SaveState(ctx context.Context, store SettingStore, s State) error {
	raw := make(map[string]string, len(s))
	for k, v := range s {
		raw[k] = v.UTC().Format(time.RFC3339)
	}
	value, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return store.SetSetting(ctx, &metadata.Setting{Key: metadata.SettingServiceTrendState, Value: value})
}

// Reset clears the recorded state so the next run records the backfill
// window again. Samples replace themselves, so this is safe to call.
func Reset(ctx context.Context, store SettingStore) error {
	return store.SetSetting(ctx, &metadata.Setting{
		Key: metadata.SettingServiceTrendState, Value: json.RawMessage("{}")})
}

// Ensure the writer contract matches the metrics store client.
var _ Writer = (*metricstore.Client)(nil)
