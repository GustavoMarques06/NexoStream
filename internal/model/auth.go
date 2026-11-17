package model

import "time"

// Role is a fixed RBAC role (docs/api.md section 3).
type Role string

const (
	RoleAdmin       Role = "admin"
	RoleOperator    Role = "operator"
	RoleViewer      Role = "viewer"
	RoleIntegration Role = "integration"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleOperator || r == RoleViewer || r == RoleIntegration
}

// Scopes used by routes and integration tokens.
const (
	ScopeChannelsRead    = "channels:read"
	ScopeChannelsWrite   = "channels:write"
	ScopeChannelsOperate = "channels:operate"
	ScopeDVRRead         = "dvr:read"
	ScopeDVRWrite        = "dvr:write"
	ScopeWorkersRead     = "workers:read"
	ScopeWorkersWrite    = "workers:write"
	ScopeAuditRead       = "audit:read"
	ScopeUsersAdmin      = "users:admin"
	ScopeTokensAdmin     = "tokens:admin"
	ScopeSettingsAdmin   = "settings:admin"
)

// AllScopes lists the scopes that can be granted to tokens.
var AllScopes = []string{
	ScopeChannelsRead, ScopeChannelsWrite, ScopeChannelsOperate,
	ScopeDVRRead, ScopeDVRWrite, ScopeWorkersRead, ScopeWorkersWrite, ScopeAuditRead,
}

// RoleScopes maps each user role to the scopes it grants.
func RoleScopes(r Role) []string {
	switch r {
	case RoleAdmin:
		return append(append([]string{}, AllScopes...), ScopeUsersAdmin, ScopeTokensAdmin, ScopeSettingsAdmin)
	case RoleOperator:
		return []string{
			ScopeChannelsRead, ScopeChannelsWrite, ScopeChannelsOperate,
			ScopeDVRRead, ScopeDVRWrite, ScopeWorkersRead, ScopeWorkersWrite,
		}
	case RoleViewer:
		return []string{ScopeChannelsRead, ScopeDVRRead, ScopeWorkersRead}
	default:
		return nil
	}
}

// User is a panel account.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Role         Role      `json:"role"`
	PasswordHash string    `json:"-"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	// TokensValidAfter revokes sessions: access and refresh tokens issued
	// before it are refused. Zero means nothing was revoked.
	TokensValidAfter time.Time `json:"-"`

	// FailedLogins counts consecutive failed sign-ins; reaching the limit
	// locks the account until LockedUntil.
	FailedLogins int       `json:"-"`
	LockedUntil  time.Time `json:"locked_until,omitzero"`
}

// RefreshToken is one issued refresh token. A token is good once: using it
// issues the next token of the same family; using it again revokes the family.
type RefreshToken struct {
	ID        string
	UserID    string
	FamilyID  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// APIToken is an integration token. Only the SHA-256 hash is stored.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"-"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// Active reports whether the token can be used at time now.
func (t APIToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	return t.ExpiresAt == nil || now.Before(*t.ExpiresAt)
}

// AuditEntry records an administrative action. Entries are append-only.
type AuditEntry struct {
	ID        int64          `json:"id"`
	At        time.Time      `json:"at"`
	Actor     string         `json:"actor"`
	ActorType string         `json:"actor_type"` // user | token | system
	Action    string         `json:"action"`
	Target    string         `json:"target"`
	SourceIP  string         `json:"source_ip"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}
