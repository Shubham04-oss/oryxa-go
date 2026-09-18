package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMembershipForwardsTheCallersCredentials(t *testing.T) {
	var gotAuthorization, gotCookie, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		gotAuthorization = request.Header.Get("Authorization")
		if cookie, err := request.Cookie(AccessCookieName); err == nil {
			gotCookie = cookie.Value
		}
		_, _ = writer.Write([]byte(`{"membership":{"user_id":"user_1","role":"admin"},"agent":false}`))
	}))
	defer server.Close()
	client := New(server.URL + "/")

	membership, err := client.Membership(context.Background(), "org 1", Credentials{Authorization: "Bearer token-a"})
	if err != nil {
		t.Fatal(err)
	}
	if membership != (Membership{OrganizationID: "org 1", UserID: "user_1", Role: "admin"}) {
		t.Fatalf("membership = %+v", membership)
	}
	if gotPath != "/v1/organizations/org 1" || gotAuthorization != "Bearer token-a" || gotCookie != "" {
		t.Fatalf("auth saw path %q, authorization %q, cookie %q", gotPath, gotAuthorization, gotCookie)
	}

	if _, err := client.Membership(context.Background(), "org_1", Credentials{AccessCookie: "token-b"}); err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "" || gotCookie != "token-b" {
		t.Fatalf("auth saw authorization %q, cookie %q", gotAuthorization, gotCookie)
	}
}

func TestMembershipReportsAnAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"membership":{"user_id":"user_1","role":"member"},"agent":true}`))
	}))
	defer server.Close()
	membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer agent"})
	if err != nil {
		t.Fatal(err)
	}
	if !membership.Agent {
		t.Fatal("an agent's token was taken for the member's own")
	}
}

func TestMembershipMapsAuthsAnswers(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, `{}`, ErrUnauthorized},
		{http.StatusForbidden, `{}`, ErrForbidden},
		{http.StatusNotFound, `{}`, ErrForbidden},
		{http.StatusTooManyRequests, `{}`, ErrRateLimited},
		{http.StatusBadGateway, `{}`, ErrUnavailable},
		// Answering without a membership is not being a member.
		{http.StatusOK, `{"membership":{}}`, ErrForbidden},
	}
	for _, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
			_, _ = writer.Write([]byte(testCase.body))
		}))
		_, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer x"})
		server.Close()
		if !errors.Is(err, testCase.want) {
			t.Errorf("status %d: error = %v, want %v", testCase.status, err, testCase.want)
		}
	}
}

func TestMembershipNeedsCredentialsAndAnAnsweringAuth(t *testing.T) {
	if _, err := New("http://127.0.0.1:1").Membership(context.Background(), "org_1", Credentials{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no credentials: error = %v, want ErrUnauthorized", err)
	}
	if _, err := New("http://127.0.0.1:1").Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("auth down: error = %v, want ErrUnavailable", err)
	}
}

func TestAccessTokenIsTheMembersOwnWhicheverWayItCame(t *testing.T) {
	cases := []struct {
		credentials Credentials
		want        string
	}{
		{Credentials{Authorization: "Bearer abc"}, "abc"},
		{Credentials{Authorization: "bearer abc"}, "abc"},
		{Credentials{AccessCookie: "from-cookie"}, "from-cookie"},
		{Credentials{Authorization: "Basic abc"}, ""},
		{Credentials{}, ""},
	}
	for _, testCase := range cases {
		if got := testCase.credentials.AccessToken(); got != testCase.want {
			t.Errorf("AccessToken(%+v) = %q, want %q", testCase.credentials, got, testCase.want)
		}
	}
}
