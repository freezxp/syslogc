package logentry

// Severity is the syslog severity code (0 = emergency … 7 = debug).
type Severity uint8

const (
	SeverityEmergency Severity = iota
	SeverityAlert
	SeverityCritical
	SeverityError
	SeverityWarning
	SeverityNotice
	SeverityInfo
	SeverityDebug
)

var severityNames = [...]string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}

// String returns the canonical severity name.
func (s Severity) String() string {
	if int(s) < len(severityNames) {
		return severityNames[s]
	}
	return "unknown"
}

func (s Severity) Valid() bool { return s <= SeverityDebug }

// Facility is the syslog facility code (0–23). NoFacility marks its absence
// (e.g. logs that did not arrive via syslog).
type Facility uint8

const NoFacility Facility = 255

var facilityNames = [...]string{
	"kern", "user", "mail", "daemon", "auth", "syslog", "lpr", "news",
	"uucp", "cron", "authpriv", "ftp", "ntp", "security", "console", "solaris-cron",
	"local0", "local1", "local2", "local3", "local4", "local5", "local6", "local7",
}

// String returns the canonical facility name.
func (f Facility) String() string {
	if int(f) < len(facilityNames) {
		return facilityNames[f]
	}
	return ""
}

func (f Facility) Valid() bool { return int(f) < len(facilityNames) }

// Protocol is the transport a log arrived on.
type Protocol uint8

const (
	ProtocolUnknown Protocol = iota
	ProtocolUDP
	ProtocolTCP
	ProtocolTLS
	ProtocolHTTP
)

var protocolNames = [...]string{"unknown", "udp", "tcp", "tls", "http"}

func (p Protocol) String() string {
	if int(p) < len(protocolNames) {
		return protocolNames[p]
	}
	return "unknown"
}

// Format is the detected or configured log format.
type Format uint8

const (
	FormatUnknown Format = iota
	FormatRFC5424
	FormatRFC3164
	FormatJSON
)

var formatNames = [...]string{"unknown", "rfc5424", "rfc3164", "json"}

func (f Format) String() string {
	if int(f) < len(formatNames) {
		return formatNames[f]
	}
	return "unknown"
}

// ParseFormat returns the Format named s.
func ParseFormat(s string) (Format, bool) {
	for i, n := range formatNames {
		if n == s {
			return Format(i), true
		}
	}
	return FormatUnknown, false
}

// SourceType is the kind of receiver that produced a log.
type SourceType uint8

const (
	SourceTypeSyslog SourceType = iota
	SourceTypeHTTPJSON
)

var sourceTypeNames = [...]string{"syslog", "http_json"}

func (s SourceType) String() string {
	if int(s) < len(sourceTypeNames) {
		return sourceTypeNames[s]
	}
	return "unknown"
}

// TimeSource records where Entry.Time came from.
type TimeSource uint8

const (
	// TimeEvent means the event timestamp from the log was used.
	TimeEvent TimeSource = iota
	// TimeReceived means the log had no usable timestamp.
	TimeReceived
	// TimeAdjusted means the event timestamp was outside the accepted skew window.
	TimeAdjusted
)

var timeSourceNames = [...]string{"event", "received", "adjusted"}

func (t TimeSource) String() string {
	if int(t) < len(timeSourceNames) {
		return timeSourceNames[t]
	}
	return "unknown"
}

// SeveritySource records whether severity came from the log or a default.
type SeveritySource uint8

const (
	SeverityFromLog SeveritySource = iota
	SeverityDefault
)
