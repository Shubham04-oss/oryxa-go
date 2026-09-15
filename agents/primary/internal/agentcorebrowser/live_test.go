package agentcorebrowser

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

func TestLiveAgentCoreBrowser(t *testing.T) {
	if os.Getenv("AGENTS_PRIMARY_AGENTCORE_LIVE_TEST") != "1" {
		t.Skip("set AGENTS_PRIMARY_AGENTCORE_LIVE_TEST=1 to run the live AgentCore Browser smoke test")
	}

	region := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = "us-east-1"
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("load AWS configuration: %v", err)
	}
	manager, err := New(awsCfg, Options{SessionTimeout: 5 * time.Minute})
	if err != nil {
		t.Fatalf("create browser manager: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := manager.Close(cleanupCtx); err != nil {
			t.Errorf("close browser manager: %v", err)
		}
	})

	const sessionKey = "agentcore-browser-live-smoke"
	page, err := manager.Invoke(ctx, sessionKey, Request{
		Action: Navigate,
		URL:    "https://example.com/",
	})
	if err != nil {
		t.Fatalf("navigate with AgentCore Browser: %v", err)
	}
	if page.Page.URL == "" || page.Page.Title == "" || page.Page.Text == "" {
		t.Fatalf("unexpected page result: URL=%q title=%q text_length=%d", page.Page.URL, page.Page.Title, len(page.Page.Text))
	}

	shot, err := manager.Invoke(ctx, sessionKey, Request{Action: Screenshot})
	if err != nil {
		t.Fatalf("capture AgentCore Browser screenshot: %v", err)
	}
	if !bytes.HasPrefix(shot.ScreenshotPNG, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("screenshot is not PNG data: %d bytes", len(shot.ScreenshotPNG))
	}

	if err := manager.Reset(ctx, sessionKey); err != nil {
		t.Fatalf("stop AgentCore Browser session: %v", err)
	}

	t.Logf("AgentCore Browser smoke passed: region=%s title=%q url=%s screenshot_bytes=%d", region, page.Page.Title, page.Page.URL, len(shot.ScreenshotPNG))
}
