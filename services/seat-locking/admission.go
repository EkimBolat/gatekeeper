package main

import (
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// admissionClaims mirrors the claims the Waiting Room Service signs into
// its admission tokens (see services/waiting-room/token.go), the same
// shape the API Gateway already verifies (see services/api-gateway/auth.go).
type admissionClaims struct {
	EventID string `json:"eventId"`
	jwt.RegisteredClaims
}

// hasValidAdmission verifies the request's admission JWT: signature,
// expiry, and that it was issued for exactly this event/user.
//
// The gateway already checks this before proxying a lock request here --
// but this service also has its own public URL, so relying on the
// gateway alone would mean anyone who found that URL could lock seats
// without ever joining the waiting room. Checking it again here closes
// that gap independently of how the request arrived.
func hasValidAdmission(r *http.Request, eventID, userID string, jwtSecret []byte) bool {
	tokenString, ok := bearerToken(r)
	if !ok {
		return false
	}

	claims := &admissionClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return false
	}

	return claims.EventID == eventID && claims.Subject == userID
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	return strings.TrimPrefix(header, prefix), true
}
