package gsp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

var b64 = base64.RawURLEncoding

// SignJWS returns a compact JWS (HS256) over claims. The real IRP signs with
// RS256 and NIC's private key; the simulator uses a local HMAC key so the
// token has the same shape (header.payload.signature) and can be decoded the
// same way for printing.
func SignJWS(key []byte, claims any) (string, error) {
	header := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := header + "." + b64.EncodeToString(body)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signing))
	return signing + "." + b64.EncodeToString(mac.Sum(nil)), nil
}

// VerifyJWS checks an HS256 compact JWS and returns its payload.
func VerifyJWS(key []byte, token string) (json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("jws: want three parts")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errors.New("jws: bad signature")
	}
	return DecodeJWSPayload(token)
}

// DecodeJWSPayload returns the payload of a compact JWS without verifying it
// (for display, e.g. printing the QR fields of an IRP-signed token).
func DecodeJWSPayload(token string) (json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("jws: want three parts")
	}
	b, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	return b, nil
}

// QRData is the content of the IRP's signed QR code (the "data" claim).
type QRData struct {
	SellerGstin string  `json:"SellerGstin"`
	BuyerGstin  string  `json:"BuyerGstin"`
	DocNo       string  `json:"DocNo"`
	DocTyp      string  `json:"DocTyp"`
	DocDt       string  `json:"DocDt"`
	TotInvVal   float64 `json:"TotInvVal"`
	ItemCnt     int     `json:"ItemCnt"`
	MainHsnCode string  `json:"MainHsnCode"`
	Irn         string  `json:"Irn"`
	IrnDt       string  `json:"IrnDt"`
}

// signedClaims is the IRP token body: {"data": "<json string>", "iss": "..."}.
type signedClaims struct {
	Data string `json:"data"`
	Iss  string `json:"iss"`
}

// ParseSignedQR decodes (without verifying) the QRData of a signed QR token,
// accepting both the IRP shape ({"data":"<json>"}) and a flat payload.
func ParseSignedQR(token string) (QRData, error) {
	raw, err := DecodeJWSPayload(token)
	if err != nil {
		return QRData{}, err
	}
	var c signedClaims
	var q QRData
	if json.Unmarshal(raw, &c) == nil && c.Data != "" {
		err = json.Unmarshal([]byte(c.Data), &q)
		return q, err
	}
	err = json.Unmarshal(raw, &q)
	return q, err
}
