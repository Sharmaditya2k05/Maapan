package creds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/lenden/internal/gateway"
	"github.com/taraju/mool/db"
)

// Store keeps gateway_credentials. It implements gateway.CredentialSource and
// gateway.WebhookSecretSource. Every query filters tenant_id.
type Store struct {
	DB     *pgxpool.Pool
	Cipher *Cipher
}

func aad(tenantID, gw, column string) string { return tenantID + "|" + gw + "|" + column }

// Put stores (or replaces) a tenant's keys for a gateway.
func (s *Store) Put(ctx context.Context, c gateway.Credentials, updatedBy string) error {
	if c.TenantID == "" || c.Gateway == "" || c.KeyID == "" || c.KeySecret == "" {
		return errors.New("tenant, gateway, key_id and key_secret are required")
	}
	secret, err := s.Cipher.Seal(c.KeySecret, aad(c.TenantID, c.Gateway, "key_secret"))
	if err != nil {
		return err
	}
	var webhook []byte
	if c.WebhookSecret != "" {
		if webhook, err = s.Cipher.Seal(c.WebhookSecret, aad(c.TenantID, c.Gateway, "webhook_secret")); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO gateway_credentials
			(tenant_id, gateway, key_id, key_secret_enc, webhook_secret_enc, env, updated_by, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now())
		ON CONFLICT (tenant_id, gateway) DO UPDATE SET key_id=EXCLUDED.key_id, key_secret_enc=EXCLUDED.key_secret_enc,
			webhook_secret_enc=EXCLUDED.webhook_secret_enc, env=EXCLUDED.env, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		c.TenantID, c.Gateway, c.KeyID, secret, webhook, c.Env, updatedBy)
	return err
}

// Delete removes a tenant's keys for a gateway (the platform keys apply again).
func (s *Store) Delete(ctx context.Context, tenantID, gw string) (bool, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM gateway_credentials WHERE tenant_id=$1 AND gateway=$2`, tenantID, gw)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Credentials implements gateway.CredentialSource (secrets decrypted).
func (s *Store) Credentials(ctx context.Context, tenantID, gw string) (gateway.Credentials, bool, error) {
	var (
		c               = gateway.Credentials{TenantID: tenantID, Gateway: gw}
		secret, webhook []byte
	)
	err := s.DB.QueryRow(ctx, `SELECT key_id, key_secret_enc, webhook_secret_enc, env, updated_at
		FROM gateway_credentials WHERE tenant_id=$1 AND gateway=$2`, tenantID, gw).
		Scan(&c.KeyID, &secret, &webhook, &c.Env, &c.UpdatedAt)
	if db.IsNotFound(err) {
		return gateway.Credentials{}, false, nil
	}
	if err != nil {
		return gateway.Credentials{}, false, err
	}
	if c.KeySecret, err = s.Cipher.Open(secret, aad(tenantID, gw, "key_secret")); err != nil {
		return gateway.Credentials{}, false, fmt.Errorf("%s credentials of tenant %s: %w", gw, tenantID, err)
	}
	if len(webhook) > 0 {
		if c.WebhookSecret, err = s.Cipher.Open(webhook, aad(tenantID, gw, "webhook_secret")); err != nil {
			return gateway.Credentials{}, false, fmt.Errorf("%s webhook secret of tenant %s: %w", gw, tenantID, err)
		}
	}
	return c, true, nil
}

// Summary is a tenant's stored gateway with the key id masked (GET /v1/settings/gateways).
type Summary struct {
	Gateway          string    `json:"gateway"`
	KeyIDMasked      string    `json:"key_id_masked"`
	Env              string    `json:"env,omitempty"`
	HasWebhookSecret bool      `json:"has_webhook_secret"`
	UpdatedBy        string    `json:"updated_by,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// List returns a tenant's stored gateways without any secret material.
func (s *Store) List(ctx context.Context, tenantID string) ([]Summary, error) {
	rows, err := s.DB.Query(ctx, `SELECT gateway, key_id, webhook_secret_enc IS NOT NULL, env, updated_by, updated_at
		FROM gateway_credentials WHERE tenant_id=$1 ORDER BY gateway`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var sm Summary
		var keyID string
		if err := rows.Scan(&sm.Gateway, &keyID, &sm.HasWebhookSecret, &sm.Env, &sm.UpdatedBy, &sm.UpdatedAt); err != nil {
			return nil, err
		}
		sm.KeyIDMasked = Mask(keyID)
		out = append(out, sm)
	}
	return out, rows.Err()
}

// WebhookSecret implements gateway.WebhookSecretSource. Gateways that sign
// webhooks with the API secret (Cashfree) store no separate webhook secret,
// so the key secret is returned then.
func (s *Store) WebhookSecret(ctx context.Context, tenantID, gw string) (string, bool, error) {
	c, ok, err := s.Credentials(ctx, tenantID, gw)
	if err != nil || !ok {
		return "", false, err
	}
	if c.WebhookSecret != "" {
		return c.WebhookSecret, true, nil
	}
	return c.KeySecret, true, nil
}

// AllWebhookSecrets implements gateway.WebhookSecretSource.
func (s *Store) AllWebhookSecrets(ctx context.Context, gw string) ([]gateway.TenantSecret, error) {
	rows, err := s.DB.Query(ctx, `SELECT tenant_id FROM gateway_credentials WHERE gateway=$1 ORDER BY updated_at DESC LIMIT 1000`, gw)
	if err != nil {
		return nil, err
	}
	var tenants []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tenants = append(tenants, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]gateway.TenantSecret, 0, len(tenants))
	for _, t := range tenants {
		sec, ok, err := s.WebhookSecret(ctx, t, gw)
		if err != nil {
			return nil, err
		}
		if ok && strings.TrimSpace(sec) != "" {
			out = append(out, gateway.TenantSecret{TenantID: t, Secret: sec})
		}
	}
	return out, nil
}
