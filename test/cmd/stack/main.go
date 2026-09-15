// Command stack supervises the k6 journey: seeds the simulated
// organization, serves the auth stub, and runs teams, files and email. It is
// not a product service.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/test/identities"
	"github.com/Shubham04-oss/oryxa-go/test/internal/authstub"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	organizationID := required("ORG_ID")
	databaseURL := required("DATABASE_URL")
	awsEndpoint := required("AWS_ENDPOINT_URL")
	sesEndpoint := value("AWS_ENDPOINT_URL_SESV2", "http://localhost:4567")
	smtpAddr := value("EMAIL_SMTP_ADDR", "127.0.0.1:1025")
	authAddr := value("AUTHSTUB_ADDRESS", "127.0.0.1:18080")
	teamsAddr := value("TEAMS_ADDRESS", "127.0.0.1:18083")
	filesAddr := value("FILES_ADDRESS", "127.0.0.1:18085")
	if err := seedAuth(ctx, databaseURL, organizationID); err != nil {
		return err
	}

	if err := serve(ctx, authAddr, authstub.Handler()); err != nil {
		return err
	}

	queueURL, err := provisionEmail(ctx, root, awsEndpoint, sesEndpoint)
	if err != nil {
		return err
	}
	filesProvision, err := provisionFiles(ctx, root, awsEndpoint)
	if err != nil {
		return err
	}

	teamsBin, err := build(ctx, root, "teams", "./teams/cmd/teams")
	if err != nil {
		return err
	}
	filesBin, err := build(ctx, root, "files", "./files/cmd/files")
	if err != nil {
		return err
	}
	emailBin, err := build(ctx, root, "email", "./email/cmd/email")
	if err != nil {
		return err
	}
	awsLocal := map[string]string{
		"AWS_REGION":                   "ap-south-1",
		"AWS_ACCESS_KEY_ID":            "test",
		"AWS_SECRET_ACCESS_KEY":        "test",
		"AWS_ENDPOINT_URL":             awsEndpoint,
		"AWS_ENDPOINT_URL_EVENTBRIDGE": awsEndpoint,
		"AWS_ENDPOINT_URL_SQS":         awsEndpoint,
		"AWS_ENDPOINT_URL_DYNAMODB":    awsEndpoint,
		"EVENT_BUS_NAME":               "default",
	}
	teams := command(ctx, teamsBin, mergeEnv(awsLocal, map[string]string{
		"DATABASE_URL":       databaseURL,
		"AUTH_URL":           "http://" + authAddr,
		"TEAMS_ADDRESS":      teamsAddr,
		"TEAMS_AUTO_MIGRATE": "true",
	}))
	files := command(ctx, filesBin, mergeEnv(awsLocal, map[string]string{
		"DATABASE_URL":           databaseURL,
		"AUTH_URL":               "http://" + authAddr,
		"TEAMS_URL":              "http://" + teamsAddr,
		"FILES_ADDRESS":          filesAddr,
		"FILES_AUTO_MIGRATE":     "true",
		"FILES_BUCKET_NAME":      filesProvision.BucketName,
		"FILES_EVENTS_QUEUE_URL": filesProvision.QueueURL,
	}))
	email := command(ctx, emailBin, mergeEnv(awsLocal, map[string]string{
		"EMAIL_FROM_ADDRESS":       "no-reply@example.com",
		"EMAIL_FROM_NAME":          "Oryxa",
		"EMAIL_QUEUE_URL":          queueURL,
		"EMAIL_SMTP_ADDR":          smtpAddr,
		"EMAIL_DELIVERIES_TABLE":   "email-deliveries",
		"EMAIL_SUPPRESSIONS_TABLE": "email-suppressions",
		"AWS_ENDPOINT_URL_SESV2":   sesEndpoint,
	}))
	if err := teams.Start(); err != nil {
		return fmt.Errorf("start teams: %w", err)
	}
	if err := files.Start(); err != nil {
		return fmt.Errorf("start files: %w", err)
	}
	if err := email.Start(); err != nil {
		return fmt.Errorf("start email: %w", err)
	}
	go waitLogged("teams", teams)
	go waitLogged("files", files)
	go waitLogged("email", email)

	log.Printf("stack ready org_id=%s auth=http://%s teams=http://%s files=http://%s mailpit=http://127.0.0.1:8025", organizationID, authAddr, teamsAddr, filesAddr)
	<-ctx.Done()
	return nil
}

func seedAuth(ctx context.Context, databaseURL, organizationID string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS auth_users (
		    id text PRIMARY KEY,
		    email text NOT NULL,
		    display_name text NOT NULL DEFAULT '',
		    avatar_url text NOT NULL DEFAULT '',
		    created_at timestamptz NOT NULL,
		    updated_at timestamptz NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS auth_users_email_unique ON auth_users (lower(email));
		CREATE TABLE IF NOT EXISTS auth_organizations (
		    id text PRIMARY KEY,
		    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
		    slug text NOT NULL CHECK (length(slug) BETWEEN 2 AND 63),
		    created_by text NOT NULL REFERENCES auth_users(id),
		    created_at timestamptz NOT NULL,
		    updated_at timestamptz NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS auth_organizations_slug_unique ON auth_organizations (lower(slug));
		CREATE TABLE IF NOT EXISTS auth_memberships (
		    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
		    user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
		    role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
		    joined_at timestamptz NOT NULL,
		    PRIMARY KEY (organization_id, user_id)
		)`); err != nil {
		return fmt.Errorf("ensure auth tables: %w", err)
	}

	now := time.Now().UTC()
	for _, person := range identities.OrgMembers() {
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth_users (id, email, display_name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (id) DO UPDATE SET email = EXCLUDED.email, display_name = EXCLUDED.display_name, updated_at = EXCLUDED.updated_at`,
			person.UserID, person.Email, person.DisplayName, now); err != nil {
			return fmt.Errorf("seed user %s: %w", person.UserID, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at)
		VALUES ($1, 'Simulated Org', $2, $3, $4, $4)
		ON CONFLICT (id) DO NOTHING`, organizationID, slug(organizationID), identities.People["owner"].UserID, now); err != nil {
		return fmt.Errorf("seed organization: %w", err)
	}
	for _, person := range identities.OrgMembers() {
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth_memberships (organization_id, user_id, role, joined_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (organization_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
			organizationID, person.UserID, person.Role, now); err != nil {
			return fmt.Errorf("seed membership %s: %w", person.UserID, err)
		}
	}
	return nil
}

func build(ctx context.Context, root, name, packagePath string) (string, error) {
	bin := filepath.Join(os.TempDir(), "oryxa-stack-"+name)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, packagePath)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build %s: %w", name, err)
	}
	return bin, nil
}

func provisionFiles(ctx context.Context, root, awsEndpoint string) (struct {
	QueueURL   string `json:"queue_url"`
	BucketName string `json:"bucket_name"`
}, error) {
	var result struct {
		QueueURL   string `json:"queue_url"`
		BucketName string `json:"bucket_name"`
	}
	bin, err := build(ctx, root, "files-provision", "./files/infra/storage/cmd/provision")
	if err != nil {
		return result, err
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = root
	cmd.Env = isolatedEnv(map[string]string{
		"AWS_REGION":              "ap-south-1",
		"AWS_ACCESS_KEY_ID":       "test",
		"AWS_SECRET_ACCESS_KEY":   "test",
		"AWS_ENDPOINT_URL":        awsEndpoint,
		"EVENT_BUS_NAME":          "default",
		"FILES_BUCKET_NAME":       "oryxa-files",
		"FILES_EVENTS_QUEUE_NAME": "files-events",
	})
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return result, fmt.Errorf("provision files: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return result, fmt.Errorf("provision files: %w", err)
	}
	if err := json.Unmarshal(out, &result); err != nil || result.QueueURL == "" || result.BucketName == "" {
		return result, fmt.Errorf("provision files: decode output: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	log.Printf("files bucket %s queue %s", result.BucketName, result.QueueURL)
	return result, nil
}

func provisionEmail(ctx context.Context, root, awsEndpoint, sesEndpoint string) (string, error) {
	bin, err := build(ctx, root, "email-provision", "./email/infra/delivery/cmd/provision")
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = root
	cmd.Env = isolatedEnv(map[string]string{
		"AWS_REGION":                   "ap-south-1",
		"AWS_ACCESS_KEY_ID":            "test",
		"AWS_SECRET_ACCESS_KEY":        "test",
		"AWS_ENDPOINT_URL":             awsEndpoint,
		"AWS_ENDPOINT_URL_EVENTBRIDGE": awsEndpoint,
		"AWS_ENDPOINT_URL_SQS":         awsEndpoint,
		"AWS_ENDPOINT_URL_DYNAMODB":    awsEndpoint,
		"AWS_ENDPOINT_URL_SESV2":       sesEndpoint,
		"EMAIL_FROM_ADDRESS":           "no-reply@example.com",
		"SES_EVENT_PUBLISHING":         "false",
		"EVENT_BUS_NAME":               "default",
		"EMAIL_QUEUE_NAME":             "email-events",
		"EMAIL_DELIVERIES_TABLE":       "email-deliveries",
		"EMAIL_SUPPRESSIONS_TABLE":     "email-suppressions",
	})
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("provision email: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("provision email: %w", err)
	}
	var result struct {
		QueueURL string `json:"queue_url"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.QueueURL == "" {
		return "", fmt.Errorf("provision email: decode queue_url: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	log.Printf("email queue %s", result.QueueURL)
	return result.QueueURL, nil
}

func mergeEnv(parts ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, part := range parts {
		for key, value := range part {
			merged[key] = value
		}
	}
	return merged
}

func command(ctx context.Context, bin string, env map[string]string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = isolatedEnv(env)
	return cmd
}

func isolatedEnv(extra map[string]string) []string {
	keep := []string{"PATH", "HOME", "TMPDIR", "GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN"}
	env := make([]string, 0, len(keep)+len(extra))
	for _, name := range keep {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	for name, value := range extra {
		env = append(env, name+"="+value)
	}
	return env
}

func waitLogged(name string, cmd *exec.Cmd) {
	if err := cmd.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("%s stopped: %v", name, err)
	}
}

func serve(ctx context.Context, address string, handler http.Handler) error {
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("listen %s: %v", address, err)
		}
	}()
	return nil
}

func slug(organizationID string) string {
	label := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return -1
		}
	}, organizationID)
	if len(label) < 2 {
		return "simorg"
	}
	if len(label) > 63 {
		return label[:63]
	}
	return strings.ToLower(label)
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func required(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		log.Fatalf("%s is required", name)
	}
	return value
}

func value(name, fallback string) string {
	if result := strings.TrimSpace(os.Getenv(name)); result != "" {
		return result
	}
	return fallback
}
