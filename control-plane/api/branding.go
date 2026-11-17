package api

import "net/http"

// handleBranding returns the panel branding (whitelabel). It is public: the
// login page shows the name, logo, color and message before anyone signs in.
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	st, _ := s.effectiveSettings(r.Context())
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, map[string]string{
		"name":          st.SystemName,
		"tagline":       st.BrandTagline,
		"color":         st.BrandColor,
		"logo":          st.BrandLogo,
		"login_message": st.BrandLoginMessage,
	})
}
