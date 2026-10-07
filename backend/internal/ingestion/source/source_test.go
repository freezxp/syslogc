package source

import (
	"strings"
	"testing"

	"github.com/freezxp/syslogc/backend/internal/config"
	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/metrics"
	"github.com/freezxp/syslogc/backend/internal/sourcetemplate"
)

func windowsSource(t *testing.T, template string, parts []string) *Settings {
	t.Helper()
	s, err := New(config.Source{
		Name: "win", Type: "syslog", Protocol: "tcp", Address: ":6514", Format: "auto",
		Timezone: "UTC", RawMessage: "on_error", Tenant: "default", HostnameFallback: "none",
		SDFlatten: "full", Framing: "auto", MaxMessageBytes: 65536,
		Template: template, TemplateParts: parts,
	}, metrics.New("test", "test"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// fieldsOf applies every rule the source carries, as the worker does.
func fieldsOf(s *Settings, message string) map[string]string {
	e := &logentry.Entry{Message: message}
	for _, j := range s.ExtractJSON {
		j.Apply(e)
	}
	out := map[string]string{}
	for _, f := range e.Fields {
		out[f.Key] = f.Value
	}
	return out
}

// One NXLog instance sends a server's Security log, SQL Server log and IIS
// log over one connection, so all three arrive at one source. Each has to
// become its own fields, and none may pick up another's.
func TestOneSourceKeepsEachWindowsLogApart(t *testing.T) {
	s := windowsSource(t, "windows-server",
		[]string{sourcetemplate.PartDirectory, sourcetemplate.PartMSSQL, sourcetemplate.PartIIS})

	cases := []struct {
		name    string
		message string
		want    map[string]string
		absent  []string
	}{
		{
			name:    "a domain controller sign-in",
			message: `{"syslogc_part":"ad","EventID":4624,"TargetUserName":"a.hassan","IpAddress":"10.20.4.19"}`,
			want:    map[string]string{"ad.event_id": "4624", "ad.user": "a.hassan", "ad.source_ip": "10.20.4.19"},
			absent:  []string{"mssql.event_id", "iis.status"},
		},
		{
			name:    "a SQL Server event",
			message: `{"syslogc_part":"mssql","EventID":18456,"SourceName":"MSSQLSERVER","Severity":"ERROR"}`,
			want:    map[string]string{"mssql.event_id": "18456", "mssql.provider": "MSSQLSERVER", "mssql.severity": "ERROR"},
			// The event id key is the same one Active Directory records use,
			// which is exactly the collision the marker field prevents.
			absent: []string{"ad.event_id", "iis.status"},
		},
		{
			name:    "an IIS request",
			message: `{"syslogc_part":"iis","cs_method":"POST","cs_uri_stem":"/app/login","sc_status":"500","time_taken":"1240","c_ip":"203.0.113.9"}`,
			want: map[string]string{
				"iis.method": "POST", "iis.uri": "/app/login", "iis.status": "500",
				"iis.time_taken": "1240", "iis.client_ip": "203.0.113.9",
			},
			absent: []string{"ad.event_id", "mssql.event_id"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fieldsOf(s, c.message)
			for name, want := range c.want {
				if got[name] != want {
					t.Errorf("%s = %q, want %q", name, got[name], want)
				}
			}
			for _, name := range c.absent {
				if _, ok := got[name]; ok {
					t.Errorf("%s was set from another part's record: %q", name, got[name])
				}
			}
		})
	}
}

// A source saved before parts existed names the retired template id and no
// parts at all. It has to keep producing exactly what it produced before.
func TestAnExistingActiveDirectorySourceIsUnchanged(t *testing.T) {
	s := windowsSource(t, "active-directory", nil)
	got := fieldsOf(s, `{"syslogc_part":"ad","EventID":4740,"TargetUserName":"s.tan","CallerComputerName":"PHONE-ST"}`)
	for name, want := range map[string]string{
		"ad.event_id": "4740", "ad.user": "s.tan", "ad.caller_computer": "PHONE-ST",
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
	// It must not have quietly gained the other parts: a domain controller is
	// not running IIS, and an empty page is worse than no page.
	if _, ok := got["iis.status"]; ok {
		t.Error("an Active Directory source gained IIS fields")
	}
	analyses := sourcetemplate.AnalysesFor([]sourcetemplate.Use{{Template: "active-directory"}})
	if len(analyses) != 1 || analyses[0] != sourcetemplate.AnalysisDirectory {
		t.Errorf("analyses = %v, want only %q", analyses, sourcetemplate.AnalysisDirectory)
	}
}

// Choosing one part must not offer the pages the others feed.
func TestPartsDecideWhichAnalysesAreOffered(t *testing.T) {
	for _, c := range []struct {
		parts []string
		want  []string
	}{
		{[]string{sourcetemplate.PartIIS}, []string{sourcetemplate.AnalysisIIS}},
		{[]string{sourcetemplate.PartMSSQL}, []string{sourcetemplate.AnalysisMSSQL}},
		{
			[]string{sourcetemplate.PartIIS, sourcetemplate.PartDirectory},
			[]string{sourcetemplate.AnalysisDirectory, sourcetemplate.AnalysisIIS},
		},
	} {
		got := sourcetemplate.AnalysesFor([]sourcetemplate.Use{{Template: "windows-server", Parts: c.parts}})
		if len(got) != len(c.want) {
			t.Errorf("parts %v unlocked %v, want %v", c.parts, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parts %v unlocked %v, want %v", c.parts, got, c.want)
				break
			}
		}
	}
}

// The generated configuration must carry only the chosen parts, and route
// every one of them to the single output.
func TestTheGeneratedNXLogConfigMatchesTheChosenParts(t *testing.T) {
	tpl, ok := sourcetemplate.ByID("windows-server")
	if !ok {
		t.Fatal("the Windows template is missing")
	}
	cfg := tpl.NXLogConfig([]string{sourcetemplate.PartDirectory, sourcetemplate.PartIIS})
	for _, want := range []string{"<Input ad>", "<Input iis>", "im_msvistalog", "im_file", "Path    ad, iis => syslogc"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the configuration is missing %q", want)
		}
	}
	if strings.Contains(cfg, "<Input mssql>") {
		t.Error("the configuration carries a part that was not chosen")
	}
	// Every input has to stamp the marker, or the server cannot tell the
	// records apart once they share a connection.
	if n := strings.Count(cfg, "$syslogc_part ="); n != 2 {
		t.Errorf("found %d marker assignments, want one per chosen part", n)
	}
}
