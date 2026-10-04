package sourcetemplate

import "github.com/freezxp/syslogc/backend/internal/extract"

// dnsQueryPattern matches the query lines dnsdist and DNScollector emit:
//
//	2026-09-22T12:51:29.98450571Z dnsdist CLIENT_QUERY - 2001:f40:973::595 3039 INET6 UDP 78b report.appmetrica.yandex.net A -
//
// The client address may be IPv4 or IPv6, and the trailing policy is "-"
// when no rule applied.
const dnsQueryPattern = `^(?P<query_time>\S+) dnsdist (?P<event>\S+) \S+ (?P<client_ip>\S+) (?P<client_port>\d+) ` +
	`(?P<address_family>\S+) (?P<transport>\S+) (?P<query_bytes>\S+) (?P<qname>\S+) (?P<qtype>\S+) (?P<policy>\S+)$`

func dnsTemplate() Template {
	return Template{
		ID:    "dns-dnsdist",
		Title: "DNS queries (dnsdist / DNScollector)",
		Description: "Client queries from a dnsdist resolver. Produces the queried name and the client " +
			"address, which is what the service trends are counted from.",
		Extract: []extract.Config{{
			Name:     "dnsdist-query",
			Contains: "dnsdist",
			Regex:    dnsQueryPattern,
			Prefix:   "dns.",
		}},
		Fields: []Field{
			{Name: "dns.qname", Description: "The name that was looked up", Example: "www.tiktok.com"},
			{Name: "dns.client_ip", Description: "Who asked", Example: "10.21.4.17"},
			{Name: "dns.qtype", Description: "Record type", Example: "A"},
			{Name: "dns.transport", Description: "UDP or TCP", Example: "UDP"},
			{Name: "dns.event", Description: "What happened", Example: "CLIENT_QUERY"},
			{Name: "dns.address_family", Description: "INET or INET6", Example: "INET6"},
			{Name: "dns.query_bytes", Description: "Size of the query", Example: "78b"},
			{Name: "dns.policy", Description: "The dnsdist rule that applied, or - for none", Example: "-"},
		},
		Analyses: []string{AnalysisDNSServices},
		Setup: Setup{
			Sender:  "dnsdist or DNScollector",
			Summary: "Point the resolver's syslog output at this source. Nothing else is needed: the template's rule reads the line as it arrives.",
			Steps: []Step{{
				Title: "Send dnsdist's client queries to this address over syslog",
				Body: "In DNScollector, add a syslog output whose transport is tcp+tls for port 6514, " +
					"or tcp for the plain port. The address takes the port with it.",
				Language: "yaml",
				Config: `  - name: out
    syslog:
      transport: tcp+tls
      remote-address: syslog.example.com:6514
      mode: text`,
			}},
			Reference: "https://dmachard.github.io/go-dnscollector/",
		},
	}
}
