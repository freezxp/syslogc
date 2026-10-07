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
	// PartsInUse names the parts some enabled source actually carries. A
	// template can be in use while most of its parts are not, and only the
	// ones that are unlock anything.
	PartsInUse []string `json:"parts_in_use,omitempty"`
}

// handleTemplates lists the shapes of log this deployment can make sense of,
// and which are actually in use.
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	using, uses := s.templatesInUse(r, p)
	out := make([]templateBody, 0, len(sourcetemplate.All()))
	for _, t := range sourcetemplate.All() {
		names := using[strings.ToLower(t.ID)]
		out = append(out, templateBody{
			Template: t, InUse: len(names) > 0, Sources: names,
			PartsInUse: partsInUse(t, uses),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"templates": out,
		// The analyses worth offering, so the interface does not have to
		// work it out from the templates itself.
		"analyses": sourcetemplate.AnalysesFor(uses),
	})
	return nil
}

// partsInUse names the parts of t that some enabled source carries.
func partsInUse(t sourcetemplate.Template, uses []sourcetemplate.Use) []string {
	if len(t.Parts) == 0 {
		return nil
	}
	on := map[string]bool{}
	for _, u := range uses {
		if got, ok := sourcetemplate.ByID(u.Template); !ok || got.ID != t.ID {
			continue
		}
		parts := u.Parts
		if len(parts) == 0 {
			parts = sourcetemplate.DefaultParts(u.Template)
		}
		for _, part := range t.SelectedParts(parts) {
			on[part.ID] = true
		}
	}
	// In the template's own order, so the interface reads consistently.
	out := make([]string, 0, len(on))
	for _, part := range t.Parts {
		if on[part.ID] {
			out = append(out, part.ID)
		}
	}
	return out
}

// templatesInUse maps a template to the enabled sources carrying it.
//
// Only enabled sources count: a disabled one produces nothing, so an analysis
// resting on it would be permanently empty, which is the situation this is
// meant to avoid.
func (s *Server) templatesInUse(r *http.Request, p *auth.Principal) (map[string][]string, []sourcetemplate.Use) {
	out := map[string][]string{}
	var uses []sourcetemplate.Use
	note := func(sc config.Source, enabled bool) {
		id := strings.ToLower(strings.TrimSpace(sc.Template))
		if id == "" || !enabled {
			return
		}
		// Keyed by the resolved template, so a source saved against a
		// retired id is counted against the one that replaced it.
		if t, ok := sourcetemplate.ByID(id); ok {
			id = strings.ToLower(t.ID)
		}
		out[id] = append(out[id], sc.Name)
		uses = append(uses, sourcetemplate.Use{Template: sc.Template, Parts: sc.TemplateParts})
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
	return out, uses
}

// handleTemplateConfig returns the sender configuration for a template and a
// chosen set of parts.
//
// It is generated rather than shown as a fixed snippet because the parts
// decide what is in it: a server sending only IIS needs none of the event-log
// inputs, and a file somebody has to edit down by hand is a file they will
// get wrong. The parts come as a repeated query parameter so the page can ask
// for exactly what is ticked.
func (s *Server) handleTemplateConfig(w http.ResponseWriter, r *http.Request, _ *auth.Principal) error {
	id := r.PathValue("id")
	t, ok := sourcetemplate.ByID(id)
	if !ok {
		return errStatus(http.StatusNotFound, "not_found",
			"unknown template %q; known templates are %s", id, strings.Join(sourcetemplate.IDs(), ", "))
	}
	parts := r.URL.Query()["part"]
	if len(parts) == 0 {
		parts = sourcetemplate.DefaultParts(id)
	}
	if err := t.ValidParts(parts); err != nil {
		return badRequest("validation_failed", "/part", "%v", err)
	}
	config := t.NXLogConfig(parts)
	if config == "" {
		return errStatus(http.StatusNotFound, "not_configured",
			"template %s does not generate a sender configuration", t.ID)
	}
	chosen := make([]string, 0, len(t.Parts))
	for _, p := range t.SelectedParts(parts) {
		chosen = append(chosen, p.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"template": t.ID,
		"parts":    chosen,
		"filename": "nxlog.conf",
		"language": "apache",
		"config":   config,
	})
	return nil
}
