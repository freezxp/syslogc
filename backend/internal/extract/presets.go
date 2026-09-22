package extract

// Preset is a ready-made rule for a log format people commonly have, so a
// working extraction does not have to be typed out as a regular expression.
type Preset struct {
	// ID identifies the preset.
	ID string `json:"id"`
	// Title and Description are shown when choosing one.
	Title       string `json:"title"`
	Description string `json:"description"`
	// Rule is the rule to add, ready to use.
	Rule Config `json:"rule"`
	// Sample is a line the rule matches, for the test panel.
	Sample string `json:"sample"`
}

// dnsdistQueryPattern matches the query lines dnsdist and DNScollector emit:
//
//	2026-09-22T12:51:29.98450571Z dnsdist CLIENT_QUERY - 2001:f40:973::595 3039 INET6 UDP 78b report.appmetrica.yandex.net A -
//
// The client address may be IPv4 or IPv6, and the trailing policy is "-"
// when no rule applied.
const dnsdistQueryPattern = `^(?P<query_time>\S+) dnsdist (?P<event>\S+) \S+ (?P<client_ip>\S+) (?P<client_port>\d+) ` +
	`(?P<address_family>\S+) (?P<transport>\S+) (?P<query_bytes>\S+) (?P<qname>\S+) (?P<qtype>\S+) (?P<policy>\S+)$`

// Presets are the built-in rules, in the order they should be offered.
func Presets() []Preset {
	return []Preset{{
		ID:    "dnsdist",
		Title: "dnsdist / DNScollector queries",
		Description: "Pulls the queried name, client address, transport and query type out of dnsdist " +
			"client-query lines. Required for DNS service trends, which count distinct clients per service.",
		Rule: Config{
			Name:     "dnsdist-query",
			Contains: "dnsdist",
			Regex:    dnsdistQueryPattern,
			Prefix:   "dns.",
		},
		Sample: "2026-09-22T12:51:29.98450571Z dnsdist CLIENT_QUERY - 2001:f40:973::595 3039 INET6 UDP 78b " +
			"report.appmetrica.yandex.net A -",
	}}
}
