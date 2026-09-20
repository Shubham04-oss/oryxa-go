// Command agentbench hands the agents a few tasks and reports what the runs
// cost: latency, time to first text, tokens, cache hits and the priced cost.
// It needs Vertex AI Application Default Credentials and
// GOOGLE_CLOUD_PROJECT, like cmd/devui.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/bench"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelcache"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

// toolsTask exercises dynamic loading with the notes fixture toolbox, whose
// skill is long enough to take the prompt prefix over the explicit cache floor.
var toolsTask = bench.Task{
	Name:   "tools",
	Prompt: "Load the notes toolbox with load_tools, add the note 'launch checklist' with notes_add, then answer with the note's text and nothing else.",
}

// defaultTasks exercise the chat model and the routed work paths without
// needing AgentCore or the service APIs.
var defaultTasks = []bench.Task{
	{Name: "chat", Prompt: "In one sentence, explain what a message queue is for."},
	{Name: "plan", Prompt: "Plan a three-step launch checklist for a new feature. Keep it short and do not carry it out."},
	{Name: "research", Prompt: "What is the latest stable Go release, and when was it published? Answer in two sentences and name your sources."},
	{Name: "read", Prompt: "Read https://go.dev/doc/devel/release and tell me the latest Go release it lists, in two sentences."},
	{Name: "draft", Prompt: "Write a 150-word welcome note for an engineer joining the platform team."},
}

func main() {
	repeat := flag.Int("repeat", 2, "runs per task; the first warms the prompt cache")
	taskNames := flag.String("tasks", "", "comma-separated task names to run (default: all)")
	cacheEnabled := flag.Bool("cache", true, "use explicit Vertex prompt caching")
	withNotes := flag.Bool("with-notes", false, "register the notes fixture toolbox and the tools task, to exercise loading, its skill and caching")
	withTeamspaces := flag.Bool("with-teamspaces", false, "run multi-user whisperer-mode tasks against a deterministic shared Teamspace fixture")
	verbose := flag.Bool("v", false, "log every agent event while measuring")
	answers := flag.Bool("answers", false, "log each run's final answer")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	settings, err := serviceconfig.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if *cacheEnabled {
		settings.Cache = true
	} else {
		settings.Cache = false
	}

	mainModel, err := agents.Gemini(ctx, settings.Model, settings.GoogleCloudProject, settings.GoogleCloudLocation, nil)
	if err != nil {
		log.Fatalf("main model: %v", err)
	}
	liteModel, err := agents.Gemini(ctx, settings.ModelLite, settings.GoogleCloudProject, settings.GoogleCloudLocation, nil)
	if err != nil {
		log.Fatalf("lite model: %v", err)
	}
	var promptCache modelcache.Store
	if settings.Cache {
		client, err := agents.GeminiClient(ctx, settings.GoogleCloudProject, settings.GoogleCloudLocation, nil)
		if err != nil {
			log.Fatalf("prompt cache client: %v", err)
		}
		cacheStore := modelcache.NewGenaiStore(client, settings.CacheTTL)
		promptCache = cacheStore
		defer func() {
			closeContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := cacheStore.Close(closeContext); err != nil {
				log.Printf("delete prompt caches: %v", err)
			}
		}()
	}
	cacheConfig := modelcache.Config{TTL: settings.CacheTTL, MinTokens: settings.CacheMinTokens}
	router, err := modelrouter.New(
		modelcache.Cached(mainModel, promptCache, cacheConfig),
		modelcache.Cached(liteModel, promptCache, cacheConfig),
	)
	if err != nil {
		log.Fatalf("model router: %v", err)
	}

	var toolboxes []agents.Toolbox
	var teamspaces *benchTeamspaces
	if *withNotes {
		toolboxes = append(toolboxes, notesToolbox())
	}
	if *withTeamspaces {
		teamspaces = newBenchTeamspaces()
		box, err := agents.TeamspaceToolbox(agents.TeamspaceConfig{Service: teamspaces})
		if err != nil {
			log.Fatalf("teamspaces toolbox: %v", err)
		}
		toolboxes = append(toolboxes, box)
		ctx = memberauth.WithAccessToken(ctx, "bench-member-token")
	}
	root, err := agents.Root(router, toolboxes, false)
	if err != nil {
		log.Fatalf("root agent: %v", err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{
		AppName:         agents.RootName,
		Agent:           root,
		SessionService:  sessions,
		ArtifactService: artifact.InMemoryService(),
	})
	if err != nil {
		log.Fatalf("runner: %v", err)
	}

	overrides, err := pricing.ParseOverrides(settings.ModelPrices)
	if err != nil {
		log.Fatal(err)
	}
	prices, err := pricing.NewTable(overrides)
	if err != nil {
		log.Fatal(err)
	}

	var onEvent func(*session.Event)
	if *verbose {
		onEvent = func(event *session.Event) {
			var parts []string
			if event.Content != nil {
				for _, part := range event.Content.Parts {
					switch {
					case part == nil || part.Thought:
					case part.Text != "":
						parts = append(parts, fmt.Sprintf("text %d chars", len(part.Text)))
					case part.FunctionCall != nil:
						parts = append(parts, "call "+part.FunctionCall.Name)
					case part.FunctionResponse != nil:
						parts = append(parts, "result "+part.FunctionResponse.Name)
					}
				}
			}
			if grounding := event.GroundingMetadata; grounding != nil {
				log.Printf("  event %-10s grounding queries=%d chunks=%d", event.Author, len(grounding.WebSearchQueries), len(grounding.GroundingChunks))
			}
			usage := ""
			if meta := event.UsageMetadata; meta != nil {
				usage = fmt.Sprintf(" in %d cached %d out %d", meta.PromptTokenCount, meta.CachedContentTokenCount, meta.CandidatesTokenCount)
			}
			if len(parts) == 0 && usage == "" {
				return
			}
			log.Printf("  event %-10s partial=%t turn=%t %s%s", event.Author, event.Partial, event.TurnComplete, strings.Join(parts, ", "), usage)
		}
	}

	harness := &bench.Harness{
		OnEvent:  onEvent,
		Turns:    turns,
		Sessions: sessions,
		AppName:  agents.RootName,
		UserID:   "user-bench",
		State: map[string]any{
			agents.UserNameKey:       "Asha",
			agents.UserAboutKey:      "an engineer on the platform team",
			agents.UserEmailKey:      "asha@oryxa.dev",
			agents.UserRoleKey:       "admin",
			agents.OrgNameKey:        "Acme",
			agents.OrganizationIDKey: "org_bench",
			agents.AgentIDKey:        "agt_primary_bench",
			agents.AgentNameKey:      "Carl",
			agents.AgentKindKey:      "primary",
		},
		Pricing: prices,
		ModelFor: func(event *session.Event) string {
			version := event.ModelVersion
			// The lite model's name extends the main one, so match it first.
			if version != "" && strings.HasPrefix(version, settings.ModelLite) {
				return settings.ModelLite
			}
			if version != "" && strings.HasPrefix(version, settings.Model) {
				return settings.Model
			}
			if event.Author == agents.RootName {
				return settings.Model
			}
			return settings.ModelLite
		},
	}

	tasks := selectTasks(*taskNames, *withNotes, *withTeamspaces)
	log.Printf("running %d tasks x %d: main %s, lite %s, prompt cache %t (min %d tokens)",
		len(tasks), *repeat, settings.Model, settings.ModelLite, settings.Cache, settings.CacheMinTokens)

	var runs []bench.Run
	for attempt := 1; attempt <= *repeat; attempt++ {
		for _, task := range tasks {
			run, err := harness.RunTask(ctx, task, attempt)
			if err != nil {
				log.Printf("%s run %d failed: %v", task.Name, attempt, err)
				runs = append(runs, run)
				continue
			}
			runs = append(runs, run)
			if *answers && run.Answer != "" {
				log.Printf("%-8s answer: %s", task.Name, run.Answer)
			}
			log.Printf("%-8s run %d: %s, first text %s, %d in (%d cached) %d out, %s",
				task.Name, attempt, run.Latency.Round(time.Millisecond), run.TimeToFirst.Round(time.Millisecond),
				run.Usage().InputTokens, run.Usage().CachedTokens, run.Usage().OutputTokens, bench.Dollars(run.Cost.TotalMicros))
		}
	}

	summary := bench.Summarize(runs)
	bench.WriteReport(os.Stdout, runs, summary, settings.Cache)
	if teamspaces != nil {
		stats := teamspaces.Stats()
		log.Printf("whisperer collaboration: lists %d, ledger reads %d, deliberate publications %d, privacy rejections %d, model tool calls/run %.2f, service operations/run %.2f",
			stats.Lists, stats.Reads, stats.Publications, stats.PrivacyViolations,
			float64(summary.ToolCalls)/float64(len(runs)),
			float64(stats.Lists+stats.Reads+stats.Publications)/float64(len(runs)))
	}
	log.Printf("note: token usage inside sub-agent and helper runs is not visible in the event stream, so those calls may be undercounted")
}

func selectTasks(names string, withNotes, withTeamspaces bool) []bench.Task {
	available := defaultTasks
	if withNotes {
		available = append(append([]bench.Task{}, defaultTasks...), toolsTask)
	}
	if withTeamspaces {
		available = append(append([]bench.Task{}, available...), teamspaceTasks...)
	}
	names = strings.TrimSpace(names)
	if names == "" {
		return available
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(names, ",") {
		wanted[strings.TrimSpace(name)] = true
	}
	var tasks []bench.Task
	for _, task := range available {
		if wanted[task.Name] {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) == 0 {
		log.Fatalf("no tasks matched %q", names)
	}
	return tasks
}
