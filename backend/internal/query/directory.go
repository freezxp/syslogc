package query

import (
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// defaultAnalysisBuckets is how many points an analysis chart holds unless
// asked otherwise — enough to show the shape of a day without drawing more
// than a screen can distinguish.
const defaultAnalysisBuckets = 120

// AnalysisSelection resolves a request for one of the source-template
// analyses — the directory, SQL Server, IIS — into a selection and the step
// its charts should use, applying the same range and permission limits as any
// other search.
//
// They share this because they ask the same question of the same store: a
// window, an optional filter, and however many points a chart can show.
func (s *Service) AnalysisSelection(p *auth.Principal, in Selection, buckets int) (storage.Selection, time.Duration, error) {
	r, err := s.resolve(p, in)
	if err != nil {
		return storage.Selection{}, 0, err
	}
	if buckets <= 0 || buckets > 1000 {
		buckets = defaultAnalysisBuckets
	}
	return r.sel, chooseStep(r.sel.Range, buckets), nil
}

// DirectorySelection is AnalysisSelection under the name the directory
// handler has called it since before there was anything to share it with.
func (s *Service) DirectorySelection(p *auth.Principal, in Selection, buckets int) (storage.Selection, time.Duration, error) {
	return s.AnalysisSelection(p, in, buckets)
}
