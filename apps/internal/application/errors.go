package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// maxComposioMessage bounds how much of Composio's own wording a caller sees.
const maxComposioMessage = 300

// composioError says what a failed Composio call means in apps' terms.
// Composio's message is kept for a refused request, because it is usually
// about the caller's own input, such as an argument a tool does not take;
// anything else keeps only its kind.
func composioError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, composio.ErrRateLimited):
		return fmt.Errorf("%s: %w", action, domain.ErrRateLimited)
	case errors.Is(err, composio.ErrUnavailable):
		return fmt.Errorf("%s: Composio did not answer: %w", action, domain.ErrUnavailable)
	case errors.Is(err, composio.ErrUnauthorized):
		// The project's own key was refused: nothing the caller can fix.
		return fmt.Errorf("%s: Composio refused apps' API key: %w", action, domain.ErrUnavailable)
	case errors.Is(err, composio.ErrNotFound):
		return fmt.Errorf("%s: %w", action, domain.ErrNotFound)
	case errors.Is(err, composio.ErrForbidden):
		return fmt.Errorf("%s: %w", action, domain.ErrForbidden)
	default:
		var failure *composio.Error
		if errors.As(err, &failure) && failure.Message != "" {
			return fmt.Errorf("%s: %s: %w", action, truncate(failure.Message, maxComposioMessage), domain.ErrInvalid)
		}
		return fmt.Errorf("%s: %w", action, domain.ErrInvalid)
	}
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "…"
}
