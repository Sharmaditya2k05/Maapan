package ledger

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

// Account is a chart-of-accounts node with its current balance (debit positive).
type Account struct {
	ID           string     `json:"id"`
	Code         string     `json:"code"`
	Name         string     `json:"name"`
	Group        string     `json:"group"`
	ParentID     *string    `json:"parent_id,omitempty"`
	IsGroup      bool       `json:"is_group"`
	IsSystem     bool       `json:"is_system"`
	IsCash       bool       `json:"is_cash"`
	IsBank       bool       `json:"is_bank"`
	Archived     bool       `json:"archived"`
	BalancePaise int64      `json:"balance_paise"` // debit − credit (including children for groups)
	Children     []*Account `json:"children,omitempty"`
}

const accountCols = `a.id, a.code, a.name, a.grp, a.parent_id, a.is_group, a.is_system, a.is_cash, a.is_bank, a.archived,
	COALESCE((SELECT sum(debit_paise - credit_paise) FROM voucher_entries e WHERE e.tenant_id=a.tenant_id AND e.account_id=a.id),0)`

func scanAccount(row pgx.Row) (*Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Code, &a.Name, &a.Group, &a.ParentID, &a.IsGroup, &a.IsSystem, &a.IsCash, &a.IsBank,
		&a.Archived, &a.BalancePaise)
	return &a, err
}

func (s *Service) listAccounts(ctx context.Context, tenantID string, includeParties bool) ([]*Account, error) {
	if err := s.WithTx(ctx, func(tx pgx.Tx) error { return EnsureChart(ctx, tx, tenantID) }); err != nil {
		return nil, err
	}
	q := `SELECT ` + accountCols + ` FROM accounts a WHERE a.tenant_id=$1`
	if !includeParties {
		q += ` AND NOT EXISTS (SELECT 1 FROM parties p WHERE p.account_id=a.id)`
	}
	rows, err := s.DB.Query(ctx, q+` ORDER BY a.code`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// buildTree nests accounts under parents and rolls balances up into groups.
func buildTree(flat []*Account) []*Account {
	byID := map[string]*Account{}
	for _, a := range flat {
		byID[a.ID] = a
	}
	var roots []*Account
	for _, a := range flat {
		if a.ParentID != nil {
			if p, ok := byID[*a.ParentID]; ok {
				p.Children = append(p.Children, a)
				continue
			}
		}
		roots = append(roots, a)
	}
	var roll func(a *Account) int64
	roll = func(a *Account) int64 {
		for _, c := range a.Children {
			a.BalancePaise += roll(c)
		}
		return a.BalancePaise
	}
	for _, r := range roots {
		roll(r)
	}
	return roots
}

func (s *Service) handleListAccounts(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	flat, err := s.listAccounts(r.Context(), p.TenantID, httpx.Query(r, "include_parties") == "true")
	if err != nil {
		return err
	}
	if httpx.Query(r, "view") == "tree" {
		return httpx.OK(w, map[string]any{"items": buildTree(flat)})
	}
	return httpx.OK(w, map[string]any{"items": flat})
}

type accountInput struct {
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	ParentCode  string         `json:"parent_code"`
	IsGroup     bool           `json:"is_group"`
	IsBank      bool           `json:"is_bank"`
	IsCash      bool           `json:"is_cash"`
	BankDetails map[string]any `json:"bank_details,omitempty"`
}

func (in *accountInput) Validate() error {
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	if in.Code == "" || in.Name == "" || in.ParentCode == "" {
		return httpx.Validation("code, name and parent_code are required")
	}
	if in.IsBank && in.IsCash {
		return httpx.Validation("an account cannot be both bank and cash")
	}
	return nil
}

func (s *Service) handleCreateAccount(w http.ResponseWriter, r *http.Request) error {
	var in accountInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	var id string
	err := s.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := EnsureChart(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		var parentID, grp string
		var parentIsGroup bool
		err := tx.QueryRow(r.Context(), `SELECT id, grp, is_group FROM accounts WHERE tenant_id=$1 AND code=$2`,
			p.TenantID, in.ParentCode).Scan(&parentID, &grp, &parentIsGroup)
		if db.IsNotFound(err) {
			return httpx.Validation("parent_code %s not found", in.ParentCode)
		}
		if err != nil {
			return err
		}
		if !parentIsGroup {
			return httpx.Validation("parent %s is not a group", in.ParentCode)
		}
		return tx.QueryRow(r.Context(), `INSERT INTO accounts (tenant_id, code, name, grp, parent_id, is_group, is_cash, is_bank, bank_details)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
			p.TenantID, in.Code, in.Name, grp, parentID, in.IsGroup, in.IsCash, in.IsBank, in.BankDetails).Scan(&id)
	})
	if db.IsUniqueViolation(err) {
		return httpx.Conflict("account code %s already exists", in.Code)
	}
	if err != nil {
		return err
	}
	a, err := scanAccount(s.DB.QueryRow(r.Context(), `SELECT `+accountCols+` FROM accounts a WHERE a.tenant_id=$1 AND a.id=$2`, p.TenantID, id))
	if err != nil {
		return err
	}
	return httpx.Created(w, a)
}

type accountPatch struct {
	Name        *string        `json:"name"`
	Archived    *bool          `json:"archived"`
	BankDetails map[string]any `json:"bank_details,omitempty"`
}

func (s *Service) handleUpdateAccount(w http.ResponseWriter, r *http.Request) error {
	var in accountPatch
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	id := r.PathValue("id")
	var isSystem bool
	err := s.DB.QueryRow(r.Context(), `SELECT is_system FROM accounts WHERE tenant_id=$1 AND id=$2`, p.TenantID, id).Scan(&isSystem)
	if db.IsNotFound(err) || isBadUUID(err) {
		return httpx.NotFound("account not found")
	}
	if err != nil {
		return err
	}
	if isSystem && in.Archived != nil && *in.Archived {
		return httpx.Validation("system accounts cannot be archived")
	}
	_, err = s.DB.Exec(r.Context(), `UPDATE accounts SET name=COALESCE($3,name), archived=COALESCE($4,archived),
		bank_details=COALESCE($5,bank_details) WHERE tenant_id=$1 AND id=$2`, p.TenantID, id, in.Name, in.Archived, in.BankDetails)
	if err != nil {
		return err
	}
	a, err := scanAccount(s.DB.QueryRow(r.Context(), `SELECT `+accountCols+` FROM accounts a WHERE a.tenant_id=$1 AND a.id=$2`, p.TenantID, id))
	if err != nil {
		return err
	}
	return httpx.OK(w, a)
}

func (s *Service) handleDeleteAccount(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	tag, err := s.DB.Exec(r.Context(), `DELETE FROM accounts a WHERE a.tenant_id=$1 AND a.id=$2 AND NOT a.is_system
		AND NOT EXISTS (SELECT 1 FROM voucher_entries e WHERE e.account_id=a.id)
		AND NOT EXISTS (SELECT 1 FROM accounts c WHERE c.parent_id=a.id)
		AND NOT EXISTS (SELECT 1 FROM parties pt WHERE pt.account_id=a.id)`, p.TenantID, r.PathValue("id"))
	if isBadUUID(err) {
		return httpx.NotFound("account not found")
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.Conflict("account not found, is a system account, or has entries/children — archive it instead")
	}
	return httpx.NoContent(w)
}
