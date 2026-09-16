package box_test

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/box"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/box/files"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/box/folders"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/box/uploads"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/box/users"
)

func TestClientsCallBoxAsDescribed(t *testing.T) {
	type sent struct {
		method, uri, authorization, contentType string
		body                                    []byte
	}
	var mu sync.Mutex
	var requests []sent
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests = append(requests, sent{request.Method, request.RequestURI, request.Header.Get("Authorization"), request.Header.Get("Content-Type"), body})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /2.0/users/me":
			_, _ = io.WriteString(writer, `{"type":"user","id":"11446498","name":"Aaron Levie","login":"ceo@example.com"}`)
		case "GET /2.0/folders/0/items":
			_, _ = io.WriteString(writer, `{"entries":[{"type":"folder","id":"12345","name":"Contracts","etag":"1"},`+
				`{"type":"file","id":"67890","name":"Plan.pdf","size":629644}],"limit":2,"next_marker":"JV9IRGZm"}`)
		case "GET /2.0/folders/404/items":
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"type":"error","status":404,"code":"not_found","message":"Not Found","request_id":"abcdef123456"}`)
		case "PUT /2.0/files/67890":
			_, _ = io.WriteString(writer, `{"type":"file","id":"67890","name":"Plan.pdf","shared_link":null}`)
		case "POST /api/2.0/files/content":
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"total_count":1,"entries":[{"type":"file","id":"99","name":"notes.txt","size":5}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	// on is where a client built on base reaches the fake Box: base's path, on
	// the test server.
	on := func(base string) string {
		parsed, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		return server.URL + parsed.Path
	}
	bearer := func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer box_example")
		return nil
	}
	ctx := context.Background()

	userClient, err := users.NewClientWithResponses(on(box.Server), users.WithHTTPClient(server.Client()), users.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	me, err := userClient.GetUsersMeWithResponse(ctx, &users.GetUsersMeParams{Fields: &[]string{"id", "name", "login"}})
	if err != nil || me.JSON200 == nil || me.JSON200.ID != "11446498" || me.JSON200.Type != users.UserFullTypeUser ||
		me.JSON200.Login == nil || *me.JSON200.Login != "ceo@example.com" {
		t.Fatalf("GetUsersMe() = %+v %v", me, err)
	}

	folderClient, err := folders.NewClientWithResponses(on(box.Server), folders.WithHTTPClient(server.Client()), folders.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	limit, marker := int64(2), true
	items, err := folderClient.GetFoldersIDItemsWithResponse(ctx, "0", &folders.GetFoldersIDItemsParams{Limit: &limit, Usemarker: &marker})
	if err != nil || items.JSON200 == nil || items.JSON200.Entries == nil || len(*items.JSON200.Entries) != 2 ||
		items.JSON200.NextMarker == nil || *items.JSON200.NextMarker != "JV9IRGZm" {
		t.Fatalf("GetFoldersIDItems() = %+v %v", items, err)
	}
	entries := *items.JSON200.Entries
	// Every item decodes as a folder; only its Type says which it is.
	folder, errFolder := entries[0].AsFolderMini()
	notFolder, errNotFolder := entries[1].AsFolderMini()
	file, errFile := entries[1].AsFileFull()
	if errFolder != nil || !folder.Type.Valid() || folder.ID != "12345" || errNotFolder != nil || notFolder.Type.Valid() ||
		errFile != nil || !file.Type.Valid() || file.Size == nil || *file.Size != 629644 {
		t.Fatalf("items = %+v %v, %+v %v, %+v %v", folder, errFolder, notFolder, errNotFolder, file, errFile)
	}
	missing, err := folderClient.GetFoldersIDItemsWithResponse(ctx, "404", nil)
	if err != nil || missing.StatusCode() != http.StatusNotFound || missing.JSON404 == nil ||
		missing.JSON404.Code == nil || *missing.JSON404.Code != folders.ClientErrorCodeNotFound {
		t.Fatalf("GetFoldersIDItems() of a missing folder = %+v %v", missing, err)
	}

	fileClient, err := files.NewClientWithResponses(on(box.Server), files.WithHTTPClient(server.Client()), files.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	fields := &files.PutFilesIDRemoveSharedLinkParams{Fields: "shared_link"}
	// The typed body leaves the shared link out, which Box reads as no change.
	if _, err := fileClient.PutFilesIDRemoveSharedLinkWithResponse(ctx, "67890", fields, files.PutFilesIDRemoveSharedLinkJSONRequestBody{}); err != nil {
		t.Fatal(err)
	}
	removed, err := fileClient.PutFilesIDRemoveSharedLinkWithBodyWithResponse(ctx, "67890", fields, "application/json", strings.NewReader(`{"shared_link":null}`))
	if err != nil || removed.JSON200 == nil || removed.JSON200.ID != "67890" || removed.JSON200.SharedLink != nil {
		t.Fatalf("PutFilesIDRemoveSharedLink() = %+v %v", removed, err)
	}

	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	_ = form.WriteField("attributes", `{"name":"notes.txt","parent":{"id":"0"}}`)
	part, _ := form.CreateFormFile("file", "notes.txt")
	_, _ = io.WriteString(part, "hello")
	_ = form.Close()
	uploadClient, err := uploads.NewClientWithResponses(on(box.UploadServer), uploads.WithHTTPClient(server.Client()), uploads.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := uploadClient.PostFilesContentWithBodyWithResponse(ctx, nil, form.FormDataContentType(), &upload)
	if err != nil || uploaded.JSON201 == nil || uploaded.JSON201.Entries == nil || len(*uploaded.JSON201.Entries) != 1 || (*uploaded.JSON201.Entries)[0].ID != "99" {
		t.Fatalf("PostFilesContent() = %+v %v", uploaded, err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []struct{ method, uri, body string }{
		{"GET", "/2.0/users/me?fields=id,name,login", ""},
		{"GET", "/2.0/folders/0/items?usemarker=true&limit=2", ""},
		{"GET", "/2.0/folders/404/items", ""},
		{"PUT", "/2.0/files/67890?fields=shared_link", "{}"},
		{"PUT", "/2.0/files/67890?fields=shared_link", `{"shared_link":null}`},
		{"POST", "/api/2.0/files/content", ""},
	}
	if len(requests) != len(want) {
		t.Fatalf("Box was sent %d requests, want %d", len(requests), len(want))
	}
	for index, request := range requests {
		body := string(request.body)
		if want[index].method == "POST" {
			body = ""
		}
		if request.method != want[index].method || request.uri != want[index].uri || strings.TrimSpace(body) != want[index].body ||
			request.authorization != "Bearer box_example" {
			t.Errorf("request %d = %s %s %q (Authorization %q), want %s %s %q", index, request.method, request.uri, body, request.authorization,
				want[index].method, want[index].uri, want[index].body)
		}
	}
	// The upload arrives as the multipart form it was built as.
	mediaType, parameters, err := mime.ParseMediaType(requests[5].contentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("upload Content-Type = %q %v", requests[5].contentType, err)
	}
	reader := multipart.NewReader(bytes.NewReader(requests[5].body), parameters["boundary"])
	parts := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(part)
		parts[part.FormName()] = string(content)
	}
	if parts["attributes"] != `{"name":"notes.txt","parent":{"id":"0"}}` || parts["file"] != "hello" {
		t.Errorf("upload parts = %v", parts)
	}
}
