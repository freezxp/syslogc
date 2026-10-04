package directory

import (
	"strings"
	"testing"
)

func TestLogonTypesAreSaidInWords(t *testing.T) {
	// The number is what Windows writes; nobody reading a page should have
	// to remember that 10 means somebody connected over remote desktop.
	for code, want := range map[string]string{
		"2": "console", "3": "network", "10": "remote desktop", "5": "service",
	} {
		if got := LogonTypeName(code); got != want {
			t.Errorf("logon type %s = %q, want %q", code, got, want)
		}
	}
	// An unfamiliar one is still shown rather than silently dropped.
	if got := LogonTypeName("13"); got != "type 13" {
		t.Errorf("unknown type = %q", got)
	}
	if got := LogonTypeName(""); got != "" {
		t.Errorf("empty type = %q", got)
	}
}

func TestFailureReasonsExplainTheCode(t *testing.T) {
	if got := FailureReason("0xC0000234"); got != "account locked out" {
		t.Errorf("= %q", got)
	}
	// Case as Windows writes it, and as people paste it.
	if got := FailureReason("0xc000006d"); got != "wrong user name or password" {
		t.Errorf("lower case = %q", got)
	}
	// Windows deliberately reports the same code for a bad password and an
	// unknown account, so the wording must not claim to know which.
	if got := FailureReason("0xC000006D"); strings.Contains(got, "password is wrong") {
		t.Errorf("= %q, want wording that does not distinguish the two cases", got)
	}
	// An unknown code is passed through rather than hidden.
	if got := FailureReason("0xDEADBEEF"); got != "0xDEADBEEF" {
		t.Errorf("unknown code = %q", got)
	}
}

func TestEveryChangeEventIsDescribed(t *testing.T) {
	// The filter and the descriptions come from one map, so a change that is
	// searched for always has words to show beside it.
	for _, id := range ChangeEvents() {
		if changeDescriptions[id] == "" {
			t.Errorf("event %s is read but has no description", id)
		}
	}
	if len(ChangeEvents()) < 10 {
		t.Errorf("only %d change events; the audit view would be thin", len(ChangeEvents()))
	}
	// The ones people ask about by name.
	want := map[string]string{
		EventAccountCreated: "account created",
		EventLockout:        "account locked out",
		EventPasswordReset:  "password reset by an administrator",
		EventGroupAddGlobal: "added to a global group",
	}
	for id, text := range want {
		if changeDescriptions[id] != text {
			t.Errorf("event %s = %q, want %q", id, changeDescriptions[id], text)
		}
	}
}

func TestEventIdsAreTheOnesWindowsUses(t *testing.T) {
	// Pinned because they are the contract with Windows, and a typo here
	// would produce a page that is simply always empty.
	for name, got := range map[string]string{
		"logon":      EventLogonSuccess,
		"failure":    EventLogonFailure,
		"logoff":     EventLogoff,
		"lockout":    EventLockout,
		"privileged": EventPrivilegedLogon,
		"preauth":    EventKerberosPreauthFailed,
	} {
		want := map[string]string{
			"logon": "4624", "failure": "4625", "logoff": "4634",
			"lockout": "4740", "privileged": "4672", "preauth": "4771",
		}[name]
		if got != want {
			t.Errorf("%s event = %s, want %s", name, got, want)
		}
	}
}
