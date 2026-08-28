package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/redis/go-redis/v9"
)

type lockRequest struct {
	UserID string `json:"userId"`
}

// path shape: /seats/{eventId}/{seatId}/lock|release|confirm
func parseSeatPath(path string) (eventID, seatID, action string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 || parts[0] != "seats" {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

func seatsHandler(rdb *redis.Client, internalSecret string, jwtSecret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// /seats/{eventId} (no seatId/action) -- GET for a snapshot of every
		// locked/sold seat, DELETE to wipe all of them (the demo's "full
		// reset" button).
		if parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/"); len(parts) == 2 && parts[0] == "seats" {
			switch r.Method {
			case http.MethodGet:
				seats, err := listSeats(r.Context(), rdb, parts[1])
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				writeJSON(w, map[string]any{"seats": seats})

			case http.MethodDelete:
				n, err := resetSeats(r.Context(), rdb, parts[1])
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				writeJSON(w, map[string]any{"reset": n})

			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		eventID, seatID, action, ok := parseSeatPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var body lockRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" {
			http.Error(w, "userId is required", http.StatusBadRequest)
			return
		}

		ctx := context.Background()
		switch action {
		case "lock":
			if !hasValidAdmission(r, eventID, body.UserID, jwtSecret) {
				http.Error(w, "missing or invalid admission token -- join the waiting room first", http.StatusUnauthorized)
				return
			}
			acquired, err := lockSeat(ctx, rdb, eventID, seatID, body.UserID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"locked": acquired})

		case "release":
			if !hasInternalSecret(r, internalSecret) {
				http.Error(w, "forbidden: release is server-to-server only", http.StatusForbidden)
				return
			}
			released, err := releaseSeat(ctx, rdb, eventID, seatID, body.UserID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"released": released})

		case "confirm":
			if !hasInternalSecret(r, internalSecret) {
				http.Error(w, "forbidden: confirm is server-to-server only", http.StatusForbidden)
				return
			}
			confirmed, err := confirmSeat(ctx, rdb, eventID, seatID, body.UserID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"confirmed": confirmed})

		default:
			http.NotFound(w, r)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
