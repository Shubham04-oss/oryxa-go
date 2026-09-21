package rig

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultPostgresContainer is the Postgres container of the repository's
// docker-compose.yml.
const DefaultPostgresContainer = "oryxa-go-postgres-1"

// Headroom reports the free and total bytes of the disk Postgres keeps its
// data on, which is the Docker VM's, not the Mac's. Checking the Mac's disk
// says nothing: Colima's VM has its own, much smaller one, and a run that
// fills it takes down every container that shares it, the development
// database included.
func Headroom(ctx context.Context, container string) (free, total int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", container, "df", "-B1", "/var/lib/postgresql/data").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("read the disk headroom through %s: %w", container, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("unreadable df output: %q", out)
	}
	total, _ = strconv.ParseInt(fields[1], 10, 64)
	free, _ = strconv.ParseInt(fields[3], 10, 64)
	return free, total, nil
}

// GiB renders bytes for a message.
func GiB(bytes int64) string { return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30)) }
