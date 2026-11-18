package util

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// TableCode is the code printed in a table's QR link. It is derived from the
// server secret, so a link cannot be made up for a table by guessing ids: only
// a code from the dashboard (or a photo of the real QR) checks a guest in.
func TableCode(restaurantID, tableID string) string {
	mac := hmac.New(sha256.New, getJWTSecret())
	mac.Write([]byte("table-qr:v1:" + restaurantID + ":" + tableID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:12])
}

// ValidTableCode reports whether code is the code for this table.
func ValidTableCode(restaurantID, tableID, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	return hmac.Equal([]byte(TableCode(restaurantID, tableID)), []byte(code))
}

// PhoneDigits keeps only the digits of a phone number and drops a leading
// country code or trunk zero for Kenya, so "+254 712 345 678", "0712345678"
// and "712345678" compare equal.
func PhoneDigits(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch {
	case strings.HasPrefix(d, "254") && len(d) == 12:
		return d[3:]
	case strings.HasPrefix(d, "0") && len(d) == 10:
		return d[1:]
	}
	return d
}
