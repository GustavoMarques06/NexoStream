package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"nexostream/control-plane/store"
	"nexostream/internal/model"
)

// TokenPrefix marks integration tokens.
const TokenPrefix = "nxs_"

// Principal is the authenticated caller.
type Principal struct {
	Actor     string     `json:"actor"`
	ActorType string     `json:"actor_type"` // user | token
	UserID    string     `json:"user_id,omitempty"`
	Role      model.Role `json:"role"`
	Scopes    []string   `json:"scopes"`
}

// Has reports whether the principal holds a scope ("" = any authenticated).
func (p *Principal) Has(scope string) bool {
	if scope == "" {
		return true
	}
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(keyPrincipal).(*Principal)
	return p
}

type claims struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Typ  string `json:"typ"` // access | refresh
	// Fam is the family of a refresh token: every token obtained by renewing
	// the same sign-in shares it (see handleRefresh).
	Fam string `json:"fam,omitempty"`
	jwt.RegisteredClaims
}

// issue signs a token and returns it with its id (the refresh token id is what
// the store tracks).
func (s *Server) issue(u model.User, typ, family string, ttl time.Duration) (token, id string, err error) {
	now := s.now()
	id = newID(16)
	c := claims{Name: u.Username, Role: string(u.Role), Typ: typ, Fam: family, RegisteredClaims: jwt.RegisteredClaims{
		Subject: u.ID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		Issuer: "nexostream", ID: id,
	}}
	token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.cfg.JWTKey)
	return token, id, err
}

func (s *Server) parse(tok, typ string) (*claims, error) {
	c := &claims{}
	_, err := jwt.ParseWithClaims(tok, c, func(t *jwt.Token) (any, error) { return s.cfg.JWTKey, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("nexostream"), jwt.WithTimeFunc(s.now))
	if err != nil {
		return nil, err
	}
	if c.Typ != typ {
		return nil, errors.New("wrong token type")
	}
	return c, nil
}

// revoked reports whether the token was issued before the user's sessions
// were revoked. JWT times have one-second precision, so the instant is
// compared truncated to the second.
func revoked(u model.User, c *claims) bool {
	if u.TokensValidAfter.IsZero() || c.IssuedAt == nil {
		return false
	}
	return c.IssuedAt.Before(u.TokensValidAfter.Truncate(time.Second))
}

// HashToken hashes an integration token for storage.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// HashPassword hashes a password with bcrypt.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// a constant hash used to spend the same time when the user does not exist.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("nexostream-dummy"), bcrypt.DefaultCost)

// authenticate resolves the Authorization header.
func (s *Server) authenticate(r *http.Request) (*Principal, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return nil, errors.New("missing bearer token")
	}
	ctx := r.Context()
	if strings.HasPrefix(tok, TokenPrefix) {
		t, err := s.st.GetAPITokenByHash(ctx, HashToken(tok))
		if err != nil || !t.Active(s.now()) {
			return nil, errors.New("invalid or expired token")
		}
		s.touchToken(t.ID)
		return &Principal{Actor: "token:" + t.Name, ActorType: "token", Role: model.RoleIntegration, Scopes: t.Scopes}, nil
	}
	c, err := s.parse(tok, "access")
	if err != nil {
		return nil, err
	}
	u, err := s.st.GetUser(ctx, c.Subject)
	if err != nil || u.Disabled {
		return nil, errors.New("user not found or disabled")
	}
	if revoked(u, c) {
		return nil, errors.New("session revoked")
	}
	return &Principal{Actor: u.Username, ActorType: "user", UserID: u.ID, Role: u.Role, Scopes: model.RoleScopes(u.Role)}, nil
}

func (s *Server) touchToken(id string) {
	now := s.now()
	if v, ok := s.touch.Load(id); ok && now.Sub(v.(time.Time)) < time.Minute {
		return
	}
	s.touch.Store(id, now)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.st.TouchAPIToken(ctx, id, now.UTC())
	}()
}

// authed protects a handler with authentication and a scope.
func (s *Server) authed(scope string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.authenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nexostream"`)
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "Autenticação ausente, inválida ou expirada.")
			return
		}
		if !p.Has(scope) {
			writeError(w, r, http.StatusForbidden, "forbidden", "Sem permissão para esta ação (escopo "+scope+").")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyPrincipal, p)))
	})
}

// audit records an administrative action (append-only).
func (s *Server) audit(r *http.Request, action, target string, details map[string]any) {
	p := principalFrom(r.Context())
	e := model.AuditEntry{At: s.now().UTC(), Action: action, Target: target, SourceIP: s.clientIP(r),
		RequestID: requestIDFrom(r.Context()), Details: details, ActorType: "system", Actor: "system"}
	if p != nil {
		e.Actor, e.ActorType = p.Actor, p.ActorType
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	if err := s.st.InsertAudit(ctx, e); err != nil {
		s.log.Error("audit insert failed", "action", action, "target", target, "error", err)
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// tokens issues an access token and a refresh token. The refresh token belongs
// to family (a new one when empty) and is recorded, so it can be used once.
func (s *Server) tokens(ctx context.Context, u model.User, family string) (tokenResponse, error) {
	st, _ := s.effectiveSettings(ctx)
	access := time.Duration(st.AccessTokenMinutes) * time.Minute
	refresh := time.Duration(st.RefreshTokenHours) * time.Hour
	at, _, err := s.issue(u, "access", "", access)
	if err != nil {
		return tokenResponse{}, err
	}
	if family == "" {
		family = newID(16)
	}
	rt, rid, err := s.issue(u, "refresh", family, refresh)
	if err != nil {
		return tokenResponse{}, err
	}
	now := s.now().UTC()
	if err := s.st.CreateRefreshToken(ctx, model.RefreshToken{ID: rid, UserID: u.ID, FamilyID: family, CreatedAt: now, ExpiresAt: now.Add(refresh)}); err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{AccessToken: at, TokenType: "Bearer", ExpiresIn: int(access.Seconds()), RefreshToken: rt}, nil
}

// genericLogin is shown for every refusal that could reveal whether an account
// exists, is disabled or is locked.
const genericLogin = "Usuário ou senha inválidos, ou conta temporariamente bloqueada."

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	perIP, perUser := s.loginLimits()
	if !s.allowLogin(r, bucketLoginIP, ip, perIP) {
		w.Header().Set("Retry-After", "60")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Muitas tentativas de login. Tente novamente em instantes.")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// A second bucket per user name stops a botnet from guessing one
	// password list against one account from many addresses.
	if !s.allowLogin(r, bucketLoginUser, strings.ToLower(strings.TrimSpace(req.Username)), perUser) {
		s.log.Warn("login throttled for user", "username", req.Username, "ip", ip)
		w.Header().Set("Retry-After", "60")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Muitas tentativas de login. Tente novamente em instantes.")
		return
	}
	u, err := s.st.GetUserByUsername(r.Context(), req.Username)
	found := err == nil
	hash := dummyHash // the same work whether or not the user exists
	if found {
		hash = []byte(u.PasswordHash)
	}
	pwErr := bcrypt.CompareHashAndPassword(hash, []byte(req.Password))
	now := s.now()
	switch {
	case !found || u.Disabled:
		s.loginFailed(r, req.Username, ip, nil)
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", genericLogin)
		return
	case u.LockedUntil.After(now):
		// Attempts on a locked account are refused without counting: the
		// lock must not be extendable by whoever is guessing.
		s.log.Warn("login on a locked account", "username", req.Username, "ip", ip)
		s.audit(r, "auth.login_locked", truncate(req.Username, 64), map[string]any{"until": u.LockedUntil})
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", genericLogin)
		return
	case pwErr != nil:
		s.loginFailed(r, req.Username, ip, &u)
		writeError(w, r, http.StatusUnauthorized, "invalid_credentials", genericLogin)
		return
	}
	// Success: forget the failures.
	u.FailedLogins, u.LockedUntil = 0, time.Time{}
	if err := s.st.UpdateUser(r.Context(), u); err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	resp, err := s.tokens(r.Context(), u, "")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Erro ao emitir token.")
		return
	}
	ctx := context.WithValue(r.Context(), keyPrincipal, &Principal{Actor: u.Username, ActorType: "user"})
	s.audit(r.WithContext(ctx), "auth.login", u.Username, nil)
	writeJSON(w, http.StatusOK, resp)
}

// handleRefresh renews a sign-in. A refresh token works once: it is replaced by
// a new one of the same family. Presenting a token that was already used means
// two parties hold it (it was stolen), so the whole family is revoked and both
// must sign in again.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	invalid := func() {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Token de renovação inválido ou expirado.")
	}
	c, err := s.parse(req.RefreshToken, "refresh")
	if err != nil || c.Fam == "" || c.ID == "" { // tokens from before rotation existed have no family
		invalid()
		return
	}
	rt, reused, err := s.st.UseRefreshToken(r.Context(), c.ID, s.now().UTC())
	switch {
	case errors.Is(err, store.ErrNotFound):
		invalid()
		return
	case err != nil:
		s.storeError(w, r, err, "token")
		return
	case rt.FamilyID != c.Fam || rt.UserID != c.Subject:
		invalid()
		return
	case reused:
		_ = s.st.RevokeRefreshFamily(r.Context(), rt.FamilyID)
		actor := c.Name
		if actor == "" {
			actor = rt.UserID
		}
		s.log.Warn("refresh token reused: session family revoked", "user", actor, "ip", s.clientIP(r))
		ctx := context.WithValue(r.Context(), keyPrincipal, &Principal{Actor: actor, ActorType: "user"})
		s.audit(r.WithContext(ctx), "auth.refresh_reuse", actor, map[string]any{"family": rt.FamilyID})
		writeError(w, r, http.StatusUnauthorized, "token_reused", "Sessão encerrada por segurança: o token de renovação foi usado mais de uma vez. Entre novamente.")
		return
	}
	u, err := s.st.GetUser(r.Context(), c.Subject)
	if err != nil || u.Disabled || revoked(u, c) {
		_ = s.st.RevokeRefreshFamily(r.Context(), rt.FamilyID)
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Sessão encerrada, usuário inexistente ou desativado.")
		return
	}
	resp, err := s.tokens(r.Context(), u, rt.FamilyID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Erro ao emitir token.")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLogout ends every session of the caller: tokens issued so far stop
// working, including the refresh tokens.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.ActorType != "user" {
		writeError(w, r, http.StatusUnprocessableEntity, "not_a_user", "Tokens de integração não têm sessão; revogue o token na página Tokens.")
		return
	}
	u, err := s.st.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	u.TokensValidAfter = s.now().UTC()
	if err := s.st.UpdateUser(r.Context(), u); err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	_ = s.st.RevokeUserRefreshTokens(r.Context(), u.ID)
	s.audit(r, "auth.logout", u.Username, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principalFrom(r.Context()))
}

// ---- users ----

// commonPasswords are rejected even when long enough.
var commonPasswords = []string{"password", "senha", "123456", "qwerty", "admin", "nexostream", "iptv", "letmein", "abc123"}

// validPassword enforces a minimum length and rejects trivially guessable
// passwords (one repeated character, a keyboard run, a common word).
func validPassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("a senha deve ter pelo menos 10 caracteres")
	}
	if len(pw) > 72 { // bcrypt ignores anything after 72 bytes
		return errors.New("a senha deve ter no máximo 72 caracteres")
	}
	distinct := map[rune]bool{}
	for _, c := range pw {
		distinct[c] = true
	}
	if len(distinct) < 5 {
		return errors.New("a senha é muito repetitiva: use pelo menos 5 caracteres diferentes")
	}
	low := strings.ToLower(pw)
	for _, c := range commonPasswords {
		// Reject when a common word makes up most of the password.
		if strings.Contains(low, c) && len(c)*2 >= len(low) {
			return errors.New("a senha é fácil de adivinhar: evite palavras comuns")
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.st.ListUsers(r.Context())
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": us, "next_cursor": nil})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string     `json:"username"`
		Password string     `json:"password"`
		Role     model.Role `json:"role"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !validName.MatchString(req.Username) {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_username", "Usuário deve ter de 1 a 64 caracteres [a-z0-9._-].")
		return
	}
	if !req.Role.Valid() || req.Role == model.RoleIntegration {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_role", "Perfil deve ser admin, operator ou viewer.")
		return
	}
	if err := validPassword(req.Password); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Erro ao gerar hash da senha.")
		return
	}
	now := s.now().UTC()
	u := model.User{ID: newID(8), Username: req.Username, Role: req.Role, PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.st.CreateUser(r.Context(), u); err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	s.audit(r, "user.create", u.Username, map[string]any{"role": u.Role})
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role     *model.Role `json:"role"`
		Password *string     `json:"password"`
		Disabled *bool       `json:"disabled"`
		// Unlock clears the lockout of the account.
		Unlock *bool `json:"unlock"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := s.st.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	changes := map[string]any{}
	if req.Role != nil {
		if !req.Role.Valid() || *req.Role == model.RoleIntegration {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_role", "Perfil deve ser admin, operator ou viewer.")
			return
		}
		u.Role = *req.Role
		changes["role"] = u.Role
	}
	if req.Password != nil {
		if err := validPassword(*req.Password); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "weak_password", err.Error())
			return
		}
		if u.PasswordHash, err = HashPassword(*req.Password); err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Erro ao gerar hash da senha.")
			return
		}
		changes["password"] = "changed"
	}
	if req.Disabled != nil {
		if p := principalFrom(r.Context()); p != nil && p.UserID == u.ID && *req.Disabled {
			writeError(w, r, http.StatusUnprocessableEntity, "cannot_disable_self", "Não é possível desativar o próprio usuário.")
			return
		}
		u.Disabled = *req.Disabled
		changes["disabled"] = u.Disabled
	}
	if req.Unlock != nil && *req.Unlock {
		u.FailedLogins, u.LockedUntil = 0, time.Time{}
		changes["unlocked"] = true
	}
	u.UpdatedAt = s.now().UTC()
	endSessions := req.Password != nil || req.Role != nil
	if endSessions {
		u.TokensValidAfter = u.UpdatedAt // a changed password or role ends the old sessions
	}
	if err := s.st.UpdateUser(r.Context(), u); err != nil {
		s.storeError(w, r, err, "user")
		return
	}
	if endSessions {
		_ = s.st.RevokeUserRefreshTokens(r.Context(), u.ID)
	}
	s.audit(r, "user.update", u.Username, changes)
	writeJSON(w, http.StatusOK, u)
}

// ---- integration tokens ----

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := s.st.ListAPITokens(r.Context())
	if err != nil {
		s.storeError(w, r, err, "token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ts, "next_cursor": nil})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expires_in_days"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !validName.MatchString(req.Name) {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_name", "Nome deve ter de 1 a 64 caracteres [a-z0-9._-].")
		return
	}
	if len(req.Scopes) == 0 {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_scopes", "Informe ao menos um escopo.")
		return
	}
	for _, sc := range req.Scopes {
		ok := false
		for _, a := range model.AllScopes {
			ok = ok || a == sc
		}
		if !ok {
			writeErrorDetails(w, r, http.StatusUnprocessableEntity, "invalid_scopes", "Escopo desconhecido: "+sc, model.AllScopes)
			return
		}
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_expiry", "expires_in_days deve estar entre 1 e 3650 (0 = 365).")
		return
	}
	if req.ExpiresInDays == 0 {
		req.ExpiresInDays = 365
	}
	secret := TokenPrefix + newID(24)
	now := s.now().UTC()
	exp := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
	t := model.APIToken{ID: newID(8), Name: req.Name, Prefix: secret[:len(TokenPrefix)+6], Hash: HashToken(secret),
		Scopes: req.Scopes, CreatedBy: principalFrom(r.Context()).Actor, CreatedAt: now, ExpiresAt: &exp}
	if err := s.st.CreateAPIToken(r.Context(), t); err != nil {
		s.storeError(w, r, err, "token")
		return
	}
	s.audit(r, "api_token.create", t.Name, map[string]any{"scopes": t.Scopes, "expires_at": exp})
	// The secret is shown once; only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "api_token": t})
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if err := s.st.RevokeAPIToken(r.Context(), r.PathValue("id"), s.now().UTC()); err != nil {
		s.storeError(w, r, err, "token")
		return
	}
	s.audit(r, "api_token.revoke", r.PathValue("id"), nil)
	w.WriteHeader(http.StatusNoContent)
}

// EnsureBootstrapAdmin creates the first admin when there are no users.
// The password must come from a secret (env file), never from the code.
func EnsureBootstrapAdmin(ctx context.Context, st interface {
	store.UserStore
	store.AuditStore
}, username, password string) (bool, error) {
	n, err := st.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if password == "" {
		return false, errors.New("no users exist: set NEXOSTREAM_BOOTSTRAP_ADMIN_PASSWORD_FILE to create the first admin")
	}
	if err := validPassword(password); err != nil {
		return false, fmt.Errorf("bootstrap admin password: %w", err)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if username == "" {
		username = "admin"
	}
	err = st.CreateUser(ctx, model.User{ID: newID(8), Username: username, Role: model.RoleAdmin, PasswordHash: hash, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return false, err
	}
	_ = st.InsertAudit(ctx, model.AuditEntry{At: now, Actor: "system", ActorType: "system", Action: "user.bootstrap", Target: username})
	return true, nil
}
