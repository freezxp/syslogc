package rfc3164

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/freezxp/syslogc/backend/internal/logentry"
	"github.com/freezxp/syslogc/backend/internal/parser"
)

// received is the fixed receive time used by tests (year inference depends on it).
var received = time.Date(2026, 9, 14, 10, 0, 5, 0, time.UTC)

func ts(year int, month time.Month, day, h, m, s, ns int, loc *time.Location) time.Time {
	return time.Date(year, month, day, h, m, s, ns, loc)
}

type expect struct {
	time       time.Time
	facility   logentry.Facility
	severity   logentry.Severity
	defaultSev bool
	hostname   string
	app        string
	pid        string
	message    string
	fields     []logentry.Field
}

func TestParse(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   string
		loc  *time.Location
		want expect
	}{
		{
			name: "RFC 3164 example",
			in:   `<34>Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8`,
			want: expect{time: ts(2025, 10, 11, 22, 14, 15, 0, time.UTC), facility: 4, severity: 2,
				hostname: "mymachine", app: "su", message: "'su root' failed for lonvick on /dev/pts/8"},
		},
		{
			name: "util-linux logger --rfc3164",
			in:   `<13>Sep 14 10:00:01 host01 ubuntu: Test syslog message`,
			want: expect{time: ts(2026, 9, 14, 10, 0, 1, 0, time.UTC), facility: 1, severity: 5,
				hostname: "host01", app: "ubuntu", message: "Test syslog message"},
		},
		{
			name: "tag with pid, space-padded day",
			in:   `<38>Sep  4 09:30:00 web-1 sshd[4021]: Failed password for root from 203.0.113.9 port 51022 ssh2`,
			want: expect{time: ts(2026, 9, 4, 9, 30, 0, 0, time.UTC), facility: 4, severity: 6,
				hostname: "web-1", app: "sshd", pid: "4021", message: "Failed password for root from 203.0.113.9 port 51022 ssh2"},
		},
		{
			name: "no hostname (local socket style)",
			in:   `<30>Sep 14 09:59:59 systemd[1]: Started Daily apt download activities.`,
			want: expect{time: ts(2026, 9, 14, 9, 59, 59, 0, time.UTC), facility: 3, severity: 6,
				app: "systemd", pid: "1", message: "Started Daily apt download activities."},
		},
		{
			name: "no PRI defaults to user.notice",
			in:   `Sep 14 09:00:00 box kernel: eth0 link up`,
			want: expect{time: ts(2026, 9, 14, 9, 0, 0, 0, time.UTC), facility: 1, severity: 5, defaultSev: true,
				hostname: "box", app: "kernel", message: "eth0 link up"},
		},
		{
			name: "no timestamp: everything after PRI is content",
			in:   `<11>myapp: something broke`,
			want: expect{facility: 1, severity: 3, app: "myapp", message: "something broke"},
		},
		{
			name: "no timestamp, no tag",
			in:   `<11>just some text: with colon`,
			want: expect{facility: 1, severity: 3, message: "just some text: with colon"},
		},
		{
			name: "fractional seconds and year",
			in:   `<189>Sep 14 2026 10:00:00.123 router1 %LINK-3-UPDOWN: Interface Gi0/1, changed state to down`,
			want: expect{time: ts(2026, 9, 14, 10, 0, 0, 123_000_000, time.UTC), facility: 23, severity: 5,
				hostname: "router1", message: "%LINK-3-UPDOWN: Interface Gi0/1, changed state to down"},
		},
		{
			name: "Cisco IOS with sequence number, unsynced clock marker and UTC zone",
			in:   `<189>000123: *Sep 14 09:46:11.123 UTC: %SYS-5-CONFIG_I: Configured from console by admin`,
			want: expect{time: ts(2026, 9, 14, 9, 46, 11, 123_000_000, time.UTC), facility: 23, severity: 5,
				message: "%SYS-5-CONFIG_I: Configured from console by admin",
				fields:  []logentry.Field{{Key: "sequence", Value: "000123"}}},
		},
		{
			name: "Cisco ASA: message id stays in message (bounded app_name cardinality)",
			in:   `<166>Sep 14 2026 09:12:01: %ASA-6-302013: Built outbound TCP connection 1234 for outside:198.51.100.7/443`,
			want: expect{time: ts(2026, 9, 14, 9, 12, 1, 0, time.UTC), facility: 20, severity: 6,
				message: "%ASA-6-302013: Built outbound TCP connection 1234 for outside:198.51.100.7/443"},
		},
		{
			name: "ISO 8601 timestamp (rsyslog forward format)",
			in:   `<86>2026-09-14T10:00:00.5+02:00 app01 CRON[999]: (root) CMD (run-parts /etc/cron.hourly)`,
			want: expect{time: ts(2026, 9, 14, 8, 0, 0, 500_000_000, time.UTC), facility: 10, severity: 6,
				hostname: "app01", app: "CRON", pid: "999", message: "(root) CMD (run-parts /etc/cron.hourly)"},
		},
		{
			name: "ISO timestamp without offset uses source timezone",
			in:   `<14>2026-09-14T12:00:00 nas01 smbd: share mounted`,
			loc:  berlin,
			want: expect{time: ts(2026, 9, 14, 12, 0, 0, 0, berlin), facility: 1, severity: 6,
				hostname: "nas01", app: "smbd", message: "share mounted"},
		},
		{
			name: "BSD timestamp in source timezone",
			in:   `<14>Sep 14 12:00:00 nas01 smbd: share mounted`,
			loc:  berlin,
			want: expect{time: ts(2026, 9, 14, 12, 0, 0, 0, berlin), facility: 1, severity: 6,
				hostname: "nas01", app: "smbd", message: "share mounted"},
		},
		{
			name: "uppercase hostname with colon is not a timezone",
			in:   `<14>Sep 14 09:00:00 CORE: link flap`,
			want: expect{time: ts(2026, 9, 14, 9, 0, 0, 0, time.UTC), facility: 1, severity: 6,
				app: "CORE", message: "link flap"},
		},
		{
			name: "IPv4 hostname and kv body (Fortinet style)",
			in:   `<189>Sep 14 09:00:00 10.10.1.1 date=2026-09-14 time=09:00:00 devname="fw01" type="event" subtype="vpn" msg="tunnel down"`,
			want: expect{time: ts(2026, 9, 14, 9, 0, 0, 0, time.UTC), facility: 23, severity: 5,
				hostname: "10.10.1.1", message: `date=2026-09-14 time=09:00:00 devname="fw01" type="event" subtype="vpn" msg="tunnel down"`},
		},
		{
			name: "tag containing slash and dot",
			in:   `<22>Sep 14 09:00:00 mail postfix/smtpd[77]: connect from unknown[192.0.2.10]`,
			want: expect{time: ts(2026, 9, 14, 9, 0, 0, 0, time.UTC), facility: 2, severity: 6,
				hostname: "mail", app: "postfix/smtpd", pid: "77", message: "connect from unknown[192.0.2.10]"},
		},
		{
			name: "key:value without space is not a tag",
			in:   `<14>Sep 14 09:00:00 host status:ok everything fine`,
			want: expect{time: ts(2026, 9, 14, 9, 0, 0, 0, time.UTC), facility: 1, severity: 6,
				hostname: "host", message: "status:ok everything fine"},
		},
		{
			name: "year rollover: December message received in January",
			in:   `<14>Dec 31 23:59:58 host app: late`,
			want: expect{time: ts(2025, 12, 31, 23, 59, 58, 0, time.UTC), facility: 1, severity: 6,
				hostname: "host", app: "app", message: "late"},
		},
		{
			name: "only PRI and timestamp",
			in:   `<14>Sep 14 09:00:00`,
			want: expect{time: ts(2026, 9, 14, 9, 0, 0, 0, time.UTC), facility: 1, severity: 6},
		},
		{
			name: "invalid PRI treated as content",
			in:   `<999>Sep 14 09:00:00 host app: x`,
			want: expect{facility: 1, severity: 5, defaultSev: true, message: `<999>Sep 14 09:00:00 host app: x`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recv := received
			if strings.Contains(tt.name, "rollover") {
				recv = time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC)
			}
			var e logentry.Entry
			e.Reset()
			err := New().Parse(&parser.Input{Data: tt.in, ReceivedAt: recv, Location: tt.loc}, &e)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			want := logentry.Entry{
				Time: tt.want.time, Facility: tt.want.facility, Severity: tt.want.severity,
				SeveritySource: logentry.SeverityFromLog,
				Hostname:       tt.want.hostname, AppName: tt.want.app, ProcessID: tt.want.pid,
				Message: tt.want.message, Fields: tt.want.fields,
			}
			if tt.want.defaultSev {
				want.SeveritySource = logentry.SeverityDefault
			}
			opts := cmp.Options{cmpopts.EquateEmpty(), cmpopts.EquateComparable(netip.Addr{}), cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })}
			if diff := cmp.Diff(want, e, opts); diff != "" {
				t.Errorf("Parse(%q) mismatch (-want +got):\n%s", tt.in, diff)
			}
		})
	}
}

func TestInferYear(t *testing.T) {
	tests := []struct {
		received time.Time
		month    int
		day      int
		wantYear int
	}{
		{time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), 9, 14, 2026},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 12, 31, 2025},   // late delivery across new year
		{time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC), 1, 1, 2026}, // sender clock slightly ahead
		{time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), 9, 20, 2026},   // within a week in the future
		{time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), 10, 1, 2025},   // weeks ahead → previous year
	}
	for _, tt := range tests {
		got := inferYear(tt.month, tt.day, 0, 0, 0, 0, time.UTC, tt.received)
		if got.Year() != tt.wantYear {
			t.Errorf("inferYear(%d-%d, recv %s) = %d, want %d", tt.month, tt.day, tt.received, got.Year(), tt.wantYear)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`<34>Oct 11 22:14:15 mymachine su: 'su root' failed`,
		`<189>000123: *Sep 14 09:46:11.123 UTC: %SYS-5-CONFIG_I: Configured`,
		`<86>2026-09-14T10:00:00.5+02:00 app01 CRON[999]: (root) CMD`,
		`Sep  4 09:30:00 web-1 sshd[4021]: Failed`,
		`<14>Dec 31 23:59:60.9999999999 h a[`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		var e logentry.Entry
		e.Reset()
		err := New().Parse(&parser.Input{Data: data, ReceivedAt: received}, &e)
		if data == "" {
			if err == nil {
				t.Fatal("empty input accepted")
			}
			return
		}
		if err != nil {
			t.Fatalf("non-empty input rejected: %v", err)
		}
		if !e.Severity.Valid() || !e.Facility.Valid() {
			t.Fatalf("invalid facility/severity %d/%d", e.Facility, e.Severity)
		}
		if !strings.HasSuffix(data, e.Message) {
			t.Fatalf("message %q is not a suffix of input", e.Message)
		}
		for _, s := range []string{e.Hostname, e.AppName, e.ProcessID} {
			if strings.ContainsAny(s, " \n") {
				t.Fatalf("header token contains whitespace: %q", s)
			}
		}
	})
}

func BenchmarkParse(b *testing.B) {
	in := &parser.Input{
		Data:       `<38>Sep  4 09:30:00 web-1 sshd[4021]: Failed password for root from 203.0.113.9 port 51022 ssh2`,
		ReceivedAt: received,
		Location:   time.UTC,
	}
	p := New()
	var e logentry.Entry
	b.ReportAllocs()
	for b.Loop() {
		e.Reset()
		if err := p.Parse(in, &e); err != nil {
			b.Fatal(err)
		}
	}
}
