package acme

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// memStore is a Store that keeps material in memory.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, ErrCacheMiss
	}
	return v, nil
}

func (m *memStore) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = data
	return nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func TestRefusesConfigurationThatWouldWasteAnAttempt(t *testing.T) {
	store := newMemStore()
	base := Config{Domains: []string{"syslog.example.com"}, AgreedToTerms: true}

	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"no domains", Config{AgreedToTerms: true}, "at least one domain"},
		{"terms not accepted", Config{Domains: []string{"a.example.com"}}, "subscriber agreement"},
		{"a wildcard", Config{Domains: []string{"*.example.com"}, AgreedToTerms: true}, "wildcards need the DNS-01"},
		{"a bare name", Config{Domains: []string{"syslog"}, AgreedToTerms: true}, "fully qualified"},
		{"localhost", Config{Domains: []string{"localhost"}, AgreedToTerms: true}, "not a public name"},
		{"a URL", Config{Domains: []string{"https://a.example.com"}, AgreedToTerms: true}, "bare hostname"},
	} {
		if _, err := New(tc.cfg, store, discard()); err == nil {
			t.Errorf("%s: accepted", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}

	// Without somewhere to keep the certificate, every restart would ask for
	// a new one and the week's allowance would go in a day.
	if _, err := New(base, nil, discard()); err == nil || !strings.Contains(err.Error(), "a store is required") {
		t.Errorf("no store: err = %v", err)
	}
}

func TestStagingIsTheDefault(t *testing.T) {
	// The authority that cannot lock you out is the one to start with.
	m, err := New(Config{Domains: []string{"a.example.com"}, AgreedToTerms: true}, newMemStore(), discard())
	if err != nil {
		t.Fatal(err)
	}
	if !m.Staging() {
		t.Error("an unset directory did not default to staging")
	}
	if m.Status().Staging != true {
		t.Error("status does not report that certificates are from staging, which nothing trusts")
	}

	live, err := New(Config{Domains: []string{"a.example.com"}, AgreedToTerms: true,
		Directory: DirectoryProduction}, newMemStore(), discard())
	if err != nil {
		t.Fatal(err)
	}
	if live.Staging() {
		t.Error("the production directory was reported as staging")
	}
}

func TestPreflightRefusesRatherThanSpendAFailure(t *testing.T) {
	// A name that does not resolve would fail validation, and only five
	// failures an hour are allowed, so it is not worth asking.
	m, err := New(Config{Domains: []string{"nothing-resolves-here.invalid"}, AgreedToTerms: true},
		newMemStore(), discard())
	if err != nil {
		t.Fatal(err)
	}
	err = m.Prime(context.Background())
	if err == nil {
		t.Fatal("a name that does not resolve was asked for anyway")
	}
	for _, want := range []string{"five failures an hour", "does not resolve"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	// And the reason is kept, so the system page can say why there is no
	// certificate.
	if st := m.Status(); st.Error == "" {
		t.Error("the failure was not recorded in the status")
	}
}

func TestTheChallengePortServesNothingElse(t *testing.T) {
	m, err := New(Config{Domains: []string{"a.example.com"}, AgreedToTerms: true}, newMemStore(), discard())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(m.HTTPHandler())
	defer srv.Close()

	// This port is open to the internet for one purpose. Anything else is
	// refused rather than redirected: a redirect would invite senders to it.
	for _, path := range []string{"/", "/api/v1/logs/search", "/metrics", "/.well-known/other"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want it refused", path, resp.StatusCode)
		}
		if !strings.Contains(string(body), "challenges only") {
			t.Errorf("%s: body = %q", path, body)
		}
	}

	// A challenge for a token we do not hold is answered by autocert, not
	// by the fallback, so it says something else.
	resp, err := http.Get(srv.URL + "/.well-known/acme-challenge/unknown-token")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "challenges only") {
		t.Error("a challenge request was handled by the fallback instead of the challenge handler")
	}
}

func TestMaterialSurvivesARestart(t *testing.T) {
	store := newMemStore()
	c := cache{store}
	ctx := context.Background()

	if _, err := c.Get(ctx, "missing"); !errors.Is(err, ErrCacheMiss) {
		t.Errorf("err = %v, want a cache miss", err)
	}
	// Account keys and certificates are binary, and must come back byte for
	// byte or the account is lost and the authority is asked again.
	material := []byte{0x00, 0x01, 0xff, 0xfe, '\n', '"'}
	if err := c.Put(ctx, "account+key", material); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "account+key")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(material) {
		t.Errorf("got %v, want %v", got, material)
	}
	if err := c.Delete(ctx, "account+key"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "account+key"); !errors.Is(err, ErrCacheMiss) {
		t.Errorf("after delete: err = %v, want a cache miss", err)
	}
}
