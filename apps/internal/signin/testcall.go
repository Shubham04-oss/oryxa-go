package signin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
)

var (
	// errTestRefused is a test call the app answered, but not as a working
	// credential answers: a 401 or another refusal, or an answer that names
	// no account.
	errTestRefused = errors.New("the app refused the credential in its test call")
	// errTestUnavailable is a test call the app did not answer, or answered
	// with a status that says to try again later.
	errTestUnavailable = errors.New("the app did not answer the test call")
)

const (
	// maxLabelRunes is how much of an account's name is kept.
	maxLabelRunes = 200
	// maxRefBytes bounds an account's ref, which routes pushed events and
	// is never cut short.
	maxRefBytes = 512
)

// runTest makes the manifest's test call with held: a GET of test.path under
// api_base, with the test's fixed headers. It passes when the app answers
// 2xx and the label pointer, and the account_ref pointer when there is one,
// resolve to values. Some apps refuse with a 200 (Slack's "ok": false), so a
// label that does not resolve fails.
func (s *Service) runTest(ctx context.Context, p *prepared, held material) (Account, error) {
	test := p.manifest.Auth.Test
	path, err := fill(test.Path, p.values, false)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %v", errTestRefused, err)
	}
	target, err := under(p.base, path)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %v", errTestRefused, err)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %v", errTestRefused, err)
	}
	for name, value := range test.Headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("Accept", "application/json")
	client := s.apiClient(&credentialTransport{
		next: s.base, prepared: p,
		material: func(context.Context) (material, error) { return held, nil },
	})
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, safehttp.ErrRefused) || errors.Is(err, safehttp.ErrScheme) {
			// A filled URL that reaches somewhere it must not is the
			// credential's fields' fault, not the app's.
			return Account{}, fmt.Errorf("%w: its URL is not on the public internet over https", errTestRefused)
		}
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(ctxErr, context.DeadlineExceeded) {
			return Account{}, ctxErr
		}
		return Account{}, fmt.Errorf("%w: %v", errTestUnavailable, err)
	}
	defer response.Body.Close()
	switch status := response.StatusCode; {
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return Account{}, fmt.Errorf("%w: it answered %d", errTestUnavailable, status)
	case status < 200 || status >= 300:
		return Account{}, fmt.Errorf("%w: it answered %d", errTestRefused, status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	if err != nil {
		return Account{}, fmt.Errorf("%w: reading its answer: %v", errTestUnavailable, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return Account{}, fmt.Errorf("%w: it answered no JSON", errTestRefused)
	}
	label, found := scalar(pointer(document, test.Label))
	if !found {
		return Account{}, fmt.Errorf("%w: its answer names no account at %s", errTestRefused, test.Label)
	}
	account := Account{Label: truncateRunes(label, maxLabelRunes)}
	if test.AccountRef != "" {
		ref, found := scalar(pointer(document, test.AccountRef))
		if !found || len(ref) > maxRefBytes {
			return Account{}, fmt.Errorf("%w: its answer names no account ref at %s", errTestRefused, test.AccountRef)
		}
		account.Ref = ref
	}
	return account, nil
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
