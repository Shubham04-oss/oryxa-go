package threads

import (
	"context"
	"errors"
	"sync"
	"time"
)

// A deploy replaces the process every agent run lives in. Without a drain the
// process is told to stop and its runs are cut short wherever they are, and
// recorded as failures. A Drain lets them finish: once it begins, no run
// starts, the runs already going are waited for, and only those still going at
// its deadline are stopped, recorded as interrupted rather than failed, so that
// whoever started them can start them again on the next process. One Drain is
// shared by every thread service in a process (Config.Drain).

var (
	// ErrDraining reports a run asked to start while the process is draining
	// to stop. Nothing was started, and the same run may start again on the
	// process that replaces this one.
	ErrDraining = errors.New("Oryxa is restarting and starts no new run here; try again in a moment")
	// ErrInterrupted is why a run the drain stopped at its deadline ended: it
	// was going well, and the process it ran in is stopping.
	ErrInterrupted = errors.New("the run was interrupted because Oryxa restarted")
)

// interruptGrace is how long a drain waits, once it has stopped the runs still
// going at its deadline, for them to record how they ended.
const interruptGrace = 5 * time.Second

// Drain tracks the runs going in a process, and stops them in order when the
// process stops. The zero value is not usable; NewDrain makes one. A nil
// *Drain tracks nothing and never drains, which is a thread service without
// one.
type Drain struct {
	mu       sync.Mutex
	draining bool
	running  map[*Run]context.CancelCauseFunc
	// idle is closed, and replaced, each time the last run going ends.
	idle chan struct{}
}

// NewDrain makes a Drain with no runs going.
func NewDrain() *Drain {
	return &Drain{running: map[*Run]context.CancelCauseFunc{}, idle: make(chan struct{})}
}

// Draining says the drain has begun: no new run starts.
func (drain *Drain) Draining() bool {
	if drain == nil {
		return false
	}
	drain.mu.Lock()
	defer drain.mu.Unlock()
	return drain.draining
}

// begin records a run going, which cancel stops; ErrDraining once the drain
// has begun.
func (drain *Drain) begin(run *Run, cancel context.CancelCauseFunc) error {
	if drain == nil {
		return nil
	}
	drain.mu.Lock()
	defer drain.mu.Unlock()
	if drain.draining {
		return ErrDraining
	}
	drain.running[run] = cancel
	return nil
}

// end records a run ended.
func (drain *Drain) end(run *Run) {
	if drain == nil {
		return
	}
	drain.mu.Lock()
	defer drain.mu.Unlock()
	if _, ok := drain.running[run]; !ok {
		return
	}
	delete(drain.running, run)
	if len(drain.running) == 0 {
		close(drain.idle)
		drain.idle = make(chan struct{})
	}
}

// Running is how many runs are going.
func (drain *Drain) Running() int {
	if drain == nil {
		return 0
	}
	drain.mu.Lock()
	defer drain.mu.Unlock()
	return len(drain.running)
}

// Drain begins the drain and returns once every run has ended. From the moment
// it is called no new run starts. The runs going are let finish until ctx
// ends; any still going then are stopped with ErrInterrupted and given
// interruptGrace to record it. It reports how many runs it had to interrupt.
func (drain *Drain) Drain(ctx context.Context) int {
	if drain == nil {
		return 0
	}
	drain.mu.Lock()
	drain.draining = true
	if len(drain.running) == 0 {
		drain.mu.Unlock()
		return 0
	}
	idle := drain.idle
	drain.mu.Unlock()

	select {
	case <-idle:
		return 0
	case <-ctx.Done():
	}

	drain.mu.Lock()
	interrupted := len(drain.running)
	for _, cancel := range drain.running {
		cancel(ErrInterrupted)
	}
	idle = drain.idle
	drain.mu.Unlock()
	if interrupted == 0 {
		return 0
	}
	select {
	case <-idle:
	case <-time.After(interruptGrace):
	}
	return interrupted
}

// Interrupted says a run ended because it was stopped by a drain, or never
// started for one: whoever started it may start it again on the next process,
// and it is not a failure of the run's own.
func Interrupted(err error) bool {
	return errors.Is(err, ErrInterrupted) || errors.Is(err, ErrDraining)
}
