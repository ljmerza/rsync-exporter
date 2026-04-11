package main

import (
	"context"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestParseBytesToken(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{"plain integer", "39,889,034,403", 39889034403},
		{"kilobytes", "5.5K", 5.5 * 1024},
		{"mebibytes", "1.25MiB", 1.25 * 1024 * 1024},
		{"gigabytes", "2.5G", 2.5 * 1024 * 1024 * 1024},
		{"bytes suffix", "100B", 100},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBytesToken(tc.input)
			if err != nil {
				t.Fatalf("parseBytesToken(%q) unexpected error: %v", tc.input, err)
			}

			if diff := math.Abs(got - tc.expected); diff > 1e-3 {
				t.Fatalf("parseBytesToken(%q) diff %f, want %f, got %f", tc.input, diff, tc.expected, got)
			}
		})
	}
}

func TestParseLogLineSentReceived(t *testing.T) {
	bytesSentGauge.WithLabelValues("test_job").Set(0)
	bytesReceivedGauge.WithLabelValues("test_job").Set(0)

	line := "2023/12/22 01:18:25 [2224747] sent 39,889,034,403 bytes  received 5,146,208 bytes  70,546,738.48 bytes/sec"
	parseLogLine(line, "test_job")

	sent := testutil.ToFloat64(bytesSentGauge.WithLabelValues("test_job"))
	received := testutil.ToFloat64(bytesReceivedGauge.WithLabelValues("test_job"))

	if math.Abs(sent-39889034403) > 1 {
		t.Fatalf("sent gauge = %f, want 39889034403", sent)
	}

	if math.Abs(received-5146208) > 1 {
		t.Fatalf("received gauge = %f, want 5146208", received)
	}
}

func TestParseLogLineTotalSize(t *testing.T) {
	totalSizeGauge.WithLabelValues("test_job").Set(0)
	lastRsyncExecutionTime.WithLabelValues("test_job").Set(0)
	lastRsyncExecutionTimeValid.WithLabelValues("test_job").Set(0)

	before := float64(time.Now().Unix())
	line := "2023/12/22 01:18:25 [2224747] total size is 199.5GiB  speedup is 4.99"
	parseLogLine(line, "test_job")

	total := testutil.ToFloat64(totalSizeGauge.WithLabelValues("test_job"))
	if math.Abs(total-(199.5*1024*1024*1024)) > 1024 {
		t.Fatalf("total size gauge = %f, want approx %f", total, 199.5*1024*1024*1024)
	}

	lastSync := testutil.ToFloat64(lastRsyncExecutionTime.WithLabelValues("test_job"))
	valid := testutil.ToFloat64(lastRsyncExecutionTimeValid.WithLabelValues("test_job"))

	after := float64(time.Now().Unix())
	if lastSync < before || lastSync > after+1 {
		t.Fatalf("last sync timestamp %f outside expected range [%f, %f]", lastSync, before, after+1)
	}

	if valid != 1 {
		t.Fatalf("lastRsyncExecutionTimeValid = %f, want 1", valid)
	}
}

func TestParseLogLineTotalSizeInvalid(t *testing.T) {
	lastRsyncExecutionTimeValid.WithLabelValues("test_job").Set(1)
	line := "2023/12/22 01:18:25 [2224747] total size is invalid_data  speedup is 4.99"
	parseLogLine(line, "test_job")

	valid := testutil.ToFloat64(lastRsyncExecutionTimeValid.WithLabelValues("test_job"))
	if valid != 0 {
		t.Fatalf("lastRsyncExecutionTimeValid = %f, want 0 after invalid parse", valid)
	}
}

func TestParseBytesTokenNegative(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"negative number", "-100"},
		{"negative with suffix", "-5K"},
		{"negative decimal", "-1.5M"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseBytesToken(tc.input)
			if err == nil {
				t.Fatalf("parseBytesToken(%q) expected error for negative value, got nil", tc.input)
			}
		})
	}
}

func TestMultipleJobsIndependent(t *testing.T) {
	// Parse stats for job "alpha"
	bytesSentGauge.WithLabelValues("alpha").Set(0)
	bytesSentGauge.WithLabelValues("beta").Set(0)

	parseLogLine("sent 1,000 bytes  received 500 bytes  1,500.00 bytes/sec", "alpha")
	parseLogLine("sent 9,999 bytes  received 8,888 bytes  18,887.00 bytes/sec", "beta")

	sentAlpha := testutil.ToFloat64(bytesSentGauge.WithLabelValues("alpha"))
	sentBeta := testutil.ToFloat64(bytesSentGauge.WithLabelValues("beta"))

	if math.Abs(sentAlpha-1000) > 1 {
		t.Fatalf("alpha sent = %f, want 1000", sentAlpha)
	}
	if math.Abs(sentBeta-9999) > 1 {
		t.Fatalf("beta sent = %f, want 9999", sentBeta)
	}

	// Verify updating one job doesn't affect the other
	parseLogLine("sent 2,000 bytes  received 100 bytes  2,100.00 bytes/sec", "alpha")
	sentAlpha = testutil.ToFloat64(bytesSentGauge.WithLabelValues("alpha"))
	sentBeta = testutil.ToFloat64(bytesSentGauge.WithLabelValues("beta"))

	if math.Abs(sentAlpha-2000) > 1 {
		t.Fatalf("alpha sent after update = %f, want 2000", sentAlpha)
	}
	if math.Abs(sentBeta-9999) > 1 {
		t.Fatalf("beta sent should be unchanged = %f, want 9999", sentBeta)
	}
}

func TestJobNameFromPath(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/logs/gym_to_z2.log", "gym_to_z2"},
		{"/logs/rsync.log", "rsync"},
		{"crawlspace_to_z2.log", "crawlspace_to_z2"},
		{"/some/deep/path/docker_to_shed.log", "docker_to_shed"},
		{"noext", "noext"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := jobNameFromPath(tc.path)
			if got != tc.expected {
				t.Fatalf("jobNameFromPath(%q) = %q, want %q", tc.path, got, tc.expected)
			}
		})
	}
}

func TestScanLogDir(t *testing.T) {
	dir := t.TempDir()

	// Create some .log files and a non-log file
	for _, name := range []string{"gym.log", "shed.log", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a subdirectory named something.log to ensure it's skipped
	if err := os.Mkdir(filepath.Join(dir, "subdir.log"), 0755); err != nil {
		t.Fatal(err)
	}

	paths, err := scanLogDir(dir)
	if err != nil {
		t.Fatalf("scanLogDir error: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("expected 2 log files, got %d: %v", len(paths), paths)
	}

	names := make(map[string]bool)
	for _, p := range paths {
		names[filepath.Base(p)] = true
	}

	if !names["gym.log"] || !names["shed.log"] {
		t.Fatalf("expected gym.log and shed.log, got %v", names)
	}
}

func TestScanLogDirEmpty(t *testing.T) {
	dir := t.TempDir()

	paths, err := scanLogDir(dir)
	if err != nil {
		t.Fatalf("scanLogDir error: %v", err)
	}

	if len(paths) != 0 {
		t.Fatalf("expected 0 log files, got %d", len(paths))
	}
}

func TestHealthEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	// Use a different port to avoid conflicts
	originalPort := port
	port = 19150
	defer func() { port = originalPort }()

	go setupHTTPListener(ctx, errCh)

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://localhost:19150/health")
	if err != nil {
		t.Fatalf("failed to GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health returned status %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestReadyEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)

	originalPort := port
	port = 19151
	defer func() { port = originalPort }()

	go setupHTTPListener(ctx, errCh)

	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://localhost:19151/ready")
	if err != nil {
		t.Fatalf("failed to GET /ready: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/ready returned status %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func BenchmarkParseBytesToken(b *testing.B) {
	inputs := []string{"39,889,034,403", "5.5K", "1.25MiB", "2.5G", "100B"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, input := range inputs {
			parseBytesToken(input)
		}
	}
}
