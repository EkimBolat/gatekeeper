// Command loadtest proves the seat-locking guarantee under real network
// concurrency, against a real running deployment, instead of just
// in-process goroutines racing a Redis client directly (that's still
// what services/seat-locking/lock_test.go covers, and it's the right
// tool for CI). This is the same claim at a different, complementary
// layer: many independent HTTP clients, many independent admission
// tokens, hitting the actual public API at the same instant.
//
// Two phases:
//  1. Admit N distinct users through the real waiting room (paced to
//     stay under the gateway's per-IP rate limit -- this is setup, not
//     what's being measured).
//  2. Release all N at once to race for the exact same seat, straight
//     against Seat Locking's own URL. That's deliberate: the gateway's
//     rate limiter would reject a genuine simultaneous burst from a
//     single test machine's IP long before it reached Seat Locking, and
//     rate limiting is a different, already-covered guarantee. Racing
//     seat-locking directly is also exactly the path this project's
//     admission-token check (services/seat-locking/admission.go) has to
//     hold up under, so this doubles as a concurrency test for that.
//
// Usage:
//
//	go run . -gateway https://api-gateway-u36u.onrender.com -seat-locking https://seat-locking.onrender.com -n 200
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type statusResp struct {
	Status string `json:"status"`
	Token  string `json:"token"`
}

type lockResp struct {
	Locked bool `json:"locked"`
}

func main() {
	gatewayURL := flag.String("gateway", "http://localhost:8080", "api-gateway base URL, used only to join the queue and get admitted")
	seatLockingURL := flag.String("seat-locking", "http://localhost:8082", "seat-locking base URL, raced directly so the gateway's per-IP rate limit doesn't interfere with phase 2")
	eventID := flag.String("event", fmt.Sprintf("loadtest-%d", time.Now().Unix()), "event id to use -- arbitrary and isolated, safe to reuse")
	seatID := flag.String("seat", "LOADTEST", "the single seat every racer will try to lock")
	n := flag.Int("n", 200, "number of concurrent users racing for the seat")
	joinRate := flag.Float64("join-rate", 4, "requests/sec used while joining the queue, kept under the gateway's rate limit (5/s, burst 10)")
	admitTimeout := flag.Duration("admit-timeout", 5*time.Minute, "how long to wait for every user to be admitted before giving up")
	flag.Parse()

	client := &http.Client{Timeout: 30 * time.Second}

	userIDs := make([]string, *n)
	for i := range userIDs {
		userIDs[i] = fmt.Sprintf("loadtest-user-%d", i)
	}

	fmt.Printf("=== Phase 1: admitting %d users into event %q ===\n", *n, *eventID)
	joinInterval := time.Duration(float64(time.Second) / *joinRate)
	for i, uid := range userIDs {
		if err := join(client, *gatewayURL, *eventID, uid); err != nil {
			log.Fatalf("join failed for %s: %v", uid, err)
		}
		if i < len(userIDs)-1 {
			time.Sleep(joinInterval)
		}
	}
	fmt.Println("all users joined, waiting for admission...")

	tokens := make([]string, *n)
	deadline := time.Now().Add(*admitTimeout)
	for i, uid := range userIDs {
		token, err := waitForAdmission(client, *gatewayURL, *eventID, uid, deadline)
		if err != nil {
			log.Fatalf("admission failed for %s: %v", uid, err)
		}
		tokens[i] = token
		if (i+1)%20 == 0 || i == *n-1 {
			fmt.Printf("  admitted %d/%d\n", i+1, *n)
		}
	}
	fmt.Printf("all %d users admitted.\n\n", *n)

	fmt.Printf("=== Phase 2: racing all %d for seat %q at the same instant ===\n", *n, *seatID)
	var wg sync.WaitGroup
	start := make(chan struct{})
	locked := make([]bool, *n)
	latencies := make([]time.Duration, *n)
	callErrs := make([]error, *n)

	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			t0 := time.Now()
			ok, err := lockSeat(client, *seatLockingURL, *eventID, *seatID, userIDs[i], tokens[i])
			latencies[i] = time.Since(t0)
			locked[i] = ok
			callErrs[i] = err
		}(i)
	}
	close(start) // release every goroutine at once
	wg.Wait()

	wins, errCount := 0, 0
	var totalLatency time.Duration
	for i := 0; i < *n; i++ {
		if callErrs[i] != nil {
			errCount++
			continue
		}
		if locked[i] {
			wins++
		}
		totalLatency += latencies[i]
	}

	fmt.Printf("\n=== Results ===\n")
	fmt.Printf("racers:      %d\n", *n)
	fmt.Printf("winners:     %d  (must be exactly 1)\n", wins)
	fmt.Printf("errors:      %d\n", errCount)
	if *n > errCount {
		fmt.Printf("avg latency: %v\n", totalLatency/time.Duration(*n-errCount))
	}

	if wins != 1 {
		fmt.Printf("\nFAIL: expected exactly 1 winner, got %d\n", wins)
		os.Exit(1)
	}
	fmt.Println("\nPASS: exactly one winner out of", *n, "simultaneous real HTTP requests.")
}

func join(client *http.Client, gateway, eventID, userID string) error {
	body, _ := json.Marshal(map[string]string{"userId": userID})
	resp, err := client.Post(gateway+"/queue/"+eventID+"/join", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

func waitForAdmission(client *http.Client, gateway, eventID, userID string, deadline time.Time) (string, error) {
	for {
		resp, err := client.Get(gateway + "/queue/" + eventID + "/status?userId=" + userID)
		if err != nil {
			return "", err
		}
		var s statusResp
		decodeErr := json.NewDecoder(resp.Body).Decode(&s)
		resp.Body.Close()
		if decodeErr != nil {
			return "", decodeErr
		}
		if s.Status == "admitted" {
			return s.Token, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for admission (last status: %s)", s.Status)
		}
		time.Sleep(2 * time.Second)
	}
}

func lockSeat(client *http.Client, seatLockingURL, eventID, seatID, userID, token string) (bool, error) {
	body, _ := json.Marshal(map[string]string{"userId": userID})
	req, err := http.NewRequest(http.MethodPost, seatLockingURL+"/seats/"+eventID+"/"+seatID+"/lock", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("status %d", resp.StatusCode)
	}

	var lr lockResp
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return false, err
	}
	return lr.Locked, nil
}
