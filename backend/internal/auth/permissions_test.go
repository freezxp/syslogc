package auth

import "testing"

func TestManagingForwardingImpliesSeeingSources(t *testing.T) {
	// The forwarding editor offers a source filter, which it fills from the
	// sources list. A role that can manage forwarding but not read sources
	// would be shown an empty filter with no explanation.
	for _, role := range []string{RoleAdmin, RoleOperator, RoleViewer} {
		set := rolePermissions[role]
		if set.Has(PermForwardingManage) && !set.Has(PermSourcesRead) {
			t.Errorf("%s can manage forwarding but cannot read sources", role)
		}
	}
	// It is an administrator's decision: it sends the organisation's logs
	// somewhere new.
	if rolePermissions[RoleOperator].Has(PermForwardingManage) {
		t.Error("operators can manage forwarding; that should be an administrator's decision")
	}
	if !rolePermissions[RoleAdmin].Has(PermForwardingManage) {
		t.Error("administrators cannot manage forwarding")
	}
}
