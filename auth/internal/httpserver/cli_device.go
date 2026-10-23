package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// cliDeviceGrants is what the approve page tells the member they hand over.
const cliDeviceGrants = "Workspace setup for one organization you administer, for at most 15 minutes. It cannot read conversations, files or knowledge, act as your agents, or accept invitations."

func (server *Server) cliDevicesEnabled() bool {
	return server.config.CLIClientID != "" && server.config.CLISessions != nil && server.config.CLIDevices != nil && server.config.CLIDeviceURL != ""
}

// cliDevice starts a device sign-in (RFC 8628 §3.1-3.2).
func (server *Server) cliDevice(writer http.ResponseWriter, request *http.Request) {
	if !server.cliDevicesEnabled() {
		writeError(writer, http.StatusNotFound, "CLI device sign-in is not configured")
		return
	}
	if request.Header.Get("Origin") != "" && !server.validMutationOrigin(request) {
		writeError(writer, http.StatusForbidden, "origin is not allowed")
		return
	}
	if !server.cliDeviceLimits.allow(request.RemoteAddr, time.Now()) {
		server.handleError(writer, domain.ErrRateLimited)
		return
	}
	var input struct {
		ClientName string `json:"client_name"`
	}
	if request.ContentLength != 0 {
		if err := readJSON(request, &input); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	deviceCode, userCode := clisession.NewDeviceCode(), clisession.NewUserCode()
	device := clisession.Device{
		DeviceCodeHash: clisession.Hash(deviceCode),
		UserCodeHash:   clisession.Hash(userCode),
		ClientName:     deviceClientName(input.ClientName),
		ClientAddress:  peerHost(request.RemoteAddr),
		ExpiresAt:      time.Now().UTC().Add(clisession.DeviceTTL),
		Interval:       clisession.DeviceInterval,
	}
	if err := server.config.CLIDevices.CreateCLIDevice(request.Context(), device); err != nil {
		server.handleError(writer, err)
		return
	}
	complete := server.config.CLIDeviceURL + "?" + url.Values{"code": {userCode}}.Encode()
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          server.config.CLIDeviceURL,
		"verification_uri_complete": complete,
		"expires_in":                int(clisession.DeviceTTL / time.Second),
		"interval":                  int(clisession.DeviceInterval / time.Second),
	})
}

// cliDeviceToken is the CLI's poll (RFC 8628 §3.4-3.5). Until a member
// decides it answers 400 with an RFC error code; once approved it issues an
// opaque setup token, admitted like the browser flow's for 15 minutes.
func (server *Server) cliDeviceToken(writer http.ResponseWriter, request *http.Request) {
	if !server.cliDevicesEnabled() {
		writeError(writer, http.StatusNotFound, "CLI device sign-in is not configured")
		return
	}
	if request.Header.Get("Origin") != "" {
		writeError(writer, http.StatusForbidden, "device tokens are for native clients")
		return
	}
	if !server.cliPollLimits.allow(request.RemoteAddr, time.Now()) {
		server.handleError(writer, domain.ErrRateLimited)
		return
	}
	var input struct {
		GrantType  string `json:"grant_type"`
		DeviceCode string `json:"device_code"`
	}
	if err := readJSON(request, &input); err != nil || input.DeviceCode == "" || (input.GrantType != "" && input.GrantType != deviceGrantType) {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	token := clisession.NewDeviceToken()
	expires := time.Now().UTC().Add(clisession.SessionTTL)
	poll, err := server.config.CLIDevices.PollCLIDevice(request.Context(), clisession.Hash(input.DeviceCode), clisession.Hash(token), expires)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	if poll.Status != "" {
		body := map[string]any{"error": poll.Status}
		if poll.Status == clisession.PollSlowDown || poll.Status == clisession.PollPending {
			body["interval"] = int(poll.Interval / time.Second)
		}
		writeJSON(writer, http.StatusBadRequest, body)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(clisession.SessionTTL / time.Second),
		"expires_at":   expires,
	})
}

type userCodeInput struct {
	UserCode string `json:"user_code"`
}

// readUserCode is shared by the member's three routes. A wrong code costs a
// peer the same budget as a right one, so codes cannot be enumerated.
func (server *Server) readUserCode(writer http.ResponseWriter, request *http.Request) (string, bool) {
	if !server.cliDevicesEnabled() {
		writeError(writer, http.StatusNotFound, "CLI device sign-in is not configured")
		return "", false
	}
	if !server.cliDecisionLimits.allow(request.RemoteAddr, time.Now()) {
		server.handleError(writer, domain.ErrRateLimited)
		return "", false
	}
	var input userCodeInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return "", false
	}
	code := clisession.NormalizeUserCode(input.UserCode)
	if code == "" {
		writeError(writer, http.StatusNotFound, "this code is not valid or has expired")
		return "", false
	}
	return code, true
}

// cliDeviceLookup shows the signed-in member who is asking and what they get
// before they decide, so a code pasted from someone else's message stands out.
func (server *Server) cliDeviceLookup(writer http.ResponseWriter, request *http.Request) {
	code, ok := server.readUserCode(writer, request)
	if !ok {
		return
	}
	device, err := server.config.CLIDevices.CLIDevice(request.Context(), clisession.Hash(code))
	if err != nil {
		server.writeDeviceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"user_code":      code,
		"client_name":    device.ClientName,
		"client_address": device.ClientAddress,
		"requested_at":   device.CreatedAt,
		"expires_at":     device.ExpiresAt,
		"grants":         cliDeviceGrants,
	})
}

func (server *Server) cliDeviceApprove(writer http.ResponseWriter, request *http.Request) {
	server.cliDeviceDecide(writer, request, true)
}

func (server *Server) cliDeviceDeny(writer http.ResponseWriter, request *http.Request) {
	server.cliDeviceDecide(writer, request, false)
}

func (server *Server) cliDeviceDecide(writer http.ResponseWriter, request *http.Request, approve bool) {
	code, ok := server.readUserCode(writer, request)
	if !ok {
		return
	}
	authenticated := requestAuth(request)
	if err := server.config.CLIDevices.DecideCLIDevice(request.Context(), clisession.Hash(code), authenticated.Claims.Subject, approve); err != nil {
		server.writeDeviceError(writer, err)
		return
	}
	status := "approved"
	if !approve {
		status = "denied"
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": status})
}

func (server *Server) writeDeviceError(writer http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "this code is not valid or has expired")
		return
	}
	server.handleError(writer, err)
}

// deviceTokenClaims answers for an opaque token a device sign-in issued, as
// the verifier answers for a Cognito CLI token: the CLI client's, so
// cliscope and authorizeCLI apply to it unchanged.
func (server *Server) deviceTokenClaims(ctx context.Context, rawToken string) (authn.Claims, error) {
	if !server.cliDevicesEnabled() {
		return authn.Claims{}, domain.ErrUnauthorized
	}
	session, err := server.config.CLISessions.CLISession(ctx, clisession.Hash(rawToken))
	if err != nil {
		return authn.Claims{}, err
	}
	if session.Subject == "" {
		return authn.Claims{}, domain.ErrUnauthorized
	}
	return authn.Claims{Subject: session.Subject, ClientID: server.config.CLIClientID, ExpiresAt: session.ExpiresAt.Unix(), TokenUse: "access"}, nil
}

func deviceClientName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "Oryxa CLI"
	}
	for utf8.RuneCountInString(name) > 80 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

func peerHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}
