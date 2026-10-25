package events

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// HandlerFunc processes one event. Return an error to have Pub/Sub retry it.
type HandlerFunc func(ctx context.Context, e Envelope) error

// Router dispatches envelopes by type.
type Router map[string]HandlerFunc

// pushBody is what a Pub/Sub push subscription POSTs.
type pushBody struct {
	Message struct {
		Data      string `json:"data"`
		MessageID string `json:"messageId"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// Handler serves POST /events. It accepts both Pub/Sub push bodies and raw
// envelopes (direct driver). Callers must present X-Service-Secret, unless
// trustIAM is true — set it on Cloud Run where the service requires IAM auth,
// so only the Pub/Sub push service account can reach this endpoint at all.
//
// Unknown event types are acknowledged (204) so they are not redelivered.
func Handler(v *auth.Verifier, trustIAM bool, routes Router) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trustIAM && !v.IsInternal(r) {
			httpx.WriteError(w, r, httpx.Unauthorized("events endpoint"))
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("read body"))
			return
		}
		env, err := parse(raw)
		if err != nil {
			// Malformed messages can never succeed; ack so they don't loop forever.
			logx.From(r.Context()).Error("dropping malformed event", "error", err.Error())
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h, ok := routes[env.Type]
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ctx := auth.WithPrincipal(r.Context(), &auth.Principal{Internal: true, TenantID: env.TenantID, StoreID: env.StoreID})
		log := logx.From(ctx).With("event_id", env.ID, "event_type", env.Type, "tenant_id", env.TenantID)
		if err := h(logx.With(ctx, log), env); err != nil {
			log.Error("event handler failed, will retry", "error", err.Error())
			httpx.WriteError(w, r, httpx.Internal("event handler failed"))
			return
		}
		log.Info("event processed")
		w.WriteHeader(http.StatusNoContent)
	})
}

func parse(raw []byte) (Envelope, error) {
	var pb pushBody
	if err := json.Unmarshal(raw, &pb); err == nil && pb.Message.Data != "" {
		data, err := base64.StdEncoding.DecodeString(pb.Message.Data)
		if err != nil {
			return Envelope{}, err
		}
		raw = data
	}
	var e Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return Envelope{}, err
	}
	if e.ID == "" || e.Type == "" {
		return Envelope{}, io.ErrUnexpectedEOF
	}
	return e, nil
}
