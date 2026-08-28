package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func main() {
	port := getenv("PORT", "8082")
	lockTTL = time.Duration(getenvInt("LOCK_TTL_MINUTES", 2)) * time.Minute

	rdb := newRedisClient()
	manager := newHubManager(rdb)

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"service": "seat-locking", "status": "ok"})
	})

	// WebSocket: /ws/{eventId} — live seat map updates for one event.
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		eventID := r.URL.Path[len("/ws/"):]
		if eventID == "" {
			http.Error(w, "eventId is required", http.StatusBadRequest)
			return
		}
		handleWS(manager, w, r, eventID)
	})

	internalSecret := getenv("INTERNAL_SECRET", "dev-internal-secret-change-me")

	jwtSecret := getenv("JWT_SECRET", "dev-secret-change-me")
	if jwtSecret == "dev-secret-change-me" {
		log.Println("warning: using default JWT_SECRET, set a real one before deploying")
	}

	// REST: /seats/{eventId}/{seatId}/lock|release|confirm
	mux.HandleFunc("/seats/", seatsHandler(rdb, internalSecret, []byte(jwtSecret)))

	log.Printf("seat-locking service listening on :%s", port)
	if err := http.ListenAndServe(":"+port, corsMiddleware(mux)); err != nil {
		log.Fatal(err)
	}
}
