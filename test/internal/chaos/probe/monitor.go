package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/rig"
)

// Sample is one look at the running system.
type Sample struct {
	At          time.Time      `json:"at"`
	RSSMB       float64        `json:"rss_mb"`
	CPU         float64        `json:"cpu_pct"`
	FDs         int            `json:"fds"`
	Goroutines  int            `json:"goroutines"`
	HeapInuseMB float64        `json:"heap_inuse_mb"`
	HeapAllocMB float64        `json:"heap_alloc_mb"`
	GCs         int            `json:"gcs"`
	DBConns     int            `json:"db_conns"`
	DBActive    int            `json:"db_active"`
	DBLockWaits int            `json:"db_lock_waits"`
	Queues      map[string]int `json:"queues"`
	DeadLetters map[string]int `json:"dead_letters"`
	Mail        int            `json:"mail_total"`
	AuthLookups int64          `json:"auth_lookups"`
	DiskFreeMB  int64          `json:"disk_free_mb"`
}

// Monitor samples the system on an interval and keeps the extremes.
type Monitor struct {
	state   rig.State
	clients *rig.Clients
	db      *pgx.Conn
	out     *os.File

	mu        sync.Mutex
	first     Sample
	last      Sample
	peak      Sample
	baseMail  int
	haveBase  bool
	container string
	takes     int
	stop      context.CancelFunc
	done      chan struct{}
}

var (
	goroutineTotal = regexp.MustCompile(`goroutine profile: total (\d+)`)
	heapField      = regexp.MustCompile(`# (HeapAlloc|HeapInuse|NumGC) = (\d+)`)
)

// StartMonitor begins sampling. Series go to seriesPath as JSON lines.
func StartMonitor(ctx context.Context, state rig.State, seriesPath string, every time.Duration) (*Monitor, error) {
	clients, err := rig.NewClients(ctx, rig.Emulators{LocalStack: state.LocalStackURL, Region: "ap-south-1"})
	if err != nil {
		return nil, err
	}
	monitor := &Monitor{state: state, clients: clients, done: make(chan struct{}), container: rig.DefaultPostgresContainer}
	if conn, err := pgx.Connect(ctx, state.DatabaseURL); err == nil {
		monitor.db = conn
	}
	if seriesPath != "" {
		if monitor.out, err = os.Create(seriesPath); err != nil {
			return nil, err
		}
	}
	ctx, monitor.stop = context.WithCancel(ctx)
	go monitor.loop(ctx, every)
	return monitor, nil
}

func (monitor *Monitor) loop(ctx context.Context, every time.Duration) {
	defer close(monitor.done)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		sample := monitor.take(ctx)
		monitor.mu.Lock()
		if monitor.first.At.IsZero() {
			monitor.first = sample
		}
		monitor.last = sample
		monitor.peak = peakOf(monitor.peak, sample)
		monitor.mu.Unlock()
		if monitor.out != nil {
			encoded, _ := json.Marshal(sample)
			_, _ = monitor.out.Write(append(encoded, '\n'))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func peakOf(peak, sample Sample) Sample {
	if sample.RSSMB > peak.RSSMB {
		peak.RSSMB = sample.RSSMB
	}
	if sample.CPU > peak.CPU {
		peak.CPU = sample.CPU
	}
	if sample.FDs > peak.FDs {
		peak.FDs = sample.FDs
	}
	if sample.Goroutines > peak.Goroutines {
		peak.Goroutines = sample.Goroutines
	}
	if sample.HeapInuseMB > peak.HeapInuseMB {
		peak.HeapInuseMB = sample.HeapInuseMB
	}
	if sample.HeapAllocMB > peak.HeapAllocMB {
		peak.HeapAllocMB = sample.HeapAllocMB
	}
	if sample.DBConns > peak.DBConns {
		peak.DBConns = sample.DBConns
	}
	if sample.DBActive > peak.DBActive {
		peak.DBActive = sample.DBActive
	}
	if sample.DBLockWaits > peak.DBLockWaits {
		peak.DBLockWaits = sample.DBLockWaits
	}
	if peak.Queues == nil {
		peak.Queues = map[string]int{}
	}
	for name, depth := range sample.Queues {
		if depth > peak.Queues[name] {
			peak.Queues[name] = depth
		}
	}
	if peak.DeadLetters == nil {
		peak.DeadLetters = map[string]int{}
	}
	for name, depth := range sample.DeadLetters {
		if depth > peak.DeadLetters[name] {
			peak.DeadLetters[name] = depth
		}
	}
	peak.Mail, peak.AuthLookups, peak.GCs = sample.Mail, sample.AuthLookups, sample.GCs
	return peak
}

func (monitor *Monitor) take(ctx context.Context) Sample {
	sample := Sample{At: time.Now(), Queues: map[string]int{}, DeadLetters: map[string]int{}}
	pid := strconv.Itoa(monitor.state.PID)
	if out, err := exec.CommandContext(ctx, "ps", "-o", "rss=,pcpu=", "-p", pid).Output(); err == nil {
		fields := strings.Fields(string(out))
		if len(fields) == 2 {
			kb, _ := strconv.ParseFloat(fields[0], 64)
			sample.RSSMB = kb / 1024
			sample.CPU, _ = strconv.ParseFloat(fields[1], 64)
		}
	}
	if out, err := exec.CommandContext(ctx, "lsof", "-nP", "-p", pid).Output(); err == nil {
		sample.FDs = strings.Count(string(out), "\n") - 1
	}
	if body, err := get(ctx, monitor.state.PprofURL+"/debug/pprof/goroutine?debug=1"); err == nil {
		if match := goroutineTotal.FindStringSubmatch(body); match != nil {
			sample.Goroutines, _ = strconv.Atoi(match[1])
		}
	}
	if body, err := get(ctx, monitor.state.PprofURL+"/debug/pprof/heap?debug=1"); err == nil {
		for _, match := range heapField.FindAllStringSubmatch(body, -1) {
			value, _ := strconv.ParseFloat(match[2], 64)
			switch match[1] {
			case "HeapAlloc":
				sample.HeapAllocMB = value / (1 << 20)
			case "HeapInuse":
				sample.HeapInuseMB = value / (1 << 20)
			case "NumGC":
				sample.GCs = int(value)
			}
		}
	}
	if monitor.db != nil {
		_ = monitor.db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE state = 'active'),
			count(*) FILTER (WHERE wait_event_type = 'Lock')
			FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()`).
			Scan(&sample.DBConns, &sample.DBActive, &sample.DBLockWaits)
	}
	for name, url := range monitor.state.Queues {
		if visible, inFlight, err := monitor.clients.QueueDepth(ctx, url); err == nil {
			sample.Queues[name] = visible + inFlight
		}
	}
	for name, url := range monitor.state.DeadLetters {
		if visible, _, err := monitor.clients.QueueDepth(ctx, url); err == nil {
			sample.DeadLetters[name] = visible
		}
	}
	if body, err := get(ctx, monitor.state.MailpitURL+"/api/v1/info"); err == nil {
		var info struct{ Messages int }
		if json.Unmarshal([]byte(body), &info) == nil {
			sample.Mail = info.Messages
			monitor.mu.Lock()
			if !monitor.haveBase {
				monitor.baseMail, monitor.haveBase = info.Messages, true
			}
			sample.Mail = info.Messages - monitor.baseMail
			monitor.mu.Unlock()
		}
	}
	// The Docker VM's disk, every fifth look: a full one takes every container down.
	monitor.takes++
	if monitor.takes%5 == 1 {
		if free, _, err := rig.Headroom(ctx, monitor.container); err == nil {
			sample.DiskFreeMB = free >> 20
		}
	} else {
		monitor.mu.Lock()
		sample.DiskFreeMB = monitor.last.DiskFreeMB
		monitor.mu.Unlock()
	}
	if body, err := get(ctx, monitor.state.StubURL+"/_stub/stats"); err == nil {
		var stats map[string]int64
		if json.Unmarshal([]byte(body), &stats) == nil {
			sample.AuthLookups = stats["lookups"]
		}
	}
	return sample
}

// LowDisk reports whether the Docker VM's disk has less than minMB free. A
// reading of zero means it has not been read yet, and is not low.
func (monitor *Monitor) LowDisk(minMB int64) bool {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.last.DiskFreeMB > 0 && monitor.last.DiskFreeMB < minMB
}

// Now takes one sample immediately, for a before-and-after comparison.
func (monitor *Monitor) Now(ctx context.Context) Sample { return monitor.take(ctx) }

// Summary is how the system moved during the run.
type Summary struct{ First, Last, Peak Sample }

// Stop ends sampling and returns what was seen.
func (monitor *Monitor) Stop() Summary {
	monitor.stop()
	<-monitor.done
	if monitor.db != nil {
		_ = monitor.db.Close(context.Background())
	}
	if monitor.out != nil {
		_ = monitor.out.Close()
	}
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return Summary{First: monitor.first, Last: monitor.last, Peak: monitor.peak}
}

// Render writes the summary as a short table.
func (summary Summary) Render(out io.Writer) {
	w := bufio.NewWriter(out)
	defer w.Flush()
	row := func(name string, first, peak, last any) {
		fmt.Fprintf(w, "  %-22s %10v %10v %10v\n", name, first, peak, last)
	}
	fmt.Fprintf(w, "  %-22s %10s %10s %10s\n", "", "start", "peak", "end")
	row("RSS (MB)", int(summary.First.RSSMB), int(summary.Peak.RSSMB), int(summary.Last.RSSMB))
	row("heap in use (MB)", int(summary.First.HeapInuseMB), int(summary.Peak.HeapInuseMB), int(summary.Last.HeapInuseMB))
	row("goroutines", summary.First.Goroutines, summary.Peak.Goroutines, summary.Last.Goroutines)
	row("open file descriptors", summary.First.FDs, summary.Peak.FDs, summary.Last.FDs)
	row("CPU (%)", int(summary.First.CPU), int(summary.Peak.CPU), int(summary.Last.CPU))
	row("db connections", summary.First.DBConns, summary.Peak.DBConns, summary.Last.DBConns)
	row("db active queries", summary.First.DBActive, summary.Peak.DBActive, summary.Last.DBActive)
	row("db lock waits", summary.First.DBLockWaits, summary.Peak.DBLockWaits, summary.Last.DBLockWaits)
	row("GC cycles", summary.First.GCs, "", summary.Last.GCs)
	row("auth lookups made", summary.First.AuthLookups, "", summary.Last.AuthLookups)
	row("docker disk free (MB)", summary.First.DiskFreeMB, "", summary.Last.DiskFreeMB)
	row("mail sent (Mailpit Δ)", 0, "", summary.Last.Mail)
	for name, depth := range summary.Peak.Queues {
		row("queue "+name, summary.First.Queues[name], depth, summary.Last.Queues[name])
	}
	for name, depth := range summary.Peak.DeadLetters {
		if depth > 0 {
			row("dead-letter "+name, 0, depth, summary.Last.DeadLetters[name])
		}
	}
}

func get(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return string(body), err
}
