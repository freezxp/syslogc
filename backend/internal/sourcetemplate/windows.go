package sourcetemplate

import (
	"sort"
	"strings"

	"github.com/freezxp/syslogc/backend/internal/extract"
)

// Part identifiers. They are stored on a source, so they are stable.
const (
	PartDirectory = "active-directory"
	PartMSSQL     = "mssql"
	PartIIS       = "iis"
)

// markerField is the field each NXLog input stamps on its own records so the
// server can tell them apart.
//
// One NXLog instance on one server sends everything over one connection, so
// the Security log, SQL Server's log and IIS's log all arrive at the same
// source and have to be distinguished after the fact. The alternatives are
// worse: the channel name is absent from IIS records, and matching the
// message text breaks on a server installed in another language. A field the
// sender sets itself is neither.
const markerField = "syslogc_part"

// auditPolicy turns on the events the sign-in analyses read. Without it a
// domain controller records far less than people expect — success logons in
// particular are off by default on member servers.
const auditPolicy = `:: Run on each domain controller, elevated.
:: Sign-in activity
auditpol /set /subcategory:"Logon" /success:enable /failure:enable
auditpol /set /subcategory:"Logoff" /success:enable
auditpol /set /subcategory:"Account Lockout" /success:enable /failure:enable
auditpol /set /subcategory:"Special Logon" /success:enable

:: Kerberos, which is how a domain actually authenticates
auditpol /set /subcategory:"Kerberos Authentication Service" /success:enable /failure:enable
auditpol /set /subcategory:"Kerberos Service Ticket Operations" /success:enable /failure:enable
auditpol /set /subcategory:"Credential Validation" /success:enable /failure:enable

:: Accounts and groups
auditpol /set /subcategory:"User Account Management" /success:enable /failure:enable
auditpol /set /subcategory:"Security Group Management" /success:enable /failure:enable
auditpol /set /subcategory:"Computer Account Management" /success:enable /failure:enable

:: Policy changes
auditpol /set /subcategory:"Audit Policy Change" /success:enable /failure:enable
auditpol /set /subcategory:"Authentication Policy Change" /success:enable /failure:enable`

// mssqlAudit turns on the login auditing SQL Server does not record by
// default. Failed logins are recorded out of the box; successful ones are
// not, and "who got in" is half of every investigation.
const mssqlAudit = `-- Run as sysadmin, then restart the SQL Server service.
-- 3 = both failed and successful logins. 2 is failures only, which is the
-- default on many builds and hides every successful sign-in.
EXEC xp_instance_regwrite
    N'HKEY_LOCAL_MACHINE', N'Software\Microsoft\MSSQLServer\MSSQLServer',
    N'AuditLevel', REG_DWORD, 3;`

// iisFields is the set of W3C fields the IIS analysis reads. IIS writes only
// the fields it is told to, and the parser below is positional, so the two
// have to agree exactly.
const iisFields = `:: Run elevated, then: iisreset
:: Sets the W3C fields the analysis reads, for every site.
%windir%\system32\inetsrv\appcmd set config /section:httpLogging /dontLog:False
%windir%\system32\inetsrv\appcmd set config /section:sites ^
  /siteDefaults.logFile.logFormat:W3C ^
  /siteDefaults.logFile.logExtFileFlags:"Date,Time,ServerIP,Method,UriStem,UriQuery,ServerPort,UserName,ClientIP,UserAgent,Referer,HttpStatus,HttpSubStatus,Win32Status,TimeTaken"`

// nxlogHeader is the top of the generated configuration: where to send, and
// the extensions every part needs.
const nxlogHeader = `## Syslogc — Microsoft Windows Server
## Install NXLog Community Edition, then replace nxlog.conf with this and
## restart the service:  Restart-Service nxlog

define SYSLOGC_HOST syslog.example.com
define SYSLOGC_PORT 6514

<Extension json>
    Module  xm_json
</Extension>

<Extension syslog>
    Module  xm_syslog
</Extension>`

// nxlogOutput sends every selected input to Syslogc as one JSON record per
// event. to_json() runs here rather than in each input so there is one place
// that decides what a message looks like on the wire.
const nxlogOutput = `<Output syslogc>
    Module  om_ssl
    Host    %SYSLOGC_HOST%
    Port    %SYSLOGC_PORT%
    # Set to TRUE only while testing against a self-signed certificate.
    AllowUntrusted FALSE

    Exec    $Message = to_json();
    Exec    $SyslogFacility = 'AUDIT';
    Exec    to_syslog_ietf();
</Output>`

const nxlogInputDirectory = `<Input ad>
    Module  im_msvistalog
    # The whole Security channel. Audit policy decides what lands in it.
    <QueryXML>
        <QueryList>
            <Query Id="0">
                <Select Path="Security">*</Select>
            </Query>
        </QueryList>
    </QueryXML>
    Exec    $` + markerField + ` = 'ad';
</Input>`

// The Application channel is read whole and filtered here rather than in the
// query: the Windows event XPath subset has no starts-with(), so a query
// cannot match a named instance such as MSSQL$SALES without listing it.
const nxlogInputMSSQL = `<Input mssql>
    Module  im_msvistalog
    <QueryXML>
        <QueryList>
            <Query Id="0">
                <Select Path="Application">*</Select>
            </Query>
        </QueryList>
    </QueryXML>
    # Keeps SQL Server's own events, including named instances, and drops the
    # rest of the Application log.
    Exec    if not ($SourceName =~ /^MSSQL/) drop();
    Exec    $` + markerField + ` = 'mssql';
</Input>`

// IIS writes W3C text files, not events, so this is the one part that reads
// from disk. Community Edition has no xm_w3c, so the line is parsed as
// space-delimited CSV with the fields named in the order IIS writes them —
// which is why the setup step above pins that order.
const nxlogInputIIS = `<Extension iis_w3c>
    Module      xm_csv
    Fields      $date, $time, $s_ip, $cs_method, $cs_uri_stem, $cs_uri_query, $s_port, \
                $cs_username, $c_ip, $cs_user_agent, $cs_referer, $sc_status, \
                $sc_substatus, $sc_win32_status, $time_taken
    Delimiter   ' '
    QuoteChar   '"'
    EscapeControl FALSE
</Extension>

<Input iis>
    Module      im_file
    File        'C:\inetpub\logs\LogFiles\W3SVC*\*.log'
    SavePos     TRUE
    # IIS writes #Software, #Fields and so on at the top of every file.
    Exec        if $raw_event =~ /^#/ drop();
    Exec        iis_w3c->parse_csv();
    Exec        delete($raw_event);
    Exec        $` + markerField + ` = 'iis';
</Input>`

// partWhen restricts a rule to the records one input produced.
func partWhen(marker string) []extract.JSONMatch {
	return []extract.JSONMatch{{Keys: []string{markerField}, Equals: []string{marker}}}
}

func directoryPart() Part {
	return Part{
		ID:      PartDirectory,
		Title:   "Active Directory",
		Default: true,
		Description: "Sign-ins, lockouts, privilege use and account changes from the Security " +
			"channel of a domain controller.",
		Analyses: []string{AnalysisDirectory},
		NXLog:    nxlogInputDirectory,
		JSON: &JSONExtract{
			Prefix: "ad.",
			When:   partWhen("ad"),
			Keys: map[string]string{
				"EventID":                   "event_id",
				"TargetUserName":            "user",
				"TargetDomainName":          "domain",
				"SubjectUserName":           "actor",
				"LogonType":                 "logon_type",
				"IpAddress":                 "source_ip",
				"IpPort":                    "source_port",
				"WorkstationName":           "workstation",
				"TargetUserSid":             "user_sid",
				"Status":                    "status",
				"SubStatus":                 "sub_status",
				"FailureReason":             "failure_reason",
				"LogonProcessName":          "logon_process",
				"AuthenticationPackageName": "auth_package",
				"ProcessName":               "process",
				"TargetServerName":          "target_server",
				"ServiceName":               "service",
				"CallerComputerName":        "caller_computer",
				"MemberName":                "member",
				"TicketOptions":             "ticket_options",
				"TicketEncryptionType":      "ticket_encryption",
				"Channel":                   "channel",
				"Hostname":                  "dc",
				"AccountName":               "account_name",
				"AccountType":               "account_type",
			},
		},
		Fields: []Field{
			{Name: "ad.event_id", Description: "Which Windows event this is", Example: "4624"},
			{Name: "ad.user", Description: "The account the event is about", Example: "a.hassan"},
			{Name: "ad.domain", Description: "Its domain", Example: "CORP"},
			{Name: "ad.actor", Description: "The account that caused the event, where different", Example: "SYSTEM"},
			{Name: "ad.logon_type", Description: "How they signed in: 2 console, 3 network, 10 remote desktop", Example: "3"},
			{Name: "ad.source_ip", Description: "Where the attempt came from", Example: "10.20.4.19"},
			{Name: "ad.workstation", Description: "The machine named by the client", Example: "LAPTOP-07"},
			{Name: "ad.caller_computer", Description: "On a lockout, the machine whose attempts caused it", Example: "LAPTOP-07"},
			{Name: "ad.status", Description: "The failure code, on a failed sign-in", Example: "0xC000006D"},
			{Name: "ad.dc", Description: "The domain controller that recorded it", Example: "DC01"},
		},
		Steps: []Step{{
			Title: "Turn on the auditing the sign-in analyses read",
			Body: "Run this on every domain controller, elevated. Windows records far less than " +
				"people expect by default — successful sign-ins among them — so the pages stay " +
				"empty until this is done.",
			Language: "batch",
			Config:   auditPolicy,
		}},
	}
}

func mssqlPart() Part {
	return Part{
		ID:    PartMSSQL,
		Title: "SQL Server",
		Description: "Failed sign-ins and who they were for, deadlocks, backups and the errors " +
			"that precede an outage, from SQL Server's events in the Application channel.",
		Analyses: []string{AnalysisMSSQL},
		NXLog:    nxlogInputMSSQL,
		JSON: &JSONExtract{
			Prefix: "mssql.",
			When:   partWhen("mssql"),
			Keys: map[string]string{
				"EventID":       "event_id",
				"SourceName":    "provider",
				"ProviderName":  "provider",
				"Severity":      "severity",
				"SeverityValue": "severity_value",
				"EventType":     "event_type",
				"Channel":       "channel",
				"Hostname":      "host",
				"Message":       "message",
			},
		},
		// SQL Server puts the account and the address inside the message text
		// rather than in fields of their own, so these two are read out of it.
		// Both are punctuation rather than prose — a quoted name, a bracketed
		// address — which is why they survive a server in another language
		// when the surrounding sentence does not.
		Extract: []extract.Config{
			{
				Name:     "mssql-login-failure",
				Contains: "Login failed for user",
				Regex:    `Login failed for user '(?P<login_user>[^']*)'`,
				Prefix:   "mssql.",
				Additive: true,
			},
			{
				Name:     "mssql-client",
				Contains: "[CLIENT:",
				Regex:    `\[CLIENT: (?P<client_ip>[^\]]+)\]`,
				Prefix:   "mssql.",
				Additive: true,
			},
		},
		Fields: []Field{
			{Name: "mssql.event_id", Description: "Which SQL Server event this is", Example: "18456"},
			{Name: "mssql.provider", Description: "The instance that recorded it", Example: "MSSQLSERVER"},
			{Name: "mssql.severity", Description: "Error, Warning or Information", Example: "ERROR"},
			{Name: "mssql.login_user", Description: "The account a failed sign-in was for", Example: "sa"},
			{Name: "mssql.client_ip", Description: "Where that attempt came from", Example: "10.20.4.19"},
			{Name: "mssql.host", Description: "The server that recorded it", Example: "SQL01"},
			{Name: "mssql.message", Description: "The event text, for the detail the fields do not carry", Example: "Login failed for user 'sa'."},
		},
		Steps: []Step{{
			Title: "Record successful sign-ins as well as failed ones",
			Body: "SQL Server records failed sign-ins out of the box and successful ones only if " +
				"asked. Run this on each instance and restart the SQL Server service. Skip it if " +
				"you only care about failures.",
			Language: "sql",
			Config:   mssqlAudit,
		}},
	}
}

func iisPart() Part {
	return Part{
		ID:    PartIIS,
		Title: "IIS (web requests)",
		Description: "Requests, response codes, the slowest URLs and who is calling them, from " +
			"the W3C log files IIS writes to disk.",
		Analyses: []string{AnalysisIIS},
		NXLog:    nxlogInputIIS,
		JSON: &JSONExtract{
			Prefix: "iis.",
			When:   partWhen("iis"),
			Keys: map[string]string{
				"cs_method":       "method",
				"cs_uri_stem":     "uri",
				"cs_uri_query":    "query",
				"sc_status":       "status",
				"sc_substatus":    "substatus",
				"sc_win32_status": "win32_status",
				"time_taken":      "time_taken",
				"c_ip":            "client_ip",
				"cs_username":     "username",
				"cs_user_agent":   "user_agent",
				"cs_referer":      "referer",
				"s_ip":            "server_ip",
				"s_port":          "port",
			},
		},
		Fields: []Field{
			{Name: "iis.status", Description: "The response code", Example: "404"},
			{Name: "iis.substatus", Description: "IIS's own further reason for it, which is what tells a 401 apart from a 401", Example: "2"},
			{Name: "iis.uri", Description: "The path requested, without the query string", Example: "/app/login"},
			{Name: "iis.method", Description: "The HTTP method", Example: "POST"},
			{Name: "iis.client_ip", Description: "Who asked", Example: "203.0.113.9"},
			{Name: "iis.time_taken", Description: "How long it took, in milliseconds", Example: "1240"},
			{Name: "iis.username", Description: "The authenticated account, where there is one", Example: "CORP\\a.hassan"},
			{Name: "iis.user_agent", Description: "The client that sent it", Example: "Mozilla/5.0"},
			{Name: "iis.server_ip", Description: "Which server answered", Example: "10.20.1.8"},
			{Name: "iis.port", Description: "The port it came in on", Example: "443"},
		},
		Steps: []Step{{
			Title: "Make IIS write the fields this reads",
			Body: "The log line is read by position, so IIS has to write exactly these fields in " +
				"this order. Run elevated, then run iisreset. If your log files are not under " +
				"C:\\inetpub\\logs\\LogFiles, change the File line in the configuration below.",
			Language: "batch",
			Config:   iisFields,
		}},
	}
}

func windowsTemplate() Template {
	return Template{
		ID:    "windows-server",
		Title: "Microsoft Windows Server",
		Description: "Active Directory, SQL Server and IIS from a Windows server, shipped by NXLog " +
			"as JSON. Choose which of them this source carries.",
		Parts: []Part{directoryPart(), mssqlPart(), iisPart()},
		Setup: Setup{
			Sender: "NXLog Community Edition",
			Summary: "NXLog reads the logs you choose and sends them here as JSON over TLS, all on one " +
				"connection. Each part needs something turned on in Windows first — Windows and SQL " +
				"Server both record far less than people expect by default.",
			Steps: []Step{
				{
					Title: "Install NXLog Community Edition on the server",
					Body: "Download it from nxlog.co. The Community Edition is free and reads the Windows " +
						"event log and log files directly; nothing needs installing on this server.",
				},
			},
			Closing: []Step{
				{
					Title: "Add the source here, then check the logs arrive",
					Body: "Create a syslog source on port 6514 with this template and enable it. Its " +
						"protocol must be TLS, with a certificate: the configuration above uses " +
						"om_ssl, so a plain TCP source would leave NXLog waiting for a handshake " +
						"that never comes. Then open the Logs page filtered to it — a Windows server " +
						"is never quiet for long.",
				},
			},
			Reference: "https://docs.nxlog.co/userguide/integrate/ms-windows-eventlog.html",
		},
	}
}

// NXLogConfig builds the configuration file for the chosen parts: the header,
// one input block per part, the output, and a route carrying them all.
func (t Template) NXLogConfig(partIDs []string) string {
	parts := t.SelectedParts(partIDs)
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(nxlogHeader)
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.NXLog == "" {
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(p.NXLog)
		names = append(names, nxlogInputName(p.ID))
	}
	b.WriteString("\n\n")
	b.WriteString(nxlogOutput)
	// One route, so every input shares the single connection to Syslogc.
	b.WriteString("\n\n<Route to_syslogc>\n    Path    ")
	b.WriteString(strings.Join(names, ", "))
	b.WriteString(" => syslogc\n</Route>")
	return b.String()
}

// nxlogInputName is what a part's <Input> block is called.
func nxlogInputName(partID string) string {
	switch partID {
	case PartDirectory:
		return "ad"
	default:
		return partID
	}
}

// SelectedParts returns the parts named by ids, in the template's own order.
// No ids means the parts marked Default, so a source saved before parts
// existed keeps doing exactly what it did.
func (t Template) SelectedParts(ids []string) []Part {
	if len(t.Parts) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.ToLower(strings.TrimSpace(id))] = true
	}
	out := make([]Part, 0, len(t.Parts))
	for _, p := range t.Parts {
		if (len(want) == 0 && p.Default) || want[strings.ToLower(p.ID)] {
			out = append(out, p)
		}
	}
	return out
}

// PartIDs lists every part of this template.
func (t Template) PartIDs() []string {
	out := make([]string, 0, len(t.Parts))
	for _, p := range t.Parts {
		out = append(out, p.ID)
	}
	return out
}

// ValidParts reports whether every id names a part of this template.
func (t Template) ValidParts(ids []string) error {
	for _, id := range ids {
		found := false
		for _, p := range t.Parts {
			if strings.EqualFold(p.ID, id) {
				found = true
				break
			}
		}
		if !found {
			return &UnknownPartError{Template: t.ID, Part: id, Known: t.PartIDs()}
		}
	}
	return nil
}

// UnknownPartError names a part that does not exist, and the ones that do.
type UnknownPartError struct {
	Template string
	Part     string
	Known    []string
}

func (e *UnknownPartError) Error() string {
	if len(e.Known) == 0 {
		return "template " + e.Template + " has no parts to choose from, so " + e.Part + " cannot be one"
	}
	return "unknown part " + e.Part + " for template " + e.Template +
		"; known parts are " + strings.Join(e.Known, ", ")
}

// analysesOf collects the analyses a set of parts unlocks, without duplicates.
func analysesOf(parts []Part) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		for _, a := range p.Analyses {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}
