package identity

import (
	"net/http"
	"strings"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Routes registers pehchaan's HTTP API.
//
// /v1/auth/* and /v1/onboarding are public on the gateway (onboarding checks its own token).
func (s *Service) Routes(mux *http.ServeMux, v *auth.Verifier) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}

	mux.Handle("POST /v1/auth/otp/request", httpx.H(s.otpRequest))
	mux.Handle("POST /v1/auth/otp/verify", httpx.H(s.otpVerify))
	mux.Handle("POST /v1/auth/refresh", httpx.H(s.refresh))
	mux.Handle("POST /v1/auth/logout", httpx.H(s.logout))
	mux.Handle("POST /v1/auth/switch", user(s.switchTenant))
	mux.Handle("POST /v1/onboarding", httpx.H(s.onboard))

	mux.Handle("GET /v1/me", user(s.me))

	mux.Handle("GET /v1/tenant", user(s.getTenant))
	mux.Handle("PUT /v1/tenant", user(s.putTenant, auth.PermSettingsManage))

	mux.Handle("GET /v1/stores", user(s.listStores))
	mux.Handle("GET /v1/stores/{id}", user(s.getStore))
	mux.Handle("POST /v1/stores", user(s.createStore, auth.PermSettingsManage))
	mux.Handle("PUT /v1/stores/{id}", user(s.updateStore, auth.PermSettingsManage))

	mux.Handle("GET /v1/registrations", user(s.listRegistrations))
	mux.Handle("GET /v1/registrations/{id}", user(s.getRegistration))
	mux.Handle("POST /v1/registrations", user(s.createRegistration, auth.PermSettingsManage))
	mux.Handle("PUT /v1/registrations/{id}", user(s.updateRegistration, auth.PermSettingsManage))
	mux.Handle("DELETE /v1/registrations/{id}", user(s.deleteRegistration, auth.PermSettingsManage))

	mux.Handle("GET /v1/users", user(s.listUsers, auth.PermUserManage))
	mux.Handle("POST /v1/users", user(s.inviteUser, auth.PermUserManage))
	mux.Handle("PATCH /v1/users/{id}", user(s.updateUser, auth.PermUserManage))
	mux.Handle("DELETE /v1/users/{id}", user(s.removeUser, auth.PermUserManage))

	mux.Handle("GET /internal/v1/stores/{id}", v.Internal(httpx.H(s.internalStore)))
	mux.Handle("GET /internal/v1/tenants/{id}", v.Internal(httpx.H(s.internalTenant)))
	mux.Handle("GET /internal/v1/registrations", v.Internal(httpx.H(s.internalRegistrations)))
	mux.Handle("GET /internal/v1/registrations/{id}", v.Internal(httpx.H(s.internalRegistration)))
}

func (s *Service) otpRequest(w http.ResponseWriter, r *http.Request) error {
	var in otpRequestInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.RequestOTP(r.Context(), in.Phone, in.Channel, httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusAccepted, res)
}

func (s *Service) otpVerify(w http.ResponseWriter, r *http.Request) error {
	var in otpVerifyInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	res, err := s.VerifyOTP(r.Context(), in, httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) refresh(w http.ResponseWriter, r *http.Request) error {
	var in refreshInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	sess, err := s.RefreshSession(r.Context(), in.RefreshToken)
	if err != nil {
		return err
	}
	return httpx.OK(w, sess)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) error {
	var in refreshInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := s.Logout(r.Context(), in.RefreshToken); err != nil {
		return httpx.Unavailable("could not log out")
	}
	return httpx.NoContent(w)
}

func (s *Service) switchTenant(w http.ResponseWriter, r *http.Request) error {
	var in switchInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	sess, err := s.Switch(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, sess)
}

func (s *Service) onboard(w http.ResponseWriter, r *http.Request) error {
	var in OnboardingInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	sess, err := s.Onboard(r.Context(), auth.BearerToken(r), in, httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.Created(w, sess)
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) error {
	res, err := s.Me(r.Context(), auth.FromContext(r.Context()))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (s *Service) getTenant(w http.ResponseWriter, r *http.Request) error {
	t, err := getTenant(r.Context(), s.DB, auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return notFound(err, "tenant")
	}
	return httpx.OK(w, t)
}

func (s *Service) putTenant(w http.ResponseWriter, r *http.Request) error {
	var in TenantInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := s.UpdateTenant(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, t)
}

func (s *Service) listStores(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	all, err := listStores(r.Context(), s.DB, p.TenantID)
	if err != nil {
		return err
	}
	out := all[:0]
	for _, st := range all {
		if p.Claims.CanAccessStore(st.ID) {
			out = append(out, st)
		}
	}
	return httpx.OK(w, httpx.List[Store]{Items: out, Total: len(out), Limit: len(out)})
}

func (s *Service) getStore(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if !p.Claims.CanAccessStore(id) {
		return httpx.NotFound("store not found")
	}
	st, err := getStore(r.Context(), s.DB, p.TenantID, id)
	if err != nil {
		return notFound(err, "store")
	}
	return httpx.OK(w, st)
}

func (s *Service) createStore(w http.ResponseWriter, r *http.Request) error {
	var in StoreInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	st, err := s.CreateStore(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, st)
}

func (s *Service) updateStore(w http.ResponseWriter, r *http.Request) error {
	var in StoreInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	st, err := s.UpdateStore(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, st)
}

func (s *Service) listUsers(w http.ResponseWriter, r *http.Request) error {
	ms, err := listMembers(r.Context(), s.DB, auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.List[Member]{Items: ms, Total: len(ms), Limit: len(ms)})
}

func (s *Service) inviteUser(w http.ResponseWriter, r *http.Request) error {
	var in InviteInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	m, err := s.Invite(r.Context(), auth.FromContext(r.Context()), in)
	if err != nil {
		return err
	}
	return httpx.Created(w, m)
}

func (s *Service) updateUser(w http.ResponseWriter, r *http.Request) error {
	var in MemberUpdate
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	m, err := s.UpdateMember(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	return httpx.OK(w, m)
}

func (s *Service) removeUser(w http.ResponseWriter, r *http.Request) error {
	off := false
	if _, err := s.UpdateMember(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), MemberUpdate{Active: &off}); err != nil {
		return err
	}
	return httpx.NoContent(w)
}

// internalStore serves GET /internal/v1/stores/{id} (ARCHITECTURE.md §6,
// PHASE3-CONTRACTS.md §1): the store plus its registration.
func (s *Service) internalStore(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	st, err := s.InternalStore(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if p.TenantID != "" && !strings.EqualFold(p.TenantID, st.TenantID) {
		return httpx.NotFound("store not found")
	}
	return httpx.OK(w, st)
}

func (s *Service) internalTenant(w http.ResponseWriter, r *http.Request) error {
	t, err := s.InternalTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.OK(w, t)
}

func notFound(err error, what string) error {
	if db.IsNotFound(err) {
		return httpx.NotFound("%s not found", what)
	}
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
