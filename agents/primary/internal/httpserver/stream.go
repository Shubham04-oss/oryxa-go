package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// eventStream writes a run's items as server-sent events: one event per
// item, named by its type, with the item as JSON data. A keep-alive comment
// shares the connection, so every write takes the lock.
type eventStream struct {
	mu         sync.Mutex
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func newEventStream(writer http.ResponseWriter, controller *http.ResponseController) *eventStream {
	return &eventStream{writer: writer, controller: controller}
}

// item sends one item. An error means the member is no longer listening.
func (stream *eventStream) item(item threads.Item) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode %s item: %w", item.Type, err)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if _, err := fmt.Fprintf(stream.writer, "event: %s\ndata: %s\n\n", item.Type, data); err != nil {
		return err
	}
	return stream.controller.Flush()
}

// keepAlive sends a comment whenever interval passes, until the returned
// function is called.
func (stream *eventStream) keepAlive(interval time.Duration) func() {
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			stream.mu.Lock()
			_, err := fmt.Fprint(stream.writer, ": keep-alive\n\n")
			if err == nil {
				err = stream.controller.Flush()
			}
			stream.mu.Unlock()
			if err != nil {
				// The run notices on its next item.
				return
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

// ForThreads serves a threads.Service. StartRun returns the service's own
// run type, so it cannot satisfy Threads by itself.
func ForThreads(service *threads.Service) Threads { return threadsService{service} }

type threadsService struct{ *threads.Service }

func (service threadsService) StartRun(ctx context.Context, member threads.Member, threadID string, input threads.RunInput) (Run, error) {
	run, err := service.Service.StartRun(ctx, member, threadID, input)
	if err != nil {
		return nil, err
	}
	return run, nil
}
