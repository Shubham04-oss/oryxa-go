// Command fakertk answers the part of RealtimeKit's REST API calls use
// (edge/src/call/realtimekit.ts), so test/live/call runs against a local
// `wrangler dev` edge with REALTIMEKIT_API_URL pointing here. Tokens are
// fake: nothing can join a meeting with them.
//
//	go run ./test/live/call/fakertk -addr 127.0.0.1:8790
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strings"
	"sync"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8790", "where to listen")
	flag.Parse()
	var mu sync.Mutex
	participants := map[string]bool{}
	answer := func(w http.ResponseWriter, status int, data any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": status < 400, "data": data})
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			answer(w, http.StatusUnauthorized, nil)
			return
		}
		// /{account}/realtime/kit/{app}/…
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[1] != "realtime" || parts[2] != "kit" {
			answer(w, http.StatusNotFound, nil)
			return
		}
		rest := parts[4:]
		log.Printf("%s /%s", r.Method, strings.Join(rest, "/"))
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "meetings":
			answer(w, http.StatusCreated, map[string]string{"id": "mtg_" + random()})
		case r.Method == http.MethodPost && len(rest) == 3 && rest[2] == "participants":
			id := "prt_" + random()
			participants[id] = true
			token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
				base64.RawURLEncoding.EncodeToString([]byte(`{"meetingId":"`+rest[1]+`","participantId":"`+id+`"}`)) + ".fake"
			answer(w, http.StatusCreated, map[string]string{"id": id, "token": token})
		case r.Method == http.MethodDelete && len(rest) == 4 && rest[2] == "participants":
			if !participants[rest[3]] {
				answer(w, http.StatusNotFound, nil)
				return
			}
			delete(participants, rest[3])
			answer(w, http.StatusOK, map[string]string{})
		case r.Method == http.MethodPost && len(rest) == 4 && rest[3] == "kick-all":
			// A meeting nobody joined over WebRTC has no session.
			answer(w, http.StatusNotFound, nil)
		case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "recordings":
			answer(w, http.StatusCreated, map[string]string{"id": "rec_" + random()})
		case r.Method == http.MethodPut && len(rest) == 2 && rest[0] == "recordings":
			answer(w, http.StatusOK, map[string]string{})
		default:
			answer(w, http.StatusNotFound, nil)
		}
	})
	log.Printf("fake RealtimeKit on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func random() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
