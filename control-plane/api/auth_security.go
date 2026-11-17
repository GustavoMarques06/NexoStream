package api

import (
	"net/http"
	"time"

	"nexostream/internal/model"
)

// Defaults of the account lockout.
const (
	defaultLockoutThreshold = 5
	defaultLockoutBase      = 5 * time.Minute
	defaultLockoutMax       = time.Hour
)

// lockDuration returns how long an account stays locked after fails
// consecutive failures: the base time at the threshold, doubling at each
// further multiple of it, up to the maximum. Zero below the threshold.
func (s *Server) lockDuration(fails int) time.Duration {
	th := s.cfg.LockoutThreshold
	if th <= 0 || fails < th {
		return 0
	}
	d := s.cfg.LockoutBase
	for level := fails / th; level > 1 && d < s.cfg.LockoutMax; level-- {
		d *= 2
	}
	return min(d, s.cfg.LockoutMax)
}

// loginFailed records a refused sign-in: log, audit and, for an existing
// account, the failure counter that leads to the lockout.
func (s *Server) loginFailed(r *http.Request, username, ip string, u *model.User) {
	s.log.Warn("login failed", "username", username, "ip", ip)
	s.audit(r, "auth.login_failed", truncate(username, 64), nil)
	if u == nil {
		return
	}
	u.FailedLogins++
	if d := s.lockDuration(u.FailedLogins); d > 0 {
		u.LockedUntil = s.now().UTC().Add(d)
		s.log.Warn("account locked after repeated failures", "username", u.Username, "failures", u.FailedLogins, "for", d.String())
		s.audit(r, "auth.account_locked", u.Username, map[string]any{"failures": u.FailedLogins, "until": u.LockedUntil, "ip": ip})
	}
	if err := s.st.UpdateUser(r.Context(), *u); err != nil {
		s.log.Error("could not record the failed login", "username", u.Username, "error", err)
	}
}
