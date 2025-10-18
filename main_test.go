package main

import (
	"math"
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
	bytesSentGauge.Set(0)
	bytesReceivedGauge.Set(0)

	line := "2023/12/22 01:18:25 [2224747] sent 39,889,034,403 bytes  received 5,146,208 bytes  70,546,738.48 bytes/sec"
	parseLogLine(line)

	sent := testutil.ToFloat64(bytesSentGauge)
	received := testutil.ToFloat64(bytesReceivedGauge)

	if math.Abs(sent-39889034403) > 1 {
		t.Fatalf("sent gauge = %f, want 39889034403", sent)
	}

	if math.Abs(received-5146208) > 1 {
		t.Fatalf("received gauge = %f, want 5146208", received)
	}
}

func TestParseLogLineTotalSize(t *testing.T) {
	totalSizeGauge.Set(0)
	lastRsyncExecutionTime.Set(0)
	lastRsyncExecutionTimeValid.Set(0)

	before := float64(time.Now().Unix())
	line := "2023/12/22 01:18:25 [2224747] total size is 199.5GiB  speedup is 4.99"
	parseLogLine(line)

	total := testutil.ToFloat64(totalSizeGauge)
	if math.Abs(total-(199.5*1024*1024*1024)) > 1024 {
		t.Fatalf("total size gauge = %f, want approx %f", total, 199.5*1024*1024*1024)
	}

	lastSync := testutil.ToFloat64(lastRsyncExecutionTime)
	valid := testutil.ToFloat64(lastRsyncExecutionTimeValid)

	after := float64(time.Now().Unix())
	if lastSync < before || lastSync > after+1 {
		t.Fatalf("last sync timestamp %f outside expected range [%f, %f]", lastSync, before, after+1)
	}

	if valid != 1 {
		t.Fatalf("lastRsyncExecutionTimeValid = %f, want 1", valid)
	}
}

func TestParseLogLineTotalSizeInvalid(t *testing.T) {
	lastRsyncExecutionTimeValid.Set(1)
	line := "2023/12/22 01:18:25 [2224747] total size is invalid_data  speedup is 4.99"
	parseLogLine(line)

	valid := testutil.ToFloat64(lastRsyncExecutionTimeValid)
	if valid != 0 {
		t.Fatalf("lastRsyncExecutionTimeValid = %f, want 0 after invalid parse", valid)
	}
}
