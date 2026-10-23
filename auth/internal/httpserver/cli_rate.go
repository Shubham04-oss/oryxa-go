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
	// perPeer and perMinute are the budgets; zero means 10 and 120.
	perPeer, perMinute int
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
	perPeer, perMinute := limiter.perPeer, limiter.perMinute
	if perPeer == 0 {
		perPeer = 10
	}
	if perMinute == 0 {
		perMinute = 120
	}
	if limiter.total >= perMinute || limiter.peers[key] >= perPeer {
		return false
	}
	limiter.total++
	limiter.peers[key]++
	return true
}
