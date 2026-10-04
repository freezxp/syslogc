package query

import (
	"time"

	"github.com/freezxp/syslogc/backend/internal/auth"
	"github.com/freezxp/syslogc/backend/internal/storage"
)

// defaultDirectoryBuckets is how many points the activity chart holds unless
// asked otherwise — enough to show the shape of a day without drawing more
// than a screen can distinguish.
const defaultDirectoryBuckets = 120

// DirectorySelection resolves a directory request into a selection and the
// step its chart should use, applying the same range and permission limits as
// any other search.
func (s *Service) DirectorySelection(p *auth.Principal, in Selection, buckets int) (storage.Selection, time.Duration, error) {
	r, err := s.resolve(p, in)
	if err != nil {
		return storage.Selection{}, 0, err
	}
	if buckets <= 0 || buckets > 1000 {
		buckets = defaultDirectoryBuckets
	}
	return r.sel, chooseStep(r.sel.Range, buckets), nil
}
