package google

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
)

const (
	defaultCalendarURL = "https://www.googleapis.com/calendar/v3"
	defaultGmailURL    = "https://gmail.googleapis.com/gmail/v1"
	defaultDriveURL    = "https://www.googleapis.com/drive/v3"
	maxResponseSize    = 1 << 20
	defaultTimeout     = 15 * time.Second
	primaryCalendarID  = "primary"
)

const (
	WatchKindCalendar = "calendar"
	WatchKindGmail    = "gmail"
	WatchKindDrive    = "drive"
)

var defaultHTTPClient = &http.Client{Timeout: defaultTimeout}

var (
	ErrInvalidWatch   = errors.New("invalid Google watch")
	ErrGoogleRejected = errors.New("Google rejected watch request")
)

// RejectionError exposes only Google's HTTP status. It intentionally omits
// the response body, which can contain mailbox or calendar details.
type RejectionError struct {
	StatusCode int
}

func (e RejectionError) Error() string {
	return fmt.Sprintf("%s (%d)", ErrGoogleRejected, e.StatusCode)
}

func (e RejectionError) Unwrap() error { return ErrGoogleRejected }

func IsAuthorizationRejection(err error) bool {
	var rejection RejectionError
	return errors.As(err, &rejection) && (rejection.StatusCode == http.StatusUnauthorized || rejection.StatusCode == http.StatusForbidden)
}

type CalendarWatchRequest struct {
	CalendarID string
	ChannelID  string
	Address    string
	Token      string
	Expiration time.Time
}

type GmailWatchRequest struct {
	TopicName string
}

type DriveWatchRequest struct {
	ChannelID  string
	Address    string
	Token      string
	Expiration time.Time
}

type Channel struct {
	ID         string
	ResourceID string
	Expiration time.Time
	CalendarID string
}

type GmailWatch struct {
	HistoryID  string
	Expiration time.Time
}

// WatchState is the non-secret state a Google trigger stores in the existing
// trigger runtime store. Channel tokens stay in the encrypted webhook-route
// record; Gmail has no per-trigger channel secret.
type WatchState struct {
	Kind       string
	ChannelID  string
	ResourceID string
	CalendarID string
	HistoryID  string
	ExpiresAt  time.Time
}

const (
	storeWatchKind      = "google_watch_kind"
	storeChannelID      = "google_channel_id"
	storeResourceID     = "google_resource_id"
	storeCalendarID     = "google_calendar_id"
	storeHistoryID      = "google_history_id"
	storeWatchExpiresAt = "google_watch_expires_at"
)

func (s WatchState) StoreMutations() []piece.StoreMutation {
	if !validWatchState(s) {
		return nil
	}
	mutations := []piece.StoreMutation{
		{Operation: "put", Key: storeWatchKind, Value: s.Kind},
		{Operation: "put", Key: storeWatchExpiresAt, Value: s.ExpiresAt.UTC().Format(time.RFC3339Nano)},
	}
	if s.Kind == WatchKindCalendar || s.Kind == WatchKindDrive {
		mutations = append(mutations,
			piece.StoreMutation{Operation: "put", Key: storeChannelID, Value: s.ChannelID},
			piece.StoreMutation{Operation: "put", Key: storeResourceID, Value: s.ResourceID},
		)
	}
	if s.Kind == WatchKindCalendar {
		mutations = append(mutations, piece.StoreMutation{Operation: "put", Key: storeCalendarID, Value: s.CalendarID})
	}
	if s.Kind == WatchKindGmail && s.HistoryID != "" {
		mutations = append(mutations, piece.StoreMutation{Operation: "put", Key: storeHistoryID, Value: s.HistoryID})
	}
	return mutations
}

func WatchStateFromStore(store map[string]any) (WatchState, error) {
	if store == nil {
		return WatchState{}, ErrInvalidWatch
	}
	kind, _ := store[storeWatchKind].(string)
	expiresRaw, _ := store[storeWatchExpiresAt].(string)
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresRaw)
	channelID, _ := store[storeChannelID].(string)
	resourceID, _ := store[storeResourceID].(string)
	calendarID, _ := store[storeCalendarID].(string)
	historyID, _ := store[storeHistoryID].(string)
	state := WatchState{Kind: kind, ChannelID: channelID, ResourceID: resourceID,
		CalendarID: calendarID, HistoryID: historyID, ExpiresAt: expiresAt.UTC()}
	if err != nil || !validWatchState(state) {
		return WatchState{}, ErrInvalidWatch
	}
	return state, nil
}

type Client struct {
	HTTPClient *http.Client
	BaseURL    string
	Now        func() time.Time
}

func (c Client) WatchCalendar(ctx context.Context, accessToken string, request CalendarWatchRequest) (Channel, error) {
	if err := validateCalendarWatch(request, c.now()); err != nil {
		return Channel{}, err
	}
	body, err := json.Marshal(struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Address    string `json:"address"`
		Token      string `json:"token"`
		Expiration string `json:"expiration"`
	}{
		ID: request.ChannelID, Type: "web_hook", Address: request.Address, Token: request.Token,
		Expiration: strconv.FormatInt(request.Expiration.UTC().UnixMilli(), 10),
	})
	if err != nil {
		return Channel{}, err
	}
	path := "/calendars/" + url.PathEscape(request.CalendarID) + "/events/watch"
	channel, err := c.calendarRequest(ctx, path, accessToken, body)
	if err != nil {
		return Channel{}, err
	}
	channel.CalendarID = request.CalendarID
	return channel, nil
}

func (c Client) WatchDrive(ctx context.Context, accessToken string, request DriveWatchRequest) (Channel, error) {
	if err := validateDriveWatch(request, c.now()); err != nil {
		return Channel{}, err
	}
	pageToken, err := c.driveStartPageToken(ctx, accessToken)
	if err != nil {
		return Channel{}, err
	}
	body, err := json.Marshal(struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Address    string `json:"address"`
		Token      string `json:"token"`
		Expiration string `json:"expiration"`
	}{
		ID: request.ChannelID, Type: "web_hook", Address: request.Address, Token: request.Token,
		Expiration: strconv.FormatInt(request.Expiration.UTC().UnixMilli(), 10),
	})
	if err != nil {
		return Channel{}, err
	}
	path := "/changes/watch?pageToken=" + url.QueryEscape(pageToken) + "&supportsAllDrives=true"
	return c.driveRequest(ctx, path, accessToken, body)
}

func (c Client) StopChannel(ctx context.Context, accessToken string, state WatchState) error {
	if !validChannelID(state.ChannelID) || !validResourceID(state.ResourceID) || strings.TrimSpace(accessToken) == "" {
		return ErrInvalidWatch
	}
	var stopURL string
	switch state.Kind {
	case WatchKindCalendar:
		stopURL = c.calendarBase() + "/channels/stop"
	case WatchKindDrive:
		stopURL = c.driveBase() + "/channels/stop"
	default:
		return ErrInvalidWatch
	}
	body, err := json.Marshal(struct {
		ID         string `json:"id"`
		ResourceID string `json:"resourceId"`
	}{ID: state.ChannelID, ResourceID: state.ResourceID})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, stopURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	c.authorize(request, accessToken)
	request.Header.Set("content-type", "application/json")
	response, err := c.client().Do(request)
	if err != nil {
		return fmt.Errorf("stop Google Calendar channel: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return googleError(response)
}

func (c Client) WatchGmail(ctx context.Context, accessToken string, request GmailWatchRequest) (GmailWatch, error) {
	if !validPubSubTopic(request.TopicName) || strings.TrimSpace(accessToken) == "" {
		return GmailWatch{}, ErrInvalidWatch
	}
	body, err := json.Marshal(struct {
		TopicName           string   `json:"topicName"`
		LabelIDs            []string `json:"labelIds"`
		LabelFilterBehavior string   `json:"labelFilterBehavior"`
	}{TopicName: request.TopicName, LabelIDs: []string{"INBOX"}, LabelFilterBehavior: "INCLUDE"})
	if err != nil {
		return GmailWatch{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gmailBase()+"/users/me/watch", strings.NewReader(string(body)))
	if err != nil {
		return GmailWatch{}, err
	}
	c.authorize(httpRequest, accessToken)
	httpRequest.Header.Set("content-type", "application/json")
	response, err := c.client().Do(httpRequest)
	if err != nil {
		return GmailWatch{}, fmt.Errorf("create Gmail watch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GmailWatch{}, googleError(response)
	}
	var payload struct {
		HistoryID  json.Number `json:"historyId"`
		Expiration string      `json:"expiration"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize+1))
	if err := decoder.Decode(&payload); err != nil {
		return GmailWatch{}, ErrInvalidWatch
	}
	expiresAt, err := parseUnixMilli(payload.Expiration)
	historyID := payload.HistoryID.String()
	if err != nil || !validHistoryID(historyID) {
		return GmailWatch{}, ErrInvalidWatch
	}
	return GmailWatch{HistoryID: historyID, Expiration: expiresAt}, nil
}

func (c Client) StopGmail(ctx context.Context, accessToken string) error {
	if strings.TrimSpace(accessToken) == "" {
		return ErrInvalidWatch
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gmailBase()+"/users/me/stop", nil)
	if err != nil {
		return err
	}
	c.authorize(request, accessToken)
	response, err := c.client().Do(request)
	if err != nil {
		return fmt.Errorf("stop Gmail watch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return googleError(response)
}

func (c Client) calendarRequest(ctx context.Context, path, accessToken string, body []byte) (Channel, error) {
	if strings.TrimSpace(accessToken) == "" {
		return Channel{}, ErrInvalidWatch
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.calendarBase()+path, strings.NewReader(string(body)))
	if err != nil {
		return Channel{}, err
	}
	c.authorize(request, accessToken)
	request.Header.Set("content-type", "application/json")
	response, err := c.client().Do(request)
	if err != nil {
		return Channel{}, fmt.Errorf("create Google Calendar watch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Channel{}, googleError(response)
	}
	var payload struct {
		ID         string `json:"id"`
		ResourceID string `json:"resourceId"`
		Expiration string `json:"expiration"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize+1))
	if err := decoder.Decode(&payload); err != nil {
		return Channel{}, ErrInvalidWatch
	}
	expiresAt, err := parseUnixMilli(payload.Expiration)
	if err != nil || !validChannelID(payload.ID) || !validResourceID(payload.ResourceID) {
		return Channel{}, ErrInvalidWatch
	}
	return Channel{ID: payload.ID, ResourceID: payload.ResourceID, Expiration: expiresAt}, nil
}

func (c Client) driveRequest(ctx context.Context, path, accessToken string, body []byte) (Channel, error) {
	if strings.TrimSpace(accessToken) == "" {
		return Channel{}, ErrInvalidWatch
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.driveBase()+path, strings.NewReader(string(body)))
	if err != nil {
		return Channel{}, err
	}
	c.authorize(request, accessToken)
	request.Header.Set("content-type", "application/json")
	response, err := c.client().Do(request)
	if err != nil {
		return Channel{}, fmt.Errorf("create Google Drive watch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Channel{}, googleError(response)
	}
	var payload struct {
		ID         string `json:"id"`
		ResourceID string `json:"resourceId"`
		Expiration string `json:"expiration"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize+1))
	if err := decoder.Decode(&payload); err != nil {
		return Channel{}, ErrInvalidWatch
	}
	expiresAt, err := parseUnixMilli(payload.Expiration)
	if err != nil || !validChannelID(payload.ID) || !validResourceID(payload.ResourceID) {
		return Channel{}, ErrInvalidWatch
	}
	return Channel{ID: payload.ID, ResourceID: payload.ResourceID, Expiration: expiresAt}, nil
}

func (c Client) driveStartPageToken(ctx context.Context, accessToken string) (string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", ErrInvalidWatch
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.driveBase()+"/changes/startPageToken?supportsAllDrives=true", nil)
	if err != nil {
		return "", err
	}
	c.authorize(request, accessToken)
	response, err := c.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("read Drive start page token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", googleError(response)
	}
	var payload struct {
		StartPageToken string `json:"startPageToken"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize+1))
	if err := decoder.Decode(&payload); err != nil || payload.StartPageToken == "" ||
		len(payload.StartPageToken) > 256 || strings.ContainsAny(payload.StartPageToken, "\r\n\t ") {
		return "", ErrInvalidWatch
	}
	return payload.StartPageToken, nil
}

func (c Client) authorize(request *http.Request, accessToken string) {
	request.Header.Set("authorization", "Bearer "+accessToken)
	request.Header.Set("accept", "application/json")
}

func (c Client) calendarBase() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(c.BaseURL, "/") + "/calendar/v3"
	}
	return defaultCalendarURL
}

func (c Client) gmailBase() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(c.BaseURL, "/") + "/gmail/v1"
	}
	return defaultGmailURL
}

func (c Client) driveBase() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimRight(c.BaseURL, "/") + "/drive/v3"
	}
	return defaultDriveURL
}

func (c Client) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTPClient
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func googleError(response *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8*1024))
	return RejectionError{StatusCode: response.StatusCode}
}

func NewChannelID(source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	bytes := make([]byte, 18)
	if _, err := io.ReadFull(source, bytes); err != nil {
		return "", fmt.Errorf("generate Google channel id: %w", err)
	}
	return "c" + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func NewChannelToken(source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(source, bytes); err != nil {
		return "", fmt.Errorf("generate Google channel token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func ValidPubSubTopic(value string) bool {
	return validPubSubTopic(value)
}

func validPubSubTopic(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	project, topic, ok := strings.Cut(strings.TrimPrefix(value, "projects/"), "/topics/")
	return ok && project != "" && topic != "" && !strings.ContainsAny(project+topic, " \r\n\t")
}

func validateCalendarWatch(request CalendarWatchRequest, now time.Time) error {
	if !validCalendarID(request.CalendarID) || !validChannelID(request.ChannelID) ||
		!validHTTPSURL(request.Address) || len(request.Token) < 32 || len(request.Token) > 256 ||
		request.Expiration.Before(now.Add(time.Minute)) {
		return ErrInvalidWatch
	}
	return nil
}

func validateDriveWatch(request DriveWatchRequest, now time.Time) error {
	if !validChannelID(request.ChannelID) || !validHTTPSURL(request.Address) ||
		len(request.Token) < 32 || len(request.Token) > 256 || request.Expiration.Before(now.Add(time.Minute)) ||
		request.Expiration.After(now.Add(25*time.Hour)) {
		return ErrInvalidWatch
	}
	return nil
}

func validWatchState(state WatchState) bool {
	if state.ExpiresAt.IsZero() {
		return false
	}
	switch state.Kind {
	case WatchKindCalendar:
		return validChannelID(state.ChannelID) && validResourceID(state.ResourceID) && validCalendarID(state.CalendarID)
	case WatchKindDrive:
		return validChannelID(state.ChannelID) && validResourceID(state.ResourceID)
	case WatchKindGmail:
		return state.HistoryID == "" || validHistoryID(state.HistoryID)
	default:
		return false
	}
}

func validCalendarID(value string) bool {
	return value == primaryCalendarID || (boundedToken(value, 1024) && !strings.ContainsAny(value, "/\\"))
}

func validChannelID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validResourceID(value string) bool {
	return boundedToken(value, 128)
}

func validHistoryID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func boundedToken(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}

func parseUnixMilli(value string) (time.Time, error) {
	millis, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || millis < 1 {
		return time.Time{}, ErrInvalidWatch
	}
	return time.UnixMilli(millis).UTC(), nil
}

func validHTTPSURL(value string) bool {
	request, err := http.NewRequest(http.MethodGet, value, nil)
	return err == nil && request.URL.Scheme == "https" && request.URL.Host != "" && request.URL.User == nil && request.URL.Fragment == ""
}
