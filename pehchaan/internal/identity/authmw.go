package identity

import (
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

// userAuth is mool/auth.Verifier.User with pehchaan's keyring: it accepts
// tokens signed with JWT_SECRET or, during a rotation, JWT_SECRET_PREVIOUS.
func (s *Service) userAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := auth.BearerToken(r)
		if tok == "" {
			httpx.WriteError(w, r, httpx.Unauthorized("missing bearer token"))
			return
		}
		c, err := s.keys().ParseAccess(tok)
		if err != nil {
			httpx.WriteError(w, r, httpx.Unauthorized("invalid or expired token"))
			return
		}
		store := c.StoreID
		if st := r.Header.Get(auth.HeaderStoreID); st != "" {
			if !c.CanAccessStore(st) {
				httpx.WriteError(w, r, httpx.Forbidden("no access to store %s", st))
				return
			}
			store = st
		}
		p := &auth.Principal{Claims: c, TenantID: c.TenantID, StoreID: store}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}
