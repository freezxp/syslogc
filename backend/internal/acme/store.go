package acme

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/metadata"
)

// settingPrefix namespaces account keys and certificates among the settings,
// so they are plainly what they are and cannot collide with anything else.
const settingPrefix = "acme:"

// SettingsStore keeps certificates in the metadata database.
//
// They belong there rather than on disk: the container filesystem does not
// survive a redeploy, and every node of a deployment needs the same
// certificate and the same account key. Losing them means asking the
// authority again, and the authority counts how often you ask.
type SettingsStore struct {
	Store metadata.Store
}

func (s SettingsStore) Get(ctx context.Context, key string) ([]byte, error) {
	setting, err := s.Store.Setting(ctx, settingPrefix+key)
	if errors.Is(err, metadata.ErrNotFound) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, err
	}
	var e cacheEntry
	if err := json.Unmarshal(setting.Value, &e); err == nil && e.Data == "" {
		return nil, ErrCacheMiss
	}
	return setting.Value, nil
}

func (s SettingsStore) Put(ctx context.Context, key string, data []byte) error {
	return s.Store.SetSetting(ctx, &metadata.Setting{Key: settingPrefix + key, Value: json.RawMessage(data)})
}

func (s SettingsStore) Delete(ctx context.Context, key string) error {
	// A setting is removed by storing nothing in it: the store has no
	// delete, and an empty value reads back as a miss.
	return s.Store.SetSetting(ctx, &metadata.Setting{Key: settingPrefix + key, Value: json.RawMessage(`{"data":""}`)})
}

// IsACMESetting reports whether a settings key holds certificate material,
// so it can be kept out of anything that lists settings for a person to read.
func IsACMESetting(key string) bool { return strings.HasPrefix(key, settingPrefix) }
