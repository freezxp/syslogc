// Package sourcetemplate describes the shapes of log a deployment knows how
// to make sense of.
//
// A template is three things that only make sense together: the rules that
// turn one kind of log into fields, the analyses those fields support, and
// how to configure whatever is sending the logs. Choosing "Active Directory"
// when adding a source should set up the parsing and reveal the Active
// Directory analysis, because otherwise the two are configured separately
// and a mismatch between them is silent — fields that nothing reads, or a
// page that is permanently empty.
//
// An analysis is therefore shown only when some enabled source uses the
// template that feeds it. A deployment that collects only DNS has no reason
// to be offered an Active Directory page it can never fill.
package sourcetemplate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/extract"
)

// Analyses a template can unlock. They name pages, so they are stable.
const (
	// AnalysisDNSServices counts distinct clients per online service.
	AnalysisDNSServices = "dns-services"
	// AnalysisDirectory reports sign-ins, lockouts and account changes.
	AnalysisDirectory = "directory"
)

// Template is one recognised shape of log.
type Template struct {
	// ID identifies the template in a source's configuration.
	ID string `json:"id"`
	// Title and Description are what someone choosing one reads.
	Title       string `json:"title"`
	Description string `json:"description"`
	// Extract are the rules a source using this template is given. They are
	// a starting point: a source keeps its own copy, so editing them later
	// does not fight the template.
	Extract []extract.Config `json:"extract,omitempty"`
	// JSON promotes the keys of a JSON message body to fields, for senders
	// that emit structured records rather than prose. Parsing English event
	// text breaks on a server in another language; parsing the sender's own
	// JSON does not.
	JSON *JSONExtract `json:"json,omitempty"`
	// Fields are what the template produces, so the interface can say what
	// becomes searchable without waiting for a log to arrive.
	Fields []Field `json:"fields,omitempty"`
	// Analyses this template feeds.
	Analyses []string `json:"analyses,omitempty"`
	// Setup is how to configure the sender.
	Setup Setup `json:"setup"`
}

// Field is one value a template produces.
type Field struct {
	Name string `json:"name"`
	// What it holds, in the terms someone searching would use.
	Description string `json:"description"`
	// Example helps far more than a type name does.
	Example string `json:"example,omitempty"`
}

// JSONExtract promotes keys of a JSON message body to log fields.
type JSONExtract struct {
	// Prefix is prepended to every field name, so a template's fields are
	// recognisable and cannot collide with another's.
	Prefix string `json:"prefix,omitempty"`
	// Keys are the JSON keys to promote, mapped to the field name they
	// become. A key that is absent from a record is simply not set.
	Keys map[string]string `json:"keys,omitempty"`
}

// Setup tells someone how to get these logs to Syslogc.
type Setup struct {
	// Sender is what produces the logs, e.g. "NXLog Community Edition".
	Sender string `json:"sender"`
	// Summary is one or two sentences on what has to happen.
	Summary string `json:"summary"`
	// Steps are what to do, in order, each a complete instruction.
	Steps []Step `json:"steps,omitempty"`
	// Reference points at the sender's own documentation.
	Reference string `json:"reference,omitempty"`
}

// Step is one instruction, optionally with something to copy.
type Step struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// Config is text to copy, such as a configuration file. The interface
	// offers it for copying rather than making someone retype it.
	Config string `json:"config,omitempty"`
	// Language labels the snippet for highlighting, e.g. "apache", "powershell".
	Language string `json:"language,omitempty"`
}

// All returns every template, in the order they should be offered.
func All() []Template { return []Template{dnsTemplate(), directoryTemplate()} }

// ByID returns a template, or false.
func ByID(id string) (Template, bool) {
	for _, t := range All() {
		if strings.EqualFold(t.ID, id) {
			return t, true
		}
	}
	return Template{}, false
}

// Valid reports whether id names a template. An empty id is valid: a source
// need not use one, and most do not.
func Valid(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if _, ok := ByID(id); !ok {
		return fmt.Errorf("unknown template %q; known templates are %s", id, strings.Join(IDs(), ", "))
	}
	return nil
}

// IDs lists every template identifier.
func IDs() []string {
	out := make([]string, 0, len(All()))
	for _, t := range All() {
		out = append(out, t.ID)
	}
	sort.Strings(out)
	return out
}

// AnalysesFor reports which analyses the given templates unlock, without
// duplicates. It is what decides whether a page is offered at all.
func AnalysesFor(templateIDs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range templateIDs {
		t, ok := ByID(id)
		if !ok {
			continue
		}
		for _, a := range t.Analyses {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}
