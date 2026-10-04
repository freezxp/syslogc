package sourcetemplate

// nxlogConfig is a complete, working NXLog configuration for a domain
// controller.
//
// It sends the Security channel as JSON inside the syslog message rather than
// as the event's English prose. Windows writes that prose in the server's own
// language, so a rule that reads it works until somebody installs a domain
// controller in another locale; the field names in the JSON do not change.
const nxlogConfig = `## Syslogc — Windows Security log
## Install NXLog Community Edition, then replace nxlog.conf with this and
## restart the nxlog service.

define SYSLOGC_HOST syslog.example.com
define SYSLOGC_PORT 6514

<Extension json>
    Module  xm_json
</Extension>

<Extension syslog>
    Module  xm_syslog
</Extension>

<Input security>
    Module  im_msvistalog
    # The whole Security channel. Audit policy decides what lands in it; see
    # the step below.
    <QueryXML>
        <QueryList>
            <Query Id="0">
                <Select Path="Security">*</Select>
            </Query>
        </QueryList>
    </QueryXML>
</Input>

<Output syslogc>
    Module  om_ssl
    Host    %SYSLOGC_HOST%
    Port    %SYSLOGC_PORT%
    # Set to TRUE only while testing against a self-signed certificate.
    AllowUntrusted FALSE

    Exec    $Message = to_json();
    Exec    $SyslogFacility = 'AUDIT';
    Exec    to_syslog_ietf();
</Output>

<Route security_to_syslogc>
    Path    security => syslogc
</Route>`

// auditPolicy turns on the events the analyses read. Without it a domain
// controller records far less than people expect — success logons in
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

func directoryTemplate() Template {
	return Template{
		ID:    "active-directory",
		Title: "Active Directory (Windows Security log)",
		Description: "Sign-ins, lockouts, privilege use and account changes from a domain controller's " +
			"Security channel, shipped by NXLog as JSON.",
		// The sender emits a JSON object as the message, so the fields are
		// taken from its keys rather than matched out of prose.
		JSON: &JSONExtract{
			Prefix: "ad.",
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
		Analyses: []string{AnalysisDirectory},
		Setup: Setup{
			Sender: "NXLog Community Edition",
			Summary: "NXLog reads the Security channel on each domain controller and sends it here as JSON " +
				"over TLS. Audit policy decides what Windows writes to that channel in the first place, " +
				"so both have to be set.",
			Steps: []Step{
				{
					Title: "Turn on the auditing these analyses read",
					Body: "Run this on every domain controller, elevated. Windows records far less than " +
						"people expect by default — successful sign-ins among them — so the pages stay " +
						"empty until this is done.",
					Language: "batch",
					Config:   auditPolicy,
				},
				{
					Title: "Install NXLog Community Edition on each domain controller",
					Body: "Download it from nxlog.co. The Community Edition is free and reads the Windows " +
						"event log directly; nothing needs installing on this server.",
				},
				{
					Title: "Replace nxlog.conf with this, then restart the NXLog service",
					Body: "Change SYSLOGC_HOST to this server's name. The name has to match its TLS " +
						"certificate or NXLog will refuse to connect, which is the behaviour you want. " +
						"Restart with: Restart-Service nxlog",
					Language: "apache",
					Config:   nxlogConfig,
				},
				{
					Title: "Add the source here, then check the logs arrive",
					Body: "Create a syslog source on port 6514 with this template, enable it, and open the " +
						"Logs page filtered to it. A domain controller is never quiet: if nothing arrives " +
						"within a minute, the connection or the certificate is the thing to look at, not " +
						"the audit policy.",
				},
			},
			Reference: "https://docs.nxlog.co/userguide/integrate/ms-windows-eventlog.html",
		},
	}
}
