package recon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/mool/db"
)

// Import describes a stored 2B statement.
type Import struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	GeneratedOn *string   `json:"generated_on,omitempty"`
	DocCount    int       `json:"doc_count"`
	Skipped     []string  `json:"skipped,omitempty"`
	ImportedBy  string    `json:"imported_by"`
	ImportedAt  time.Time `json:"imported_at"`
}

// Store persists 2B documents and IMS actions.
type Store struct{ DB *pgxpool.Pool }

func nullDate(iso string) any {
	if iso == "" {
		return nil
	}
	return iso
}

// ReplaceImport stores a statement for gstin+period, replacing an earlier import.
// IMS actions are kept: they are keyed by document, not by import.
func (s Store) ReplaceImport(ctx context.Context, tenantID, gstin, period, source, actor string, raw []byte, st Statement) (Import, error) {
	var imp Import
	if !json.Valid(raw) {
		raw, _ = json.Marshal(st)
	}
	skipped, _ := json.Marshal(append([]string{}, st.Skipped...))
	err := db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM gstr2b_docs WHERE tenant_id=$1 AND gstin=$2 AND period=$3`, tenantID, gstin, period); err != nil {
			return err
		}
		var gen *time.Time
		if err := tx.QueryRow(ctx, `INSERT INTO gstr2b_imports (tenant_id, gstin, period, source, generated_on, doc_count, skipped, raw, imported_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (tenant_id, gstin, period) DO UPDATE SET source=EXCLUDED.source, generated_on=EXCLUDED.generated_on,
				doc_count=EXCLUDED.doc_count, skipped=EXCLUDED.skipped, raw=EXCLUDED.raw, imported_by=EXCLUDED.imported_by, imported_at=now()
			RETURNING id, source, generated_on, doc_count, imported_by, imported_at`,
			tenantID, gstin, period, source, nullDate(st.GeneratedOn), len(st.Docs), skipped, raw, actor).
			Scan(&imp.ID, &imp.Source, &gen, &imp.DocCount, &imp.ImportedBy, &imp.ImportedAt); err != nil {
			return err
		}
		if gen != nil {
			g := gen.Format(time.DateOnly)
			imp.GeneratedOn = &g
		}
		imp.Skipped = st.Skipped
		b := &pgx.Batch{}
		for _, d := range st.Docs {
			b.Queue(`INSERT INTO gstr2b_docs (tenant_id, gstin, period, doc_key, section, supplier_gstin, supplier_name, doc_type, doc_no,
				norm_no, doc_date, supply_type, pos, reverse_charge, value, taxable, igst, cgst, sgst, cess, itc_available, reason,
				supplier_period, supplier_filed_on, irn)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
				tenantID, gstin, period, d.Key, d.Section, d.SupplierGSTIN, d.SupplierName, d.DocType, d.DocNo,
				NormaliseDocNo(d.DocNo), nullDate(d.DocDate), d.SupplyType, d.POS, d.ReverseCharge, int64(d.Value), int64(d.Taxable),
				int64(d.IGST), int64(d.CGST), int64(d.SGST), int64(d.Cess), d.ITCAvailable, d.Reason, d.SupplierPeriod,
				nullDate(d.SupplierFiledOn), d.IRN)
		}
		if b.Len() == 0 {
			return nil
		}
		return tx.SendBatch(ctx, b).Close()
	})
	return imp, err
}

// Statement loads the stored import and its documents (nil Import if none).
func (s Store) Statement(ctx context.Context, tenantID, gstin, period string) (*Import, []Doc, error) {
	var imp Import
	var gen *time.Time
	var skipped []byte
	err := s.DB.QueryRow(ctx, `SELECT id, source, generated_on, doc_count, skipped, imported_by, imported_at FROM gstr2b_imports
		WHERE tenant_id=$1 AND gstin=$2 AND period=$3`, tenantID, gstin, period).
		Scan(&imp.ID, &imp.Source, &gen, &imp.DocCount, &skipped, &imp.ImportedBy, &imp.ImportedAt)
	if db.IsNotFound(err) {
		return nil, []Doc{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if gen != nil {
		g := gen.Format(time.DateOnly)
		imp.GeneratedOn = &g
	}
	_ = json.Unmarshal(skipped, &imp.Skipped)
	rows, err := s.DB.Query(ctx, `SELECT doc_key, section, supplier_gstin, supplier_name, doc_type, doc_no, doc_date, supply_type, pos,
		reverse_charge, value, taxable, igst, cgst, sgst, cess, itc_available, reason, supplier_period, supplier_filed_on, irn
		FROM gstr2b_docs WHERE tenant_id=$1 AND gstin=$2 AND period=$3 ORDER BY supplier_gstin, doc_date, doc_no`, tenantID, gstin, period)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	docs := []Doc{}
	for rows.Next() {
		var d Doc
		var dt, filed *time.Time
		var val, tx, ig, cg, sg, cs int64
		if err := rows.Scan(&d.Key, &d.Section, &d.SupplierGSTIN, &d.SupplierName, &d.DocType, &d.DocNo, &dt, &d.SupplyType, &d.POS,
			&d.ReverseCharge, &val, &tx, &ig, &cg, &sg, &cs, &d.ITCAvailable, &d.Reason, &d.SupplierPeriod, &filed, &d.IRN); err != nil {
			return nil, nil, err
		}
		if dt != nil {
			d.DocDate = dt.Format(time.DateOnly)
		}
		if filed != nil {
			d.SupplierFiledOn = filed.Format(time.DateOnly)
		}
		d.Value, d.Taxable, d.IGST, d.CGST, d.SGST, d.Cess = Rs(val), Rs(tx), Rs(ig), Rs(cg), Rs(sg), Rs(cs)
		docs = append(docs, d)
	}
	return &imp, docs, rows.Err()
}

// ReportedElsewhere returns supplierGSTIN|normalised number of documents in
// this GSTIN's 2B statements of other periods.
func (s Store) ReportedElsewhere(ctx context.Context, tenantID, gstin, period string) (map[string]bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT supplier_gstin, norm_no FROM gstr2b_docs WHERE tenant_id=$1 AND gstin=$2 AND period<>$3`,
		tenantID, gstin, period)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var g, n string
		if err := rows.Scan(&g, &n); err != nil {
			return nil, err
		}
		out[g+"|"+n] = true
	}
	return out, rows.Err()
}

// DocExists reports whether gstin has a 2B document with this key in any period.
func (s Store) DocExists(ctx context.Context, tenantID, gstin, key string) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gstr2b_docs WHERE tenant_id=$1 AND gstin=$2 AND doc_key=$3)`,
		tenantID, gstin, key).Scan(&ok)
	return ok, err
}

// SetActions records IMS actions (one transaction, logged).
func (s Store) SetActions(ctx context.Context, tenantID, gstin string, keys []string, action, remark, actor string) error {
	return db.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		for _, k := range keys {
			if _, err := tx.Exec(ctx, `INSERT INTO ims_actions (tenant_id, gstin, doc_key, action, remark, actor) VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (tenant_id, gstin, doc_key) DO UPDATE SET action=EXCLUDED.action, remark=EXCLUDED.remark,
					actor=EXCLUDED.actor, acted_at=now()`, tenantID, gstin, k, action, remark, actor); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO ims_action_log (tenant_id, gstin, doc_key, action, remark, actor) VALUES ($1,$2,$3,$4,$5,$6)`,
				tenantID, gstin, k, action, remark, actor); err != nil {
				return err
			}
		}
		return nil
	})
}

// Actions returns the current IMS action of each document of gstin.
func (s Store) Actions(ctx context.Context, tenantID, gstin string, docs []Doc) (map[string]Action, error) {
	keys := make([]string, 0, len(docs))
	for _, d := range docs {
		keys = append(keys, d.Key)
	}
	out := map[string]Action{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT doc_key, action, remark, actor, acted_at FROM ims_actions
		WHERE tenant_id=$1 AND gstin=$2 AND doc_key = ANY($3)`, tenantID, gstin, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var a Action
		if err := rows.Scan(&k, &a.Action, &a.Remark, &a.Actor, &a.ActedAt); err != nil {
			return nil, err
		}
		out[k] = a
	}
	return out, rows.Err()
}
