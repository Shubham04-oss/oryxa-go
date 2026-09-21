package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/probe"
)

// attack is one targeted experiment: a hypothesis with a measurable outcome.
type attack struct {
	name    string
	summary string
	run     func(context.Context, *session) error
}

var attacks = []attack{
	{"preauth-body", "does files read a huge upload before it checks who is sending it?", attackPreAuthBody},
	{"search-cost", "does an oversized search string make the database do more work?", attackSearchCost},
	{"mail-amplification", "how many emails can one org member trigger with the team-call tool?", attackMailAmplification},
}

func attackCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("attack", flag.ExitOnError)
	workdir := flags.String("workdir", defaultWorkDir(), "rig scratch directory")
	verbose := flags.Bool("v", false, "verbose")
	_ = flags.Parse(args)
	names := flags.Args()
	if len(names) == 0 {
		fmt.Println("attacks:")
		for _, one := range attacks {
			fmt.Printf("  %-20s %s\n", one.name, one.summary)
		}
		return nil
	}
	s, err := openSession(ctx, *workdir, *verbose)
	if err != nil {
		return err
	}
	// Give both organizations their working objects, so an attack has something
	// to aim at.
	s.world.RunBaselines(ctx, probe.Flatten(probe.Select(nil)), false)
	for _, name := range names {
		found := false
		for _, one := range attacks {
			if one.name != name && name != "all" {
				continue
			}
			found = true
			fmt.Printf("\n══ %s — %s\n", one.name, one.summary)
			if err := one.run(ctx, s); err != nil {
				fmt.Printf("  attack could not run: %v\n", err)
			}
		}
		if !found {
			return fmt.Errorf("no attack named %q", name)
		}
	}
	s.finish("attacks")
	return nil
}

// countingReader counts what the server actually took from a request body.
type countingReader struct {
	inner io.Reader
	n     atomic.Int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	count, err := reader.inner.Read(buffer)
	reader.n.Add(int64(count))
	return count, err
}

// zeros is a reader of n zero bytes that costs no memory.
type zeros struct{ left int64 }

func (z *zeros) Read(buffer []byte) (int, error) {
	if z.left <= 0 {
		return 0, io.EOF
	}
	count := int64(len(buffer))
	if count > z.left {
		count = z.left
	}
	for index := int64(0); index < count; index++ {
		buffer[index] = 'A'
	}
	z.left -= count
	return int(count), nil
}

// upload streams a multipart file of size bytes to path and reports what the
// server took before answering.
func (s *session) upload(ctx context.Context, path, token string, size int64) (status int, consumed, total int64, took time.Duration, err error) {
	var prefix bytes.Buffer
	writer := multipart.NewWriter(&prefix)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="big.txt"`)
	header.Set("Content-Type", "text/plain")
	if _, err = writer.CreatePart(header); err != nil {
		return
	}
	prefixBytes := append([]byte{}, prefix.Bytes()...)
	suffix := []byte("\r\n--" + writer.Boundary() + "--\r\n")
	counter := &countingReader{inner: io.MultiReader(bytes.NewReader(prefixBytes), &zeros{left: size}, bytes.NewReader(suffix))}
	total = int64(len(prefixBytes)) + size + int64(len(suffix))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.state.OryxaURL+path, counter)
	if err != nil {
		return
	}
	request.ContentLength = total
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	started := time.Now()
	client := &http.Client{Timeout: 90 * time.Second}
	response, err := client.Do(request)
	took = time.Since(started)
	if err != nil {
		return 0, counter.n.Load(), total, took, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode, counter.n.Load(), total, took, nil
}

// spill lists the temp files the server has open right now, which is where
// Go's multipart parser puts what does not fit in memory.
func spill(workdir string) (files int, bytesOnDisk int64) {
	entries, _ := os.ReadDir(filepath.Join(workdir, "tmp"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "multipart-") {
			files++
			if info, err := entry.Info(); err == nil {
				bytesOnDisk += info.Size()
			}
		}
	}
	return
}

func mb(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

func attackPreAuthBody(ctx context.Context, s *session) error {
	filesPath := "/v1/organizations/" + s.world.A.ID + "/files"
	fmt.Println("  A bogus bearer token passes the handler's shape check, so the question is whether the body is read before auth is asked.")

	// 1. How much of an upload does the server take before it says no?
	fmt.Println("\n  multipart upload with a token nobody issued:")
	for _, size := range []int64{1 << 20, 16 << 20, 128 << 20} {
		status, consumed, total, took, err := s.upload(ctx, filesPath, "not-a-real-token", size)
		if err != nil {
			fmt.Printf("    %-9s -> error after %s (took %s of %s): %v\n", mb(size), took.Round(time.Millisecond), mb(consumed), mb(total), err)
			continue
		}
		fmt.Printf("    %-9s -> %d in %-8s; the server read %s of %s before answering\n", mb(size), status, took.Round(time.Millisecond), mb(consumed), mb(total))
		if consumed > 2<<20 {
			s.runner.Findings.Add(probe.Finding{
				Kind: "pre-auth-body-read", Severity: probe.High, Op: "files.create", Route: "POST /v1/organizations/{org}/files", Class: "resource",
				Status: status, Detail: fmt.Sprintf("with a token nobody issued, files read %s of a %s multipart upload before refusing it", mb(consumed), mb(total)),
			}, "multipart")
		}
	}
	fmt.Println("  control: the same upload with no Authorization header at all:")
	status, consumed, total, took, err := s.upload(ctx, filesPath, "", 128<<20)
	if err == nil {
		fmt.Printf("    128.0 MiB -> %d in %s; the server read %s of %s\n", status, took.Round(time.Millisecond), mb(consumed), mb(total))
	}

	// 2. What does it cost the server to hold many at once? The same question
	// for every endpoint that accepts a large body.
	filesBody, _ := json.Marshal(map[string]any{
		"filename": "x.txt", "content_type": "text/plain", "purpose": "upload", "scope": "member",
		"content": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("A"), 6<<20)),
	})
	s.burst(ctx, "files: 40 concurrent 8 MiB JSON uploads", filesPath, filesBody, 40)

	ragBody, _ := json.Marshal(map[string]any{
		"source": map[string]any{"kind": "inline"}, "title": "x", "content_type": "text/plain",
		"text": strings.Repeat("A", 20<<20),
	})
	s.burst(ctx, "rag: 40 concurrent 20 MiB JSON documents", "/v1/organizations/"+s.world.A.ID+"/corpora/no_such_corpus/documents", ragBody, 40)
	return nil
}

// burst sends n copies of a body at once, each with a token nobody issued,
// while timing a legitimate request beside them, and reports what it cost.
func (s *session) burst(ctx context.Context, label, path string, body []byte, n int) {
	fmt.Printf("\n  %s, bogus token, while timing a legitimate request:\n", label)
	before := s.monitor.Now(ctx)
	var wait sync.WaitGroup
	var mu sync.Mutex
	tally := map[string]int{}
	stop := make(chan struct{})
	var peakRSS, peakHeap, peakSpillFiles, peakSpillBytes atomic.Int64
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(150 * time.Millisecond):
			}
			sample := s.monitor.Now(ctx)
			if rss := int64(sample.RSSMB * 100); rss > peakRSS.Load() {
				peakRSS.Store(rss)
			}
			if heap := int64(sample.HeapInuseMB * 100); heap > peakHeap.Load() {
				peakHeap.Store(heap)
			}
			files, size := spill(s.workdir)
			if int64(files) > peakSpillFiles.Load() {
				peakSpillFiles.Store(int64(files))
			}
			if size > peakSpillBytes.Load() {
				peakSpillBytes.Store(size)
			}
		}
	}()
	started := time.Now()
	for range n {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.state.OryxaURL+path, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer not-a-real-token")
			response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				text := err.Error()
				if at := strings.LastIndex(text, ": "); at >= 0 {
					text = text[at+2:]
				}
				tally["error: "+text]++
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			tally[fmt.Sprint(response.StatusCode)]++
		}()
	}
	var legit []time.Duration
	for range 20 {
		time.Sleep(60 * time.Millisecond)
		res := s.client.Do(ctx, probe.Req{Method: "GET", Path: "/v1/organizations/" + s.world.A.ID + "/teams", Token: s.world.A.Owner.Token, Op: "legit", Route: "GET teams"})
		legit = append(legit, res.Latency)
	}
	wait.Wait()
	close(stop)
	sort.Slice(legit, func(i, j int) bool { return legit[i] < legit[j] })
	fmt.Printf("    answers: %v; finished in %s\n", tally, time.Since(started).Round(time.Millisecond))
	fmt.Printf("    RSS %.0f -> peak %.0f MiB; heap in use %.0f -> peak %.0f MiB\n", before.RSSMB, float64(peakRSS.Load())/100, before.HeapInuseMB, float64(peakHeap.Load())/100)
	fmt.Printf("    a legitimate GET during the burst: median %s, worst %s\n", legit[len(legit)/2].Round(time.Microsecond), legit[len(legit)-1].Round(time.Microsecond))
	if peakSpillFiles.Load() > 0 {
		fmt.Printf("    multipart spill files seen at once: %d (%s)\n", peakSpillFiles.Load(), mb(peakSpillBytes.Load()))
	}
}

func attackSearchCost(ctx context.Context, s *session) error {
	org := s.world.A
	owner := org.Owner.Token
	base := "/v1/organizations/" + org.ID
	fmt.Println("  making 1500 teams so a search has rows to score...")
	var wait sync.WaitGroup
	sem := make(chan struct{}, 8)
	for index := range 1500 {
		wait.Add(1)
		sem <- struct{}{}
		go func() {
			defer wait.Done()
			defer func() { <-sem }()
			body, _ := json.Marshal(map[string]string{"name": fmt.Sprintf("Platform group %04d ops", index)})
			s.client.Do(ctx, probe.Req{Method: "POST", Path: base + "/teams", Token: owner, Body: body, Header: http.Header{"Content-Type": {"application/json"}}, Op: "setup", Route: "POST teams"})
		}()
	}
	wait.Wait()

	fmt.Println("\n  one search at a time, longer and longer q:")
	fmt.Printf("    %-10s %-12s %-12s\n", "q length", "teams/search", "members/search")
	for _, size := range []int{8, 100, 1_000, 10_000, 100_000, 800_000} {
		q := strings.Repeat("plat ", size/5+1)[:size]
		var cells []string
		for _, route := range []string{"/teams/search", "/members/search"} {
			var best time.Duration
			var status int
			for range 3 {
				res := s.client.Do(ctx, probe.Req{Method: "GET", Path: base + route + "?limit=20&q=" + url.QueryEscape(q), Token: owner, Op: "search", Route: "GET " + route, Timeout: 60 * time.Second})
				status = res.Status
				if best == 0 || res.Latency > best {
					best = res.Latency
				}
			}
			cells = append(cells, fmt.Sprintf("%d %s", status, best.Round(time.Millisecond)))
		}
		fmt.Printf("    %-10d %-12s %-12s\n", size, cells[0], cells[1])
	}

	fmt.Println("\n  16 concurrent 100 KB searches for 8 seconds, timing an ordinary request beside them:")
	q := strings.Repeat("plat ", 20_000)
	var done atomic.Int64
	var statusMu sync.Mutex
	statuses := map[int]int{}
	stop := make(chan struct{})
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res := s.client.Do(ctx, probe.Req{Method: "GET", Path: base + "/teams/search?limit=20&q=" + url.QueryEscape(q), Token: owner, Op: "search", Route: "GET /teams/search", Timeout: 60 * time.Second})
				statusMu.Lock()
				statuses[res.Status]++
				statusMu.Unlock()
				done.Add(1)
			}
		}()
	}
	time.Sleep(time.Second)
	var quiet []time.Duration
	for range 30 {
		res := s.client.Do(ctx, probe.Req{Method: "GET", Path: base + "/tools", Token: owner, Op: "legit", Route: "GET tools"})
		quiet = append(quiet, res.Latency)
		time.Sleep(200 * time.Millisecond)
	}
	close(stop)
	wait.Wait()
	sort.Slice(quiet, func(i, j int) bool { return quiet[i] < quiet[j] })
	sample := s.monitor.Now(ctx)
	fmt.Printf("    %d searches answered %v; an unrelated GET took median %s, worst %s; db active queries at the end: %d\n",
		done.Load(), statuses, quiet[len(quiet)/2].Round(time.Microsecond), quiet[len(quiet)-1].Round(time.Microsecond), sample.DBActive)
	return nil
}

func attackMailAmplification(ctx context.Context, s *session) error {
	org := s.world.A
	base := "/v1/organizations/" + org.ID
	teamID := s.world.Get("a", "team")
	if teamID == "" {
		return fmt.Errorf("no team to call")
	}
	// Put everyone on the team with email switched on, then call it as the
	// least privileged person in the organization.
	for _, person := range []probe.Identity{org.Admin, org.Member, org.Member2} {
		body, _ := json.Marshal(map[string]string{"user_id": person.UserID, "role": "member"})
		s.client.Do(ctx, probe.Req{Method: "POST", Path: base + "/teams/" + teamID + "/members", Token: org.Owner.Token, Body: body, Header: http.Header{"Content-Type": {"application/json"}}, Op: "setup", Route: "POST members"})
		s.client.Do(ctx, probe.Req{Method: "PATCH", Path: base + "/teams/" + teamID + "/notification", Token: person.Token, Body: []byte(`{"notify_email":true}`), Header: http.Header{"Content-Type": {"application/json"}}, Op: "setup", Route: "PATCH notification"})
	}
	conn, err := pgx.Connect(ctx, s.state.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	count := func() (outbox int) {
		_ = conn.QueryRow(ctx, `SELECT count(*) FROM teams_outbox`).Scan(&outbox)
		return
	}
	mailBefore := s.monitor.Now(ctx).Mail
	outboxBefore := count()
	started := time.Now()
	ok, refused := 0, 0
	for range 200 {
		res := s.client.Do(ctx, probe.Req{Method: "POST", Path: base + "/tools/call_team", Token: org.Member.Token, Body: []byte(`{"query":"` + teamID + `"}`), Header: http.Header{"Content-Type": {"application/json"}}, Op: "call_team", Route: "POST /tools/call_team"})
		if res.Status == 200 {
			ok++
		} else {
			refused++
		}
	}
	elapsed := time.Since(started)
	fmt.Printf("  200 calls to call_team by a plain member: %d accepted, %d refused, in %s (no rate limit if none refused)\n", ok, refused, elapsed.Round(time.Millisecond))
	fmt.Printf("  outbox rows written: %d\n", count()-outboxBefore)
	time.Sleep(12 * time.Second)
	after := s.monitor.Now(ctx)
	fmt.Printf("  emails that reached Mailpit after 12s: %d (email queue depth now %d)\n", after.Mail-mailBefore, after.Queues["email"])
	if ok >= 200 {
		s.runner.Findings.Add(probe.Finding{
			Kind: "no-rate-limit-fanout", Severity: probe.Medium, Op: "tools.call_team", Route: "POST /v1/organizations/{org}/tools/call_team", Class: "abuse", Status: 200,
			Detail: fmt.Sprintf("a plain member made %d tool calls in %s, each writing outbox rows and pinging every member who has email on; no call was refused", ok, elapsed.Round(time.Millisecond)),
		}, "200-calls")
	}
	return nil
}
