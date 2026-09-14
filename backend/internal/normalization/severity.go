package normalization

import (
	"strings"

	"github.com/freezxp/syslogc/backend/internal/logentry"
)

var severityAliases = map[string]logentry.Severity{
	"emerg": logentry.SeverityEmergency, "emergency": logentry.SeverityEmergency, "panic": logentry.SeverityEmergency,
	"alert": logentry.SeverityAlert,
	"crit":  logentry.SeverityCritical, "critical": logentry.SeverityCritical, "fatal": logentry.SeverityCritical,
	"err": logentry.SeverityError, "error": logentry.SeverityError, "eror": logentry.SeverityError,
	"warn": logentry.SeverityWarning, "warning": logentry.SeverityWarning,
	"notice": logentry.SeverityNotice,
	"info":   logentry.SeverityInfo, "information": logentry.SeverityInfo, "informational": logentry.SeverityInfo,
	"debug": logentry.SeverityDebug, "trace": logentry.SeverityDebug, "verbose": logentry.SeverityDebug,
}

// ParseSeverity maps a level string or numeric code (0–7) to a canonical
// severity, case-insensitively. See docs/log-data-model.md §2.2.
func ParseSeverity(s string) (logentry.Severity, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 1 && s[0] >= '0' && s[0] <= '7' {
		return logentry.Severity(s[0] - '0'), true
	}
	if sev, ok := severityAliases[s]; ok {
		return sev, true
	}
	sev, ok := severityAliases[strings.ToLower(s)]
	return sev, ok
}
