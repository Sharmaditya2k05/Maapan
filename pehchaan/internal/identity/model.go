// Package identity is pehchaan's core: OTP login, sessions, tenants, stores,
// users and memberships.
package identity

import (
	"strings"
	"time"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/gst"
	"github.com/taraju/mool/httpx"
)

// Tenant is a business.
type Tenant struct {
	ID           string    `json:"id"`
	BusinessName string    `json:"business_name"`
	GSTIN        *string   `json:"gstin,omitempty"`
	PAN          *string   `json:"pan,omitempty"`
	StateCode    string    `json:"state_code"`
	Address      *string   `json:"address,omitempty"`
	Email        *string   `json:"email,omitempty"`
	Phone        *string   `json:"phone,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// Store is a location under a tenant.
type Store struct {
	ID        string  `json:"id"`
	TenantID  string  `json:"tenant_id"`
	Name      string  `json:"name"`
	Code      string  `json:"code"`
	StateCode string  `json:"state_code"`
	GSTIN     *string `json:"gstin,omitempty"`
	Address   *string `json:"address,omitempty"`
	Phone     *string `json:"phone,omitempty"`
	Active    bool    `json:"active"`
	// RegistrationID is the GST registration the store bills under. An empty
	// GSTIN/state/address on the store means "the registration's".
	RegistrationID string `json:"registration_id"`
}

// User is a person, identified by phone.
type User struct {
	ID          string     `json:"id"`
	Phone       string     `json:"phone"`
	Name        *string    `json:"name,omitempty"`
	Email       *string    `json:"email,omitempty"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Membership grants a user a role in a tenant.
type Membership struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	BusinessName string   `json:"business_name,omitempty"`
	UserID       string   `json:"user_id"`
	Role         string   `json:"role"`
	StoreIDs     []string `json:"store_ids"`
	Active       bool     `json:"active"`
}

// Member is a user as seen in a tenant's user list.
type Member struct {
	User
	Role     string   `json:"role"`
	StoreIDs []string `json:"store_ids"`
	Active   bool     `json:"active"`
}

// TokenPair is returned by every login-like endpoint.
type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
}

// Session is the full login response.
type Session struct {
	TokenPair
	User        User         `json:"user"`
	TenantID    string       `json:"tenant_id"`
	StoreID     string       `json:"store_id"`
	Role        string       `json:"role"`
	Permissions []string     `json:"permissions"`
	Memberships []Membership `json:"memberships"`
}

// --- inputs ---

type otpRequestInput struct {
	Phone   string `json:"phone"`
	Channel string `json:"channel,omitempty"` // sms (default) | whatsapp
}

func (in *otpRequestInput) Validate() error {
	if in.Channel == "" {
		in.Channel = "sms"
	}
	if in.Channel != "sms" && in.Channel != "whatsapp" {
		return httpx.Validation("channel must be sms or whatsapp")
	}
	return nil
}

type otpVerifyInput struct {
	Phone    string `json:"phone"`
	OTP      string `json:"otp"`
	TenantID string `json:"tenant_id,omitempty"`
}

type refreshInput struct {
	RefreshToken string `json:"refresh_token"`
}

type switchInput struct {
	TenantID string `json:"tenant_id"`
	StoreID  string `json:"store_id,omitempty"`
}

// OnboardingInput creates a business for a verified phone.
type OnboardingInput struct {
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin,omitempty"`
	StateCode    string `json:"state_code"`
	StoreName    string `json:"store_name"`
	OwnerName    string `json:"owner_name"`
	Address      string `json:"address,omitempty"`
	City         string `json:"city,omitempty"` // the IRP rejects e-invoices without city and PIN
	PIN          string `json:"pin,omitempty"`
}

// Validate normalises and checks the onboarding form.
func (in *OnboardingInput) Validate() error {
	in.BusinessName = strings.TrimSpace(in.BusinessName)
	in.OwnerName = strings.TrimSpace(in.OwnerName)
	in.StoreName = strings.TrimSpace(in.StoreName)
	in.GSTIN = gst.Normalize(in.GSTIN)
	if in.BusinessName == "" || in.OwnerName == "" {
		return httpx.Validation("business_name and owner_name are required")
	}
	if in.StoreName == "" {
		in.StoreName = "Main Store"
	}
	in.Address, in.City, in.PIN = strings.TrimSpace(in.Address), strings.TrimSpace(in.City), strings.TrimSpace(in.PIN)
	if err := validPIN(in.PIN); err != nil {
		return err
	}
	if len(in.City) > 50 {
		return httpx.Validation("city must be at most 50 characters")
	}
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
		if in.StateCode == "" {
			in.StateCode = gst.StateCode(in.GSTIN)
		}
		if in.StateCode != gst.StateCode(in.GSTIN) {
			return httpx.Validation("state_code does not match GSTIN state")
		}
	}
	if !gst.ValidState(in.StateCode) {
		return httpx.Validation("state_code must be a valid GST state code")
	}
	return nil
}

// StoreInput creates/updates a store.
type StoreInput struct {
	Name      string `json:"name"`
	Code      string `json:"code"`
	StateCode string `json:"state_code"`
	GSTIN     string `json:"gstin,omitempty"`
	Address   string `json:"address,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Active    *bool  `json:"active,omitempty"`
	// RegistrationID moves the store to another of the tenant's registrations. On
	// create it defaults to the registration of the store's GSTIN, else the primary;
	// on update an empty value keeps the current one.
	RegistrationID string `json:"registration_id,omitempty"`
}

// Validate checks a store. state_code may be empty: it then comes from the
// store's registration (the service fills it in).
func (in *StoreInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	in.GSTIN = gst.Normalize(in.GSTIN)
	if in.Name == "" || in.Code == "" || len(in.Code) > 8 {
		return httpx.Validation("name and code (max 8 chars) are required")
	}
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
		if in.StateCode == "" {
			in.StateCode = gst.StateCode(in.GSTIN)
		}
		if gst.StateCode(in.GSTIN) != in.StateCode {
			return httpx.Validation("state_code does not match GSTIN state")
		}
	}
	if in.StateCode != "" && !gst.ValidState(in.StateCode) {
		return httpx.Validation("state_code must be a valid GST state code")
	}
	in.RegistrationID = strings.TrimSpace(in.RegistrationID)
	return nil
}

// TenantInput updates the business profile.
type TenantInput struct {
	BusinessName string `json:"business_name"`
	GSTIN        string `json:"gstin,omitempty"`
	StateCode    string `json:"state_code"`
	Address      string `json:"address,omitempty"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
}

// Validate checks the profile.
func (in *TenantInput) Validate() error {
	in.BusinessName = strings.TrimSpace(in.BusinessName)
	in.GSTIN = gst.Normalize(in.GSTIN)
	if in.BusinessName == "" {
		return httpx.Validation("business_name is required")
	}
	if in.GSTIN != "" {
		if err := gst.ValidateGSTIN(in.GSTIN); err != nil {
			return httpx.Validation("gstin: %s", err.Error())
		}
		if in.StateCode == "" {
			in.StateCode = gst.StateCode(in.GSTIN)
		}
		if in.StateCode != gst.StateCode(in.GSTIN) {
			return httpx.Validation("state_code does not match GSTIN state")
		}
	}
	if !gst.ValidState(in.StateCode) {
		return httpx.Validation("state_code must be a valid GST state code")
	}
	return nil
}

// InviteInput adds a user to the tenant.
type InviteInput struct {
	Phone    string   `json:"phone"`
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	StoreIDs []string `json:"store_ids"`
}

// Validate checks an invite.
func (in *InviteInput) Validate() error {
	if !auth.ValidRole(in.Role) {
		return httpx.Validation("role must be super_admin, store_manager, cashier or accountant")
	}
	if auth.StoreScoped(in.Role) && len(in.StoreIDs) == 0 {
		return httpx.Validation("%s needs at least one store in store_ids", in.Role)
	}
	return nil
}

// MemberUpdate changes a member.
type MemberUpdate struct {
	Name     *string  `json:"name,omitempty"`
	Role     *string  `json:"role,omitempty"`
	StoreIDs []string `json:"store_ids,omitempty"`
	Active   *bool    `json:"active,omitempty"`
}

// Validate checks an update.
func (in *MemberUpdate) Validate() error {
	if in.Role != nil && !auth.ValidRole(*in.Role) {
		return httpx.Validation("invalid role")
	}
	return nil
}
