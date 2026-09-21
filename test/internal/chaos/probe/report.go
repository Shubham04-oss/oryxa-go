package probe

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// WriteFindings prints findings, worst first, with a way to reproduce each.
func WriteFindings(out io.Writer, findings []*Finding, verbose bool) {
	if len(findings) == 0 {
		fmt.Fprintln(out, "  no findings")
		return
	}
	counts := map[string]int{}
	for _, finding := range findings {
		counts[finding.Severity]++
	}
	fmt.Fprintf(out, "  %d distinct findings: %d critical, %d high, %d medium, %d low, %d info\n\n",
		len(findings), counts[Critical], counts[High], counts[Medium], counts[Low], counts[Info])
	for index, finding := range findings {
		fmt.Fprintf(out, "  %2d. [%s] %s  %s  (×%d)\n", index+1, strings.ToUpper(finding.Severity), finding.Kind, finding.Route, finding.Count)
		fmt.Fprintf(out, "      %s", finding.Detail)
		if finding.Status != 0 {
			fmt.Fprintf(out, "  status=%d", finding.Status)
		}
		fmt.Fprintln(out)
		if len(finding.Examples) > 0 {
			fmt.Fprintf(out, "      e.g. %s\n", strings.Join(finding.Examples, ", "))
		}
		if verbose {
			if finding.Snippet != "" {
				fmt.Fprintf(out, "      response: %s\n", finding.Snippet)
			}
			if finding.RequestID != "" {
				fmt.Fprintf(out, "      request:  X-Request-Id %s\n", finding.RequestID)
			}
			if finding.Curl != "" {
				fmt.Fprintf(out, "      repro:    %s\n", clip(finding.Curl, 700))
			}
		}
	}
}

// WriteMatrix prints a table.
func WriteMatrix(out io.Writer, header []string, rows [][]string) {
	writer := tabwriter.NewWriter(out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "  "+strings.Join(header, "\t"))
	for _, row := range rows {
		fmt.Fprintln(writer, "  "+strings.Join(row, "\t"))
	}
	_ = writer.Flush()
}

// WriteLogDigest prints what the server said about itself.
func WriteLogDigest(out io.Writer, digest LogDigest, limit int) {
	fmt.Fprintf(out, "  %d log lines: %d errors, %d warnings, %d panics\n", digest.Lines, digest.Errors, digest.Warnings, digest.Panics)
	for _, text := range digest.PanicTxt {
		fmt.Fprintf(out, "  PANIC %s\n", text)
	}
	shown := 0
	for _, entry := range digest.Entries {
		if shown == limit {
			fmt.Fprintf(out, "  … and %d more distinct entries\n", len(digest.Entries)-limit)
			break
		}
		shown++
		fmt.Fprintf(out, "  ×%-5d %-5s %-10s %s\n", entry.Count, entry.Level, entry.Component, clip(entry.Message, 90))
		if entry.Error != "" {
			fmt.Fprintf(out, "         error: %s\n", clip(entry.Error, 220))
		}
		if entry.Path != "" {
			fmt.Fprintf(out, "         path:  %s\n", clip(entry.Path, 120))
		}
	}
}

// WriteAccess prints the server's own per-route view, busiest first.
func WriteAccess(out io.Writer, digest LogDigest, limit int) {
	if len(digest.Access) == 0 {
		return
	}
	fmt.Fprintf(out, "  %-6s %-8s %-9s %-9s %s\n", "reqs", "mean ms", "max ms", "MB out", "route  (statuses)")
	for index, stats := range digest.Access {
		if index == limit {
			fmt.Fprintf(out, "  ... and %d more routes\n", len(digest.Access)-limit)
			break
		}
		codes := make([]int, 0, len(stats.Statuses))
		for code := range stats.Statuses {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		parts := make([]string, 0, len(codes))
		for _, code := range codes {
			parts = append(parts, fmt.Sprintf("%d:%d", code, stats.Statuses[code]))
		}
		fmt.Fprintf(out, "  %-6d %-8.1f %-9.0f %-9.2f %s  (%s)\n", stats.Requests, stats.TotalMS/float64(stats.Requests), stats.MaxMS,
			float64(stats.Bytes)/(1<<20), clip(strings.TrimPrefix(stats.Route, "/v1/organizations/{organizationID}"), 60), strings.Join(parts, " "))
	}
	if len(digest.Slowest) > 0 {
		fmt.Fprintln(out, "  slowest requests the server logged:")
		for _, line := range digest.Slowest {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
}

// WriteStatusSpread prints how many answers of each status the client got.
func WriteStatusSpread(out io.Writer, stats Stats) {
	codes := make([]int, 0, len(stats.ByStatus))
	for code := range stats.ByStatus {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		label := fmt.Sprint(code)
		if code == 0 {
			label = "no-response"
		}
		parts = append(parts, fmt.Sprintf("%s×%d", label, stats.ByStatus[code]))
	}
	fmt.Fprintf(out, "  %d requests: %s\n", stats.Total, strings.Join(parts, "  "))
	fmt.Fprintf(out, "  latency: p50 %s  p95 %s  p99 %s  max %s\n", stats.P50, stats.P95, stats.P99, stats.Max)
}
