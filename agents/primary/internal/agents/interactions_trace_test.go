package agents

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// heldInteractions answers every read with the texts an answer guard held,
// as a ledger that failed to keep them from agents would.
type heldInteractions struct{ pinnedInteractions }

func (service *heldInteractions) Get(_ context.Context, _ InteractionActor, id string) (Interaction, error) {
	return Interaction{ID: id, Status: "review", Held: &InteractionHeld{
		Result: InteractionResult{Status: "completed", Summary: "Call Priya on +1 415 555 0100."},
	}}, nil
}

// TestAnAgentRequestsContentNeverReachesATrace runs a delivery through ADK
// with the process's trace pipeline: the answer an agent records, which the
// answer guard has yet to read, and whatever the ledger's reads return, go
// out of no trace, although ADK traces every tool call's arguments and
// response.
func TestAnAgentRequestsContentNeverReachesATrace(t *testing.T) {
	exported := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(observability.RedactToolCalls(exported)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	toolbox, err := InteractionToolbox(InteractionConfig{Service: &heldInteractions{}, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Done.", genai.RoleModel)
		}
		return functionCalls(
			&genai.FunctionCall{ID: "read", Name: "interactions_get", Args: map[string]any{"request_id": "irq_1"}},
			&genai.FunctionCall{ID: "answer", Name: "interactions_respond", Args: map[string]any{
				"request_id": "irq_1", "status": "completed", "summary": "Priya is at priya@example.com.", "content": "Her PIN is 4921.",
			}},
		)
	})
	deliveringHarness(t, llm, toolbox).turn("user_b", deliveryStateFor(DeliveryWorks), "Deliver irq_1.")
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := exported.GetSpans()
	toolSpans := 0
	for _, span := range spans {
		if strings.HasPrefix(span.Name, "execute_tool interactions_") {
			toolSpans++
		}
		for _, value := range span.Attributes {
			for _, secret := range []string{"priya@example.com", "4921", "415 555"} {
				if strings.Contains(value.Value.Emit(), secret) {
					t.Errorf("%s carries %s = %q", span.Name, value.Key, value.Value.Emit())
				}
			}
		}
	}
	if toolSpans == 0 {
		t.Fatalf("no interactions tool span was traced among %d spans: the test's trace pipeline is not ADK's", len(spans))
	}
}
