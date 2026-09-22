// Package servicetrends records how many distinct clients queried each
// online service over time.
//
// A "service" is a named set of domains — TikTok is tiktok.com, tiktokcdn.com
// and a few more. Membership is decided when the rollup runs, not when a log
// is stored, so editing the catalog changes past windows too and history can
// be backfilled from logs that were stored long before the service existed in
// the catalog.
//
// Distinct counts do not add up: the number of distinct clients in an hour is
// not the sum of the distinct clients in its twelve five-minute windows,
// because a client active all hour is one client. Every resolution that
// should be chartable is therefore counted at that resolution and stored as
// its own series.
package servicetrends

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/storage"
	"github.com/freezxp/syslogc/backend/internal/storage/filter"
)

// Limits on a catalog.
const (
	MaxServices          = storage.MaxCategories
	MaxDomainsPerService = 32
	MaxNameBytes         = 64
	MaxLabelBytes        = 64
	MaxDomainBytes       = 253 // the longest legal DNS name
)

// Service is one named group of domains.
type Service struct {
	// Name identifies the service in the stored series. It is a metric label
	// value, so it is restricted to a slug.
	Name string `json:"name"`
	// Label is what people see; it may be anything printable.
	Label string `json:"label"`
	// Domains are matched against the domain field: a query matches when it
	// equals a domain or is a subdomain of one.
	Domains []string `json:"domains"`
	// Enabled is false to keep a service in the catalog without counting it.
	Enabled bool `json:"enabled"`
}

// Catalog is the set of services to count.
type Catalog struct {
	Services []Service `json:"services"`
}

// DefaultCatalog is the catalog a deployment starts with. It covers the
// services people usually ask about; it is meant to be edited.
func DefaultCatalog() Catalog {
	return Catalog{Services: []Service{
		{Name: "facebook", Label: "Facebook & Instagram", Enabled: true, Domains: []string{
			"facebook.com", "fbcdn.net", "fb.com", "instagram.com", "cdninstagram.com", "whatsapp.net", "whatsapp.com",
		}},
		{Name: "tiktok", Label: "TikTok", Enabled: true, Domains: []string{
			"tiktok.com", "tiktokv.com", "tiktokcdn.com", "byteoversea.com", "ibytedtos.com",
		}},
		{Name: "youtube", Label: "YouTube", Enabled: true, Domains: []string{
			"youtube.com", "youtu.be", "ytimg.com", "googlevideo.com", "youtubei.googleapis.com",
		}},
		{Name: "netflix", Label: "Netflix", Enabled: true, Domains: []string{
			"netflix.com", "nflxvideo.net", "nflxso.net", "nflximg.net",
		}},
		{Name: "x", Label: "X (Twitter)", Enabled: true, Domains: []string{
			"x.com", "twitter.com", "twimg.com", "t.co",
		}},
		{Name: "snapchat", Label: "Snapchat", Enabled: true, Domains: []string{
			"snapchat.com", "sc-cdn.net", "snap.com",
		}},
		{Name: "telegram", Label: "Telegram", Enabled: true, Domains: []string{
			"telegram.org", "t.me", "cdn-telegram.org", "telegram.me",
		}},
		{Name: "spotify", Label: "Spotify", Enabled: true, Domains: []string{
			"spotify.com", "scdn.co", "spotifycdn.com",
		}},
		{Name: "microsoft365", Label: "Microsoft 365", Enabled: true, Domains: []string{
			"office365.com", "office.com", "microsoft.com", "microsoftonline.com", "teams.microsoft.com", "sharepoint.com",
		}},
		{Name: "google", Label: "Google", Enabled: true, Domains: []string{
			"google.com", "gstatic.com", "googleapis.com", "gmail.com",
		}},
	}}
}

// Validate reports every problem with the catalog at once.
func (c Catalog) Validate() error {
	var errs []error
	if len(c.Services) > MaxServices {
		errs = append(errs, fmt.Errorf("at most %d services are allowed, the catalog has %d", MaxServices, len(c.Services)))
	}
	seen := make(map[string]bool, len(c.Services))
	for i, s := range c.Services {
		where := fmt.Sprintf("service %d", i+1)
		if s.Name != "" {
			where = fmt.Sprintf("service %q", s.Name)
		}
		if err := ValidName(s.Name); err != nil {
			errs = append(errs, fmt.Errorf("%s: name: %w", where, err))
		} else if seen[s.Name] {
			errs = append(errs, fmt.Errorf("%s: name: already used by another service", where))
		}
		seen[s.Name] = true
		if len(s.Label) > MaxLabelBytes {
			errs = append(errs, fmt.Errorf("%s: label: longer than %d characters", where, MaxLabelBytes))
		}
		if strings.ContainsAny(s.Label, "\n\r") {
			errs = append(errs, fmt.Errorf("%s: label: must be a single line", where))
		}
		switch {
		case len(s.Domains) == 0:
			errs = append(errs, fmt.Errorf("%s: needs at least one domain", where))
		case len(s.Domains) > MaxDomainsPerService:
			errs = append(errs, fmt.Errorf("%s: at most %d domains are allowed, it has %d",
				where, MaxDomainsPerService, len(s.Domains)))
		}
		for _, d := range s.Domains {
			if err := validDomain(d); err != nil {
				errs = append(errs, fmt.Errorf("%s: domain %q: %w", where, d, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Enabled returns the services that are counted, in a stable order.
func (c Catalog) Enabled() []Service {
	out := make([]Service, 0, len(c.Services))
	for _, s := range c.Services {
		if s.Enabled && len(s.Domains) > 0 {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Categories turns the enabled services into storage categories matched
// against domainField.
func (c Catalog) Categories(domainField string) []storage.Category {
	services := c.Enabled()
	out := make([]storage.Category, 0, len(services))
	for _, s := range services {
		out = append(out, storage.Category{Name: s.Name, Filter: s.Filter(domainField)})
	}
	return out
}

// Filter matches the service's domains against field.
//
// A domain matches itself exactly, or as a suffix after a dot, so
// "tiktok.com" covers "www.tiktok.com" without also covering
// "nottiktok.com". The suffix test is a substring match rather than an
// anchored one, which VictoriaLogs indexes well; the cost is that a domain
// that merely contains ".tiktok.com." somewhere — "x.tiktok.com.example.org"
// — also matches. That is rare, and a deliberate trade for speed.
func (s Service) Filter(field string) *filter.Expr {
	args := make([]*filter.Expr, 0, len(s.Domains)*2)
	for _, d := range s.Domains {
		d = strings.ToLower(strings.Trim(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}
		args = append(args,
			&filter.Expr{Op: filter.Eq, Field: field, Value: d},
			&filter.Expr{Op: filter.Contains, Field: field, Value: "." + d},
		)
	}
	if len(args) == 0 {
		return nil
	}
	if len(args) == 1 {
		return args[0]
	}
	return &filter.Expr{Op: filter.Or, Args: args}
}

// ValidName reports whether name is usable as a service name: it becomes a
// metric label value and part of a PromQL selector, so it is a plain slug.
func ValidName(name string) error {
	switch {
	case name == "":
		return errors.New("is required")
	case len(name) > MaxNameBytes:
		return fmt.Errorf("longer than %d characters", MaxNameBytes)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("unexpected character %q (use lower-case letters, digits, - and _)", r)
		}
	}
	return nil
}

func validDomain(d string) error {
	switch {
	case strings.TrimSpace(d) == "":
		return errors.New("is empty")
	case len(d) > MaxDomainBytes:
		return fmt.Errorf("longer than %d characters", MaxDomainBytes)
	case strings.ContainsAny(d, " \t\n\r\"'*"):
		return errors.New("must be a plain domain name, without spaces or wildcards")
	case !strings.Contains(strings.Trim(d, "."), "."):
		return errors.New("must be a domain name, such as tiktok.com")
	}
	return nil
}
