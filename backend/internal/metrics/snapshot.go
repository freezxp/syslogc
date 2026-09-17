package metrics

import (
	"math"
	"sort"

	dto "github.com/prometheus/client_model/go"
)

// SourceTotals are cumulative counters of one source.
type SourceTotals struct {
	Name              string           `json:"name"`
	Protocol          string           `json:"protocol,omitempty"`
	Received          int64            `json:"received"`
	BytesReceived     int64            `json:"bytes_received"`
	Parsed            int64            `json:"parsed"`
	ParseErrors       int64            `json:"parse_errors"`
	Stored            int64            `json:"stored"`
	Dropped           map[string]int64 `json:"dropped"`
	ActiveConnections int64            `json:"active_connections"`
}

// Totals is a snapshot of ingestion counters.
type Totals struct {
	Sources     map[string]*SourceTotals
	BytesStored int64
	// StorageHealthy is false if any storage backend's last write failed;
	// nil before the gauge is set.
	StorageHealthy *bool
	// E2E latency quantile estimates in seconds (NaN when no data).
	E2EP50, E2EP99 float64
}

// Sum returns counters summed over all sources.
func (t Totals) Sum() SourceTotals {
	var s SourceTotals
	s.Dropped = map[string]int64{}
	for _, st := range t.Sources {
		s.Received += st.Received
		s.BytesReceived += st.BytesReceived
		s.Parsed += st.Parsed
		s.ParseErrors += st.ParseErrors
		s.Stored += st.Stored
		for k, v := range st.Dropped {
			s.Dropped[k] += v
		}
	}
	return s
}

// SortedSources returns sources ordered by name.
func (t Totals) SortedSources() []*SourceTotals {
	out := make([]*SourceTotals, 0, len(t.Sources))
	for _, s := range t.Sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snapshot reads current ingestion counters from the registry.
func (m *Metrics) Snapshot() (Totals, error) {
	families, err := m.Registry.Gather()
	if err != nil {
		return Totals{}, err
	}
	t := Totals{Sources: map[string]*SourceTotals{}, E2EP50: math.NaN(), E2EP99: math.NaN()}
	src := func(metric *dto.Metric) *SourceTotals {
		name := label(metric, "source")
		s, ok := t.Sources[name]
		if !ok {
			s = &SourceTotals{Name: name, Dropped: map[string]int64{}}
			t.Sources[name] = s
		}
		if p := label(metric, "protocol"); p != "" {
			s.Protocol = p
		}
		return s
	}
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			v := int64(metric.GetCounter().GetValue())
			switch f.GetName() {
			case namespace + "_ingest_messages_received_total":
				src(metric).Received += v
			case namespace + "_ingest_bytes_received_total":
				src(metric).BytesReceived += v
			case namespace + "_ingest_messages_parsed_total":
				src(metric).Parsed += v
			case namespace + "_ingest_parse_errors_total":
				src(metric).ParseErrors += v
			case namespace + "_ingest_messages_stored_total":
				src(metric).Stored += v
			case namespace + "_ingest_messages_dropped_total":
				if v > 0 {
					src(metric).Dropped[label(metric, "reason")] += v
				} else {
					src(metric)
				}
			case namespace + "_ingest_active_connections":
				src(metric).ActiveConnections += int64(metric.GetGauge().GetValue())
			case namespace + "_storage_healthy":
				healthy := metric.GetGauge().GetValue() == 1 && (t.StorageHealthy == nil || *t.StorageHealthy)
				t.StorageHealthy = &healthy
			case namespace + "_ingest_bytes_stored_total":
				t.BytesStored += v
			case namespace + "_ingest_e2e_latency_seconds":
				h := metric.GetHistogram()
				t.E2EP50, t.E2EP99 = quantile(h, 0.5), quantile(h, 0.99)
			}
		}
	}
	return t, nil
}

func label(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// quantile estimates a quantile from cumulative histogram buckets by
// returning the upper bound of the bucket containing it.
func quantile(h *dto.Histogram, q float64) float64 {
	total := h.GetSampleCount()
	if total == 0 {
		return math.NaN()
	}
	rank := uint64(math.Ceil(q * float64(total)))
	for _, b := range h.GetBucket() {
		if b.GetCumulativeCount() >= rank {
			return b.GetUpperBound()
		}
	}
	return math.Inf(1)
}
