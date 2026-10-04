package api

import (
	"net/http"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/sourcetemplate"
)

// templateBody is one template, with whether this deployment uses it.
type templateBody struct {
	sourcetemplate.Template
	// InUse is true when an enabled source carries this template, which is
	// what decides whether its analyses are offered.
	InUse bool `json:"in_use"`
	// Sources names the enabled sources using it, so the interface can say
	// where the data comes from rather than only that it exists.
	Sources []string `json:"sources,omitempty"`
}

// handleTemplates lists the shapes of log this deployment can make sense of,
// and which are actually in use.
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	using := s.templatesInUse(r, p)
	out := make([]templateBody, 0, len(sourcetemplate.All()))
	for _, t := range sourcetemplate.All() {
		names := using[strings.ToLower(t.ID)]
		out = append(out, templateBody{Template: t, InUse: len(names) > 0, Sources: names})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"templates": out,
		// The analyses worth offering, so the interface does not have to
		// work it out from the templates itself.
		"analyses": sourcetemplate.AnalysesFor(inUseIDs(using)),
	})
	return nil
}

func inUseIDs(using map[string][]string) []string {
	out := make([]string, 0, len(using))
	for id := range using {
		out = append(out, id)
	}
	return out
}

// templatesInUse maps a template to the enabled sources carrying it.
//
// Only enabled sources count: a disabled one produces nothing, so an analysis
// resting on it would be permanently empty, which is the situation this is
// meant to avoid.
func (s *Server) templatesInUse(r *http.Request, p *auth.Principal) map[string][]string {
	out := map[string][]string{}
	note := func(sc config.Source, enabled bool) {
		id := strings.ToLower(strings.TrimSpace(sc.Template))
		if id == "" || !enabled {
			return
		}
		out[id] = append(out[id], sc.Name)
	}
	adopted := map[string]bool{}
	if s.opts.API.Store != nil {
		managed, err := s.opts.API.Store.ListSources(r.Context(), p.Tenant)
		if err == nil {
			for _, m := range managed {
				body, err := managedSource(m, nil)
				if err != nil {
					continue
				}
				if m.Adopted {
					adopted[strings.ToLower(m.Name)] = true
				}
				note(body.Config, m.Enabled)
			}
		}
	}
	for _, sc := range s.opts.API.FileSources {
		// A file source that has been adopted is replaced by its copy, which
		// has already been counted.
		if adopted[strings.ToLower(sc.Name)] {
			continue
		}
		note(sc, sc.IsEnabled())
	}
	return out
}
