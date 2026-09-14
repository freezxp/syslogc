// Package auth implements authentication (passwords, sessions, API keys)
// and role-based authorization. Handlers never check roles; they declare the
// permission a route needs (see docs/security.md §4).
package auth

import "sort"

// Permission names an action.
type Permission string

const (
	PermDashboardView   Permission = "dashboard:view"
	PermLogsSearch      Permission = "logs:search"
	PermLogsTail        Permission = "logs:tail"
	PermLogsViewRaw     Permission = "logs:view_raw"
	PermLogsQueryNative Permission = "logs:query_native"
	PermLogsExport      Permission = "logs:export"
	PermLogsIngest      Permission = "logs:ingest"
	PermSearchesRead    Permission = "searches:read"
	PermSearchesWrite   Permission = "searches:write"
	PermSourcesRead     Permission = "sources:read"
	PermSourcesManage   Permission = "sources:manage"
	PermSystemView      Permission = "system:view"
	PermConfigView      Permission = "config:view"
	PermConfigManage    Permission = "config:manage"
	PermRetentionManage Permission = "retention:manage"
	PermUsersManage     Permission = "users:manage"
	PermAPIKeysOwn      Permission = "apikeys:own"
	PermAPIKeysManage   Permission = "apikeys:manage"
	PermAuditView       Permission = "audit:view"
)

// Roles.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

var viewerPermissions = []Permission{
	PermDashboardView, PermLogsSearch, PermLogsTail, PermLogsViewRaw,
	PermSearchesRead, PermSearchesWrite, PermSourcesRead, PermSystemView,
}

var operatorPermissions = append(append([]Permission{}, viewerPermissions...),
	PermLogsQueryNative, PermLogsExport, PermSourcesManage, PermConfigView, PermAPIKeysOwn,
)

var adminPermissions = append(append([]Permission{}, operatorPermissions...),
	PermConfigManage, PermRetentionManage, PermUsersManage, PermAPIKeysManage, PermAuditView,
)

var rolePermissions = map[string]PermissionSet{
	RoleViewer:   newSet(viewerPermissions),
	RoleOperator: newSet(operatorPermissions),
	RoleAdmin:    newSet(adminPermissions),
}

// apiKeyScopes are the permissions an API key may carry.
var apiKeyScopes = newSet([]Permission{PermLogsIngest, PermLogsSearch, PermLogsTail, PermLogsExport, PermLogsQueryNative, PermSystemView})

// PermissionSet is a set of permissions.
type PermissionSet map[Permission]struct{}

func newSet(ps []Permission) PermissionSet {
	s := make(PermissionSet, len(ps))
	for _, p := range ps {
		s[p] = struct{}{}
	}
	return s
}

// Has reports whether p is in the set.
func (s PermissionSet) Has(p Permission) bool {
	_, ok := s[p]
	return ok
}

// Sorted returns the permissions in a stable order.
func (s PermissionSet) Sorted() []Permission {
	out := make([]Permission, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// RolePermissions returns the permissions of a role (empty for unknown roles).
func RolePermissions(role string) PermissionSet {
	return rolePermissions[role]
}

// ValidRole reports whether role exists.
func ValidRole(role string) bool {
	_, ok := rolePermissions[role]
	return ok
}
