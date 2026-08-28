package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

const testJWTSecret = "the-real-jwt-secret"

func signTestAdmission(t *testing.T, eventID, userID, secret string, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	claims := admissionClaims{
		EventID: eventID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}
	return token
}

// TestSeatsHandler_ConfirmAndReleaseRequireInternalSecret guards the fix
// for the bug where any client that could reach this service -- through
// the gateway's blanket /seats/ passthrough, or directly -- could call
// confirm/release itself and mark a seat SOLD (or release someone
// else's checkout) without ever going through the order/payment saga.
// It doesn't need a real Redis: a rejected request never reaches it.
func TestSeatsHandler_ConfirmAndReleaseRequireInternalSecret(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	handler := seatsHandler(rdb, "the-real-secret", []byte(testJWTSecret))

	for _, action := range []string{"release", "confirm"} {
		t.Run(action+"/no secret", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/seats/evt/S1/"+action, strings.NewReader(`{"userId":"u1"}`))
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 with no secret, got %d", rec.Code)
			}
		})

		t.Run(action+"/wrong secret", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/seats/evt/S1/"+action, strings.NewReader(`{"userId":"u1"}`))
			req.Header.Set("X-Internal-Secret", "not-the-secret")
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 with wrong secret, got %d", rec.Code)
			}
		})
	}
}

// TestSeatsHandler_LockRequiresValidAdmission guards the fix for the bug
// where this service's own public URL let anyone POST directly to /lock
// and skip the waiting room entirely -- the gateway's admission check
// only protects requests that actually go through the gateway. Every
// rejected case here never reaches Redis, so a real instance isn't
// needed; the last case just confirms a valid token clears the gate
// (what happens after is a separate concern).
func TestSeatsHandler_LockRequiresValidAdmission(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	handler := seatsHandler(rdb, "the-internal-secret", []byte(testJWTSecret))

	lockReq := func(t *testing.T, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/seats/evt/S1/lock", strings.NewReader(`{"userId":"u1"}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	cases := []struct {
		name  string
		token func(t *testing.T) string
	}{
		{"no token", func(t *testing.T) string { return "" }},
		{"wrong signature", func(t *testing.T) string {
			return signTestAdmission(t, "evt", "u1", "not-the-real-secret", 10*time.Minute)
		}},
		{"expired token", func(t *testing.T) string {
			return signTestAdmission(t, "evt", "u1", testJWTSecret, -time.Minute)
		}},
		{"token for a different event", func(t *testing.T) string {
			return signTestAdmission(t, "some-other-event", "u1", testJWTSecret, 10*time.Minute)
		}},
		{"token for a different user", func(t *testing.T) string {
			return signTestAdmission(t, "evt", "someone-else", testJWTSecret, 10*time.Minute)
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := lockReq(t, c.token(t))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
		})
	}

	t.Run("valid token clears the admission check", func(t *testing.T) {
		token := signTestAdmission(t, "evt", "u1", testJWTSecret, 10*time.Minute)
		rec := lockReq(t, token)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("expected a valid token to pass the admission check, got 401")
		}
	})
}
