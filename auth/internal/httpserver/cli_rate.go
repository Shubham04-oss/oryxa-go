package httpserver

import (
	"net"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
)

// Bound the new public database-writing endpoint without trusting forwarding
// headers supplied by callers. Per-peer keys are hashed and discarded each
// minute; the global budget bounds memory even with arbitrary remote peers.
type cliChallengeLimiter struct {
	mu     sync.Mutex
	minute int64
	total  int
	peers  map[string]int
}

func (limiter *cliChallengeLimiter) allow(remote string, now time.Time) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	key := clisession.Hash(host)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	minute := now.Unix() / 60
	if minute != limiter.minute || limiter.peers == nil {
		limiter.minute = minute
		limiter.total = 0
		limiter.peers = map[string]int{}
	}
	if limiter.total >= 120 || limiter.peers[key] >= 10 {
		return false
	}
	limiter.total++
	limiter.peers[key]++
	return true
}
