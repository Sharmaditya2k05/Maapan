package identity

import (
	"net/http"
	"strings"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/httpx"
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

	mux.Handle("GET /v1/users", user(s.listUsers, auth.PermUserManage))
	mux.Handle("POST /v1/users", user(s.inviteUser, auth.PermUserManage))
	mux.Handle("PATCH /v1/users/{id}", user(s.updateUser, auth.PermUserManage))
	mux.Handle("DELETE /v1/users/{id}", user(s.removeUser, auth.PermUserManage))

	mux.Handle("GET /internal/v1/stores/{id}", v.Internal(httpx.H(s.internalStore)))
	mux.Handle("GET /internal/v1/tenants/{id}", v.Internal(httpx.H(s.internalTenant)))
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
	p := auth.FromContext(r.Context())
	var pan *string
	if in.GSTIN != "" {
		x := in.GSTIN[2:12]
		pan = &x
	}
	t, err := scanTenant(s.DB.QueryRow(r.Context(), `UPDATE tenants SET business_name=$2, gstin=$3, pan=COALESCE($4, pan),
		state_code=$5, address=$6, email=$7, phone=$8, updated_at=now() WHERE id=$1 RETURNING `+tenantCols,
		p.TenantID, in.BusinessName, nilIfEmpty(in.GSTIN), pan, in.StateCode, nilIfEmpty(in.Address),
		nilIfEmpty(in.Email), nilIfEmpty(in.Phone)))
	if err != nil {
		return notFound(err, "tenant")
	}
	audit(r.Context(), s.DB, &p.TenantID, p.UserID(), "tenant.update", p.TenantID, httpx.ClientIP(r), nil)
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
	p := auth.FromContext(r.Context())
	st, err := insertStore(r.Context(), s.DB, p.TenantID, in)
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("store code %s already exists", in.Code)
	}
	if err != nil {
		return err
	}
	audit(r.Context(), s.DB, &p.TenantID, p.UserID(), "store.create", st.ID, httpx.ClientIP(r), map[string]any{"code": st.Code})
	return httpx.Created(w, st)
}

func (s *Service) updateStore(w http.ResponseWriter, r *http.Request) error {
	var in StoreInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	st, err := updateStore(r.Context(), s.DB, p.TenantID, r.PathValue("id"), in)
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("store code %s already exists", in.Code)
	}
	if err != nil {
		return notFound(err, "store")
	}
	audit(r.Context(), s.DB, &p.TenantID, p.UserID(), "store.update", st.ID, httpx.ClientIP(r), nil)
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

// internalStore serves GET /internal/v1/stores/{id} (ARCHITECTURE.md §6).
func (s *Service) internalStore(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	st, err := getStoreAnyTenant(r.Context(), s.DB, r.PathValue("id"))
	if err != nil {
		return notFound(err, "store")
	}
	if p.TenantID != "" && !strings.EqualFold(p.TenantID, st.TenantID) {
		return httpx.NotFound("store not found")
	}
	return httpx.OK(w, map[string]any{
		"id": st.ID, "tenant_id": st.TenantID, "name": st.Name, "code": st.Code, "state_code": st.StateCode,
		"gstin": deref(st.GSTIN), "address": deref(st.Address), "phone": deref(st.Phone),
	})
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
