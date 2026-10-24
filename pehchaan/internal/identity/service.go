package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/cache"
	"github.com/taraju/mool/db"
	"github.com/taraju/mool/events"
	"github.com/taraju/mool/httpx"
	"github.com/taraju/mool/svc"
	"github.com/taraju/pehchaan/internal/otp"
	"github.com/taraju/pehchaan/internal/tokens"
)

// OTP policy (ARCHITECTURE.md §3).
const (
	otpTTL            = 5 * time.Minute
	otpPerPhonePerMin = 3
	otpPerIPPer10Min  = 20
	verifyPerIPPer10  = 30
	maxWrongAttempts  = 5
	lockAfterTooMany  = 15 * time.Minute
)

// Service implements identity use cases.
type Service struct {
	DB          *pgxpool.Pool
	Redis       *redis.Client
	Refresh     *tokens.Store
	Soochna     *svc.Client
	Relay       *events.Relay
	Log         *slog.Logger
	JWTSecret   []byte
	Env         string
	DevFixedOTP string // only honoured when Env == "local"
	Now         func() time.Time
}

func otpKey(phone string) string      { return "pehchaan:otp:" + phone }
func attemptsKey(phone string) string { return "pehchaan:otp:att:" + phone }
func lockKey(phone string) string     { return "pehchaan:otp:lock:" + phone }

// OTPRequested is the response of /v1/auth/otp/request.
type OTPRequested struct {
	Phone        string `json:"phone"` // masked
	Channel      string `json:"channel"`
	ExpiresInSec int    `json:"expires_in_sec"`
}

// RequestOTP generates, stores (hashed) and sends an OTP.
func (s *Service) RequestOTP(ctx context.Context, rawPhone, channel, ip string) (*OTPRequested, error) {
	phone, err := otp.NormalizePhone(rawPhone)
	if err != nil {
		return nil, httpx.Validation("%s", err.Error())
	}
	if locked, _ := s.Redis.Exists(ctx, lockKey(phone)).Result(); locked > 0 {
		return nil, httpx.TooMany("too many wrong attempts, try again in 15 minutes")
	}
	if ok, ttl, _ := cache.Allow(ctx, s.Redis, "pehchaan:rl:otp:phone:"+phone, otpPerPhonePerMin, time.Minute); !ok {
		return nil, httpx.TooMany("please wait %d seconds before requesting another OTP", int(ttl.Seconds())+1)
	}
	if ok, _, _ := cache.Allow(ctx, s.Redis, "pehchaan:rl:otp:ip:"+ip, otpPerIPPer10Min, 10*time.Minute); !ok {
		return nil, httpx.TooMany("too many OTP requests from this network")
	}

	code := s.DevFixedOTP
	if s.Env != "local" || code == "" {
		if code, err = otp.Generate(); err != nil {
			return nil, err
		}
	}
	pipe := s.Redis.TxPipeline()
	pipe.Set(ctx, otpKey(phone), otp.Hash(s.JWTSecret, phone, code), otpTTL)
	pipe.Del(ctx, attemptsKey(phone))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, httpx.Unavailable("could not store OTP")
	}
	if s.Env == "local" {
		s.Log.Info("DEV OTP (local only)", "phone", phone, "otp", code)
	}

	msg := map[string]any{
		"channel": channel, "to": phone, "template": "otp",
		"params":          map[string]string{"otp": code, "minutes": strconv.Itoa(int(otpTTL.Minutes()))},
		"idempotency_key": fmt.Sprintf("otp:%s:%d", phone, s.Now().UnixNano()),
	}
	if err := s.Soochna.Post(ctx, "/internal/v1/messages", msg, nil); err != nil {
		if s.Env != "local" {
			return nil, httpx.Unavailable("could not send OTP, please retry")
		}
		s.Log.Warn("soochna unavailable; OTP only logged (local)", "error", err.Error())
	}
	return &OTPRequested{Phone: otp.Mask(phone), Channel: channel, ExpiresInSec: int(otpTTL.Seconds())}, nil
}

// VerifyResult is either a Session or an onboarding hand-off.
type VerifyResult struct {
	*Session
	NeedsOnboarding bool   `json:"needs_onboarding"`
	OnboardingToken string `json:"onboarding_token,omitempty"`
	NewUser         *User  `json:"new_user,omitempty"`
}

// VerifyOTP checks the code and logs the user in.
func (s *Service) VerifyOTP(ctx context.Context, in otpVerifyInput, ip string) (*VerifyResult, error) {
	phone, err := otp.NormalizePhone(in.Phone)
	if err != nil {
		return nil, httpx.Validation("%s", err.Error())
	}
	if !otp.ValidCode(in.OTP) {
		return nil, httpx.Validation("otp must be 6 digits")
	}
	if ok, _, _ := cache.Allow(ctx, s.Redis, "pehchaan:rl:verify:ip:"+ip, verifyPerIPPer10, 10*time.Minute); !ok {
		return nil, httpx.TooMany("too many attempts from this network")
	}
	if locked, _ := s.Redis.Exists(ctx, lockKey(phone)).Result(); locked > 0 {
		return nil, httpx.TooMany("too many wrong attempts, try again in 15 minutes")
	}
	stored, err := s.Redis.Get(ctx, otpKey(phone)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, httpx.Unauthorized("OTP expired or not requested")
	}
	if err != nil {
		return nil, httpx.Unavailable("could not verify OTP")
	}
	if !otp.Equal(stored, otp.Hash(s.JWTSecret, phone, in.OTP)) {
		n, _ := s.Redis.Incr(ctx, attemptsKey(phone)).Result()
		s.Redis.Expire(ctx, attemptsKey(phone), otpTTL)
		if n >= maxWrongAttempts {
			s.Redis.Set(ctx, lockKey(phone), "1", lockAfterTooMany)
			s.Redis.Del(ctx, otpKey(phone), attemptsKey(phone))
			return nil, httpx.TooMany("too many wrong attempts, try again in 15 minutes")
		}
		return nil, httpx.Unauthorized("wrong OTP").WithDetails(map[string]any{"attempts_left": maxWrongAttempts - n})
	}
	s.Redis.Del(ctx, otpKey(phone), attemptsKey(phone))

	user, err := upsertUser(ctx, s.DB, phone, nil)
	if err != nil {
		return nil, err
	}
	if user.Status != "active" {
		return nil, httpx.Forbidden("this account is disabled")
	}
	ms, err := activeMemberships(ctx, s.DB, user.ID)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		tok, err := tokens.IssueOnboarding(s.JWTSecret, user.ID, phone, s.Now())
		if err != nil {
			return nil, err
		}
		return &VerifyResult{NeedsOnboarding: true, OnboardingToken: tok, NewUser: &user}, nil
	}
	m := ms[0]
	if in.TenantID != "" {
		i := slices.IndexFunc(ms, func(x Membership) bool { return x.TenantID == in.TenantID })
		if i < 0 {
			return nil, httpx.Forbidden("you are not a member of that business")
		}
		m = ms[i]
	}
	sess, err := s.issueSession(ctx, user, m, "", "")
	if err != nil {
		return nil, err
	}
	_ = touchLogin(ctx, s.DB, user.ID)
	audit(ctx, s.DB, &m.TenantID, user.ID, "login", user.ID, ip, map[string]any{"method": "otp"})
	return &VerifyResult{Session: sess}, nil
}

// issueSession builds access + refresh tokens for user acting in membership m.
// storeID "" picks the default store. refreshTok "" issues a brand-new family.
func (s *Service) issueSession(ctx context.Context, user User, m Membership, storeID, refreshTok string) (*Session, error) {
	if storeID == "" {
		if len(m.StoreIDs) > 0 {
			storeID = m.StoreIDs[0]
		} else {
			id, err := firstActiveStore(ctx, s.DB, m.TenantID)
			if err != nil && !db.IsNotFound(err) {
				return nil, err
			}
			storeID = id
		}
	} else if auth.StoreScoped(m.Role) && !slices.Contains(m.StoreIDs, storeID) {
		return nil, httpx.Forbidden("no access to that store")
	} else if _, err := getStore(ctx, s.DB, m.TenantID, storeID); err != nil {
		if db.IsNotFound(err) {
			return nil, httpx.NotFound("store not found")
		}
		return nil, err
	}

	now := s.Now()
	claims := auth.Claims{
		TenantID: m.TenantID, StoreID: storeID, Role: m.Role, Stores: m.StoreIDs, Phone: user.Phone,
		RegisteredClaims: auth.RegisteredClaimsFor(user.ID),
	}
	access, err := auth.Issue(s.JWTSecret, claims, now)
	if err != nil {
		return nil, err
	}
	if refreshTok == "" {
		refreshTok, err = s.Refresh.Issue(ctx, tokens.Session{UserID: user.ID, TenantID: m.TenantID, StoreID: storeID})
		if err != nil {
			return nil, httpx.Unavailable("could not create session")
		}
	}
	ms, err := activeMemberships(ctx, s.DB, user.ID)
	if err != nil {
		return nil, err
	}
	return &Session{
		TokenPair: TokenPair{AccessToken: access, RefreshToken: refreshTok, ExpiresAt: now.Add(auth.AccessTTL), TokenType: "Bearer"},
		User:      user, TenantID: m.TenantID, StoreID: storeID, Role: m.Role,
		Permissions: auth.Permissions(m.Role), Memberships: ms,
	}, nil
}

// RefreshSession rotates a refresh token and re-reads the user's current role,
// so role changes and disabled users take effect at the next refresh.
func (s *Service) RefreshSession(ctx context.Context, tok string) (*Session, error) {
	rs, next, err := s.Refresh.Rotate(ctx, tok)
	if errors.Is(err, tokens.ErrReuse) {
		s.Log.Warn("refresh token reuse detected; family revoked")
		return nil, httpx.Unauthorized("session expired, please log in again")
	}
	if err != nil {
		return nil, httpx.Unauthorized("session expired, please log in again")
	}
	user, err := getUser(ctx, s.DB, rs.UserID)
	if err != nil || user.Status != "active" {
		_ = s.Refresh.Revoke(ctx, next)
		return nil, httpx.Unauthorized("account not available")
	}
	m, err := getMembership(ctx, s.DB, rs.TenantID, rs.UserID)
	if err != nil || !m.Active {
		_ = s.Refresh.Revoke(ctx, next)
		return nil, httpx.Unauthorized("access to this business was removed")
	}
	store := rs.StoreID
	if store != "" && auth.StoreScoped(m.Role) && !slices.Contains(m.StoreIDs, store) {
		store = ""
	}
	return s.issueSession(ctx, user, m, store, next)
}

// Logout revokes the refresh token's family.
func (s *Service) Logout(ctx context.Context, tok string) error { return s.Refresh.Revoke(ctx, tok) }

// Switch re-issues tokens for another business/store the user belongs to.
func (s *Service) Switch(ctx context.Context, p *auth.Principal, in switchInput) (*Session, error) {
	user, err := getUser(ctx, s.DB, p.UserID())
	if err != nil {
		return nil, err
	}
	tenant := in.TenantID
	if tenant == "" {
		tenant = p.TenantID
	}
	m, err := getMembership(ctx, s.DB, tenant, user.ID)
	if db.IsNotFound(err) || (err == nil && !m.Active) {
		return nil, httpx.Forbidden("you are not a member of that business")
	}
	if err != nil {
		return nil, err
	}
	return s.issueSession(ctx, user, m, in.StoreID, "")
}

// Onboard creates a business, its first store and the owner's super_admin
// membership. bearer is an onboarding token (new user) or an access token
// (existing user adding another business).
func (s *Service) Onboard(ctx context.Context, bearer string, in OnboardingInput, ip string) (*Session, error) {
	var userID string
	if oc, err := tokens.ParseOnboarding(s.JWTSecret, bearer); err == nil {
		userID = oc.Subject
	} else if c, err := auth.Parse(s.JWTSecret, bearer); err == nil {
		userID = c.Subject
	} else {
		return nil, httpx.Unauthorized("verify your phone first")
	}

	var tenantID, storeID string
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		user, err := getUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET name=COALESCE(name,$2), updated_at=now() WHERE id=$1`, userID, in.OwnerName); err != nil {
			return err
		}
		var pan *string
		if in.GSTIN != "" {
			p := in.GSTIN[2:12]
			pan = &p
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tenants (business_name, gstin, pan, state_code, address, phone)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			in.BusinessName, nilIfEmpty(in.GSTIN), pan, in.StateCode, nilIfEmpty(in.Address), user.Phone).Scan(&tenantID); err != nil {
			return err
		}
		regID, err := insertRegistration(ctx, tx, tenantID, RegistrationInput{GSTIN: in.GSTIN, LegalName: in.BusinessName,
			TradeName: in.BusinessName, StateCode: in.StateCode, Address: in.Address, City: in.City, PIN: in.PIN, Phone: user.Phone},
			true, "active")
		if err != nil {
			return err
		}
		st, err := insertStore(ctx, tx, tenantID, StoreInput{Name: in.StoreName, Code: "MAIN", StateCode: in.StateCode, GSTIN: in.GSTIN,
			Address: in.Address, RegistrationID: regID})
		if err != nil {
			return err
		}
		storeID = st.ID
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role, store_ids) VALUES ($1,$2,'super_admin',$3::uuid[])`,
			tenantID, userID, []string{storeID}); err != nil {
			return err
		}
		ev, err := events.New(events.TenantCreated, "pehchaan", tenantID, storeID, events.TenantCreatedPayload{
			TenantID: tenantID, BusinessName: in.BusinessName, GSTIN: in.GSTIN, StateCode: in.StateCode,
			OwnerUserID: userID, OwnerPhone: user.Phone, StoreID: storeID,
		})
		if err != nil {
			return err
		}
		audit(ctx, tx, &tenantID, userID, "onboarding", tenantID, ip, map[string]any{"business_name": in.BusinessName})
		return events.Enqueue(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	if s.Relay != nil {
		s.Relay.Kick()
	}
	user, err := getUser(ctx, s.DB, userID)
	if err != nil {
		return nil, err
	}
	m, err := getMembership(ctx, s.DB, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return s.issueSession(ctx, user, m, storeID, "")
}

// Me describes the caller.
func (s *Service) Me(ctx context.Context, p *auth.Principal) (map[string]any, error) {
	user, err := getUser(ctx, s.DB, p.UserID())
	if err != nil {
		return nil, err
	}
	ms, err := activeMemberships(ctx, s.DB, user.ID)
	if err != nil {
		return nil, err
	}
	tenant, err := getTenant(ctx, s.DB, p.TenantID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"user": user, "tenant": tenant, "store_id": p.StoreID, "role": p.Claims.Role,
		"stores": p.Claims.Stores, "permissions": auth.Permissions(p.Claims.Role), "memberships": ms,
	}, nil
}

// --- users ---

// Invite adds (or re-activates) a member by phone.
func (s *Service) Invite(ctx context.Context, p *auth.Principal, in InviteInput) (*Member, error) {
	phone, err := otp.NormalizePhone(in.Phone)
	if err != nil {
		return nil, httpx.Validation("%s", err.Error())
	}
	if err := s.checkStores(ctx, p.TenantID, in.StoreIDs); err != nil {
		return nil, err
	}
	if in.StoreIDs == nil {
		in.StoreIDs = []string{}
	}
	var userID string
	err = db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		u, err := upsertUser(ctx, tx, phone, nilIfEmpty(in.Name))
		if err != nil {
			return err
		}
		userID = u.ID
		tag, err := tx.Exec(ctx, `INSERT INTO memberships (tenant_id, user_id, role, store_ids, invited_by)
			VALUES ($1,$2,$3,$4::uuid[],$5) ON CONFLICT (tenant_id, user_id) DO NOTHING`,
			p.TenantID, u.ID, in.Role, in.StoreIDs, p.UserID())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.Conflict("this phone is already a member; update it instead")
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "user.invite", u.ID, "", map[string]any{"role": in.Role})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.member(ctx, p.TenantID, userID)
}

// UpdateMember changes role/stores/active/name of a member.
func (s *Service) UpdateMember(ctx context.Context, p *auth.Principal, userID string, in MemberUpdate) (*Member, error) {
	if userID == p.UserID() && (in.Role != nil || (in.Active != nil && !*in.Active)) {
		return nil, httpx.Forbidden("you cannot change your own role or deactivate yourself")
	}
	if in.StoreIDs != nil {
		if err := s.checkStores(ctx, p.TenantID, in.StoreIDs); err != nil {
			return nil, err
		}
	}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		m, err := getMembership(ctx, tx, p.TenantID, userID)
		if db.IsNotFound(err) {
			return httpx.NotFound("member not found")
		}
		if err != nil {
			return err
		}
		role, stores, active := m.Role, m.StoreIDs, m.Active
		if in.Role != nil {
			role = *in.Role
		}
		if in.StoreIDs != nil {
			stores = in.StoreIDs
		}
		if in.Active != nil {
			active = *in.Active
		}
		if auth.StoreScoped(role) && len(stores) == 0 {
			return httpx.Validation("%s needs at least one store", role)
		}
		if m.Role == auth.RoleSuperAdmin && (role != auth.RoleSuperAdmin || !active) {
			var admins int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE tenant_id=$1 AND role='super_admin' AND active`, p.TenantID).Scan(&admins); err != nil {
				return err
			}
			if admins <= 1 {
				return httpx.Conflict("a business needs at least one active super_admin")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE memberships SET role=$3, store_ids=$4::uuid[], active=$5, updated_at=now()
			WHERE tenant_id=$1 AND user_id=$2`, p.TenantID, userID, role, stores, active); err != nil {
			return err
		}
		if in.Name != nil {
			if _, err := tx.Exec(ctx, `UPDATE users SET name=$2, updated_at=now() WHERE id=$1`, userID, *in.Name); err != nil {
				return err
			}
		}
		audit(ctx, tx, &p.TenantID, p.UserID(), "user.update", userID, "", map[string]any{"role": role, "active": active})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.member(ctx, p.TenantID, userID)
}

func (s *Service) member(ctx context.Context, tenantID, userID string) (*Member, error) {
	ms, err := listMembers(ctx, s.DB, tenantID)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if m.ID == userID {
			return &m, nil
		}
	}
	return nil, httpx.NotFound("member not found")
}

func (s *Service) checkStores(ctx context.Context, tenantID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	n, err := countStoresInTenant(ctx, s.DB, tenantID, ids)
	if err != nil {
		return httpx.Validation("store_ids must be valid UUIDs")
	}
	if n != len(ids) {
		return httpx.Validation("store_ids contains a store outside this business")
	}
	return nil
}

// TenantInfo is the internal tenant shape (ARCHITECTURE.md §6).
type TenantInfo struct {
	ID           string `json:"id"`
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin"`
	StateCode    string `json:"state_code"`
	OwnerPhone   string `json:"owner_phone"`
}

// InternalTenant returns the tenant with its owner phone.
func (s *Service) InternalTenant(ctx context.Context, id string) (*TenantInfo, error) {
	t, err := getTenant(ctx, s.DB, id)
	if db.IsNotFound(err) {
		return nil, httpx.NotFound("tenant not found")
	}
	if err != nil {
		return nil, err
	}
	phone, err := ownerPhone(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	info := &TenantInfo{ID: t.ID, BusinessName: t.BusinessName, StateCode: t.StateCode, OwnerPhone: phone}
	if t.GSTIN != nil {
		info.GSTIN = *t.GSTIN
	}
	return info, nil
}
