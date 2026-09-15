package figma_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/figma"
)

func TestClientCallsFigmaAsDescribed(t *testing.T) {
	var posted, query string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		// Personal access tokens go in X-Figma-Token.
		if request.Header.Get("X-Figma-Token") != "figd_example" {
			writer.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(writer, `{"status":403,"error":true,"message":"Invalid token"}`)
			return
		}
		comment := func(id, message string) string {
			return `{"id":"` + id + `","file_key":"FILE123","user":{"id":"12345","handle":"ada","img_url":"https://example.test/ada.png"},` +
				`"created_at":"2026-09-15T10:00:00Z","resolved_at":null,"message":"` + message + `","client_meta":{"x":10,"y":20},"order_id":"1","reactions":[]}`
		}
		switch request.Method + " " + request.URL.Path {
		case "GET /v1/me":
			_, _ = io.WriteString(writer, `{"id":"12345","handle":"ada","img_url":"https://example.test/ada.png","email":"ada@example.test"}`)
		case "GET /v1/files/FILE123/comments":
			query = request.URL.RawQuery
			_, _ = io.WriteString(writer, `{"comments":[`+comment("c1", "Looks good")+`]}`)
		case "POST /v1/files/FILE123/comments":
			body, _ := io.ReadAll(request.Body)
			posted = string(body)
			_, _ = io.WriteString(writer, comment("c2", "Ship it"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx := context.Background()
	token := func(_ context.Context, request *http.Request) error {
		request.Header.Set("X-Figma-Token", "figd_example")
		return nil
	}
	client, err := figma.NewClientWithResponses(server.URL, figma.WithHTTPClient(server.Client()), figma.WithRequestEditorFn(token))
	if err != nil {
		t.Fatal(err)
	}

	me, err := client.GetMeWithResponse(ctx)
	if err != nil || me.JSON200 == nil || me.JSON200.ID != "12345" || me.JSON200.Email != "ada@example.test" || me.JSON200.Handle != "ada" {
		t.Fatalf("GetMe() = %+v %v", me, err)
	}
	markdown := true
	comments, err := client.GetCommentsWithResponse(ctx, "FILE123", &figma.GetCommentsParams{AsMd: &markdown})
	if err != nil || comments.JSON200 == nil || len(comments.JSON200.Comments) != 1 || comments.JSON200.Comments[0].Message != "Looks good" ||
		comments.JSON200.Comments[0].User.Handle != "ada" || query != "as_md=true" {
		t.Fatalf("GetComments() = %+v %v, query %q", comments, err, query)
	}
	created, err := client.PostCommentWithResponse(ctx, "FILE123", figma.PostCommentJSONRequestBody{Message: "Ship it"})
	if err != nil || created.JSON200 == nil || created.JSON200.ID != "c2" || strings.TrimSpace(posted) != `{"message":"Ship it"}` {
		t.Fatalf("PostComment() = %+v %v, sent %s", created, err, posted)
	}

	// A request Figma refuses decodes as its error response.
	anonymous, err := figma.NewClientWithResponses(server.URL, figma.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	if refused, err := anonymous.GetMeWithResponse(ctx); err != nil || refused.StatusCode() != http.StatusForbidden || refused.JSON403 == nil {
		t.Fatalf("GetMe() without a token = %+v %v", refused, err)
	}
}
