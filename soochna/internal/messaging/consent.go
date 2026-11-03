package messaging

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/soochna/internal/templates"
)

// Consent statuses.
const (
	ConsentOptedOut = "opted_out" // STOP: no customer messages on that channel
	ConsentOptedIn  = "opted_in"  // START / explicit consent
	ConsentDND      = "dnd"       // on the DND list: no promotional messages
)

// ScopeGlobal applies to every tenant.
const ScopeGlobal = "global"

// Consent is one consent record.
type Consent struct {
	Scope     string    `json:"scope"`
	Address   string    `json:"address"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	Note      *string   `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Blocked reports whether a message of category may not go to address.
//
//   - OTP and owner/staff alerts are never blocked.
//   - Transactional (bills, receipts, reminders): blocked by any opt-out for
//     the channel (or "all") in the global or the tenant's scope.
//   - Promotional: additionally blocked by the DND list and, when
//     PromoRequiresOptIn is set, by the absence of an opt-in.
func (s *Service) Blocked(ctx context.Context, tenantID, channel, address string, category templates.Category) (bool, string, error) {
	if category == templates.CategoryOTP || category == templates.CategoryAlert {
		return false, "", nil
	}
	scopes := []string{ScopeGlobal}
	if tenantID != "" {
		scopes = append(scopes, tenantID)
	}
	rows, err := s.DB.Query(ctx, `SELECT scope, status, source FROM consents
		WHERE address=$1 AND channel IN ($2,'all') AND scope = ANY($3)`, address, channel, scopes)
	if err != nil {
		return false, "", err
	}
	defer rows.Close()
	optedIn := false
	var dnd string
	for rows.Next() {
		var scope, status, source string
		if err := rows.Scan(&scope, &status, &source); err != nil {
			return false, "", err
		}
		switch status {
		case ConsentOptedOut:
			who := "the store"
			if scope == ScopeGlobal {
				who = "the recipient"
			}
			return true, "opted out (" + source + ", by " + who + ")", nil
		case ConsentDND:
			dnd = "on the DND list (" + source + ")"
		case ConsentOptedIn:
			optedIn = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, "", err
	}
	if category == templates.CategoryPromotional {
		if dnd != "" {
			return true, dnd, nil
		}
		if s.PromoRequiresOptIn && !optedIn {
			return true, "no opt-in for promotional messages", nil
		}
	}
	return false, "", nil
}

// ConsentInput sets consent for one address.
type ConsentInput struct {
	Address string `json:"address"` // phone or e-mail
	Channel string `json:"channel"` // sms | whatsapp | email | all
	Status  string `json:"status"`  // opted_out | opted_in | dnd
	Note    string `json:"note,omitempty"`
	Source  string `json:"source,omitempty"` // set by the server
}

// Validate normalises and checks the input.
func (in *ConsentInput) Validate() error {
	a, err := NormalizeAddress(in.Address)
	if err != nil {
		return httpx.Validation("address: %s", err.Error())
	}
	in.Address = a
	if in.Channel == "" {
		in.Channel = "all"
	}
	switch in.Channel {
	case templates.SMS, templates.WhatsApp, templates.Email, "all":
	default:
		return httpx.Validation("channel must be sms, whatsapp, email or all")
	}
	switch in.Status {
	case ConsentOptedOut, ConsentOptedIn, ConsentDND:
	default:
		return httpx.Validation("status must be opted_out, opted_in or dnd")
	}
	if len(in.Note) > 300 {
		return httpx.Validation("note too long")
	}
	return nil
}

// SetConsent upserts one consent record and logs the change.
// opted_in removes an earlier dnd/opt-out in the same scope and channel.
func (s *Service) SetConsent(ctx context.Context, scope string, in ConsentInput) error {
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		return setConsent(ctx, tx, scope, in)
	})
}

func setConsent(ctx context.Context, tx pgx.Tx, scope string, in ConsentInput) error {
	if in.Source == "" {
		in.Source = "api"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO consents (scope, address, channel, status, source, note)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (scope, address, channel) DO UPDATE SET status=EXCLUDED.status, source=EXCLUDED.source,
		  note=EXCLUDED.note, updated_at=now()`, scope, in.Address, in.Channel, in.Status, in.Source, nilIfEmpty(in.Note)); err != nil {
		return err
	}
	if in.Status == ConsentOptedIn && in.Channel == "all" {
		// START ALL clears channel-specific opt-outs too.
		if _, err := tx.Exec(ctx, `DELETE FROM consents WHERE scope=$1 AND address=$2 AND channel <> 'all' AND status <> 'opted_in'`,
			scope, in.Address); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO consent_log (scope, address, channel, status, source, detail) VALUES ($1,$2,$3,$4,$5,$6)`,
		scope, in.Address, in.Channel, in.Status, in.Source, nilIfEmpty(in.Note))
	return err
}

// BulkConsentInput imports a DND / opt-out list (internal).
type BulkConsentInput struct {
	Scope     string   `json:"scope,omitempty"` // "global" (default) or a tenant id
	Addresses []string `json:"addresses"`
	Channel   string   `json:"channel,omitempty"` // default all
	Status    string   `json:"status,omitempty"`  // default dnd
	Note      string   `json:"note,omitempty"`
}

// Validate checks the input.
func (in *BulkConsentInput) Validate() error {
	if len(in.Addresses) == 0 || len(in.Addresses) > 10000 {
		return httpx.Validation("addresses: 1 to 10000 entries")
	}
	if in.Scope == "" {
		in.Scope = ScopeGlobal
	}
	if in.Status == "" {
		in.Status = ConsentDND
	}
	return nil
}

// BulkConsent applies one status to many addresses; invalid ones are reported.
func (s *Service) BulkConsent(ctx context.Context, in BulkConsentInput) (map[string]any, error) {
	applied := 0
	invalid := []string{}
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		for _, a := range in.Addresses {
			ci := ConsentInput{Address: a, Channel: in.Channel, Status: in.Status, Note: in.Note}
			if err := ci.Validate(); err != nil {
				invalid = append(invalid, a)
				continue
			}
			ci.Source = "dnd_import"
			if err := setConsent(ctx, tx, in.Scope, ci); err != nil {
				return err
			}
			applied++
		}
		return nil
	})
	return map[string]any{"applied": applied, "invalid": invalid}, err
}

// ListConsents returns consent records for the tenant (its own and global ones).
func (s *Service) ListConsents(ctx context.Context, tenantID, address string, page httpx.Page) ([]Consent, error) {
	args := []any{[]string{ScopeGlobal, tenantID}}
	q := `SELECT scope, address, channel, status, source, note, updated_at FROM consents WHERE scope = ANY($1)`
	if address != "" {
		a, err := NormalizeAddress(address)
		if err != nil {
			return nil, httpx.Validation("address: %s", err.Error())
		}
		args = append(args, a)
		q += ` AND address = $2`
	}
	args = append(args, page.Limit, page.Offset)
	q += ` ORDER BY updated_at DESC LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Consent, error) {
		var c Consent
		err := r.Scan(&c.Scope, &c.Address, &c.Channel, &c.Status, &c.Source, &c.Note, &c.UpdatedAt)
		return c, err
	})
	if out == nil {
		out = []Consent{}
	}
	return out, err
}

// Keyword reads an inbound message: STOP-style words opt out, START-style
// words opt back in. all=true when the sender asked for every channel.
func Keyword(text string) (status string, all bool) {
	t := strings.ToUpper(strings.Join(strings.Fields(strings.Trim(text, " .!\t\r\n")), " "))
	switch t {
	case "STOP", "STOP ALL", "UNSUBSCRIBE", "CANCEL", "END", "QUIT", "OPTOUT", "OPT OUT", "OPT-OUT", "STOP PROMO", "बंद", "रोकें":
		return ConsentOptedOut, t == "STOP ALL"
	case "START", "START ALL", "UNSTOP", "SUBSCRIBE", "OPTIN", "OPT IN", "OPT-IN", "शुरू":
		return ConsentOptedIn, t == "START ALL"
	}
	return "", false
}

// ApplyInbound records a STOP/START keyword received on channel from address
// (global scope: the shared sender number belongs to Maapan, not one store).
// It returns the status applied, or "" when the text is not a keyword.
func (s *Service) ApplyInbound(ctx context.Context, channel, from, text string) (string, error) {
	status, all := Keyword(text)
	if status == "" {
		return "", nil
	}
	addr, err := NormalizePhone(from)
	if err != nil {
		return "", nil // not an Indian mobile: nothing to record
	}
	ch := channel
	if all || status == ConsentOptedIn {
		// STOP stops the channel it arrived on (STOP ALL: every channel);
		// START always restores every channel.
		ch = "all"
	}
	if err := s.SetConsent(ctx, ScopeGlobal, ConsentInput{Address: addr, Channel: ch, Status: status,
		Source: "keyword", Note: truncate(strings.TrimSpace(text), 50)}); err != nil {
		return "", err
	}
	s.Log.Info("consent keyword applied", "channel", ch, "to", maskAddress(addr), "status", status)
	return status, nil
}
