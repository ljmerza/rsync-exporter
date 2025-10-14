package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	port          = 9150
	rsyncFilePath = "/logs/rsync.log"
)

func init() {
	if portEnv := os.Getenv("RSYNC_EXPORTER_PORT"); portEnv != "" {
		parsedPort, err := strconv.Atoi(portEnv)
		if err != nil || parsedPort <= 0 || parsedPort > 65535 {
			fmt.Fprintf(os.Stderr, "invalid RSYNC_EXPORTER_PORT value %q, using default %d\n", portEnv, port)
		} else {
			port = parsedPort
		}
	}

	if pathEnv := os.Getenv("RSYNC_LOG_PATH"); pathEnv != "" {
		rsyncFilePath = pathEnv
	}
}

var (
	bytesSentGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_last_sent_bytes",
		Help: "Bytes sent during the most recent rsync run",
	})

	bytesReceivedGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_last_received_bytes",
		Help: "Bytes received during the most recent rsync run",
	})

	totalSizeGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_last_total_size_bytes",
		Help: "Total size synced during the most recent rsync run",
	})

	lastRsyncExecutionTime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_last_sync",
		Help: "Last rsync sync time",
	})

	lastRsyncExecutionTimeValid = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_last_sync_valid",
		Help: "Indicates if the last rsync sync time is valid",
	})
)

func setupHTTPListener() error {
	fmt.Println("Starting HTTP listener for Prometheus metrics...")
	err := http.ListenAndServe(":"+strconv.Itoa(port), promhttp.Handler())
	if err != nil {
		return fmt.Errorf("error starting HTTP server: %w", err)
	}
	return nil
}

func parseLogLine(logLine string) {
	parts := strings.Fields(logLine)

	if len(parts) == 0 {
		return
	}

	// Check if the line contains "sent" and "received" information
	if len(parts) >= 8 && parts[3] == "sent" && parts[6] == "received" {
		sentBytes, err := strconv.ParseFloat(strings.ReplaceAll(parts[4], ",", ""), 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error parsing sent bytes: %v\n", err)
			return
		}

		fmt.Printf("Sent bytes: %f\n", sentBytes)
		bytesSentGauge.Set(sentBytes)

		receivedBytes, err := strconv.ParseFloat(strings.ReplaceAll(parts[7], ",", ""), 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error parsing received bytes: %v\n", err)
			return
		}

		fmt.Printf("Received bytes: %f\n", receivedBytes)
		bytesReceivedGauge.Set(receivedBytes)
	}

	// Check if the line contains "total size" information
	if len(parts) >= 7 && parts[3] == "total" && parts[4] == "size" {
		totalSizeBytes, err := strconv.ParseFloat(strings.ReplaceAll(parts[6], ",", ""), 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error parsing total size bytes: %v\n", err)
			lastRsyncExecutionTimeValid.Set(0)
			return
		}

		fmt.Printf("Total size bytes: %f\n", totalSizeBytes)
		totalSizeGauge.Set(totalSizeBytes)

		currentTimeMillis := float64(time.Now().UnixNano()) / 1e6
		fmt.Printf("Setting last sync time to %f\n", currentTimeMillis)
		lastRsyncExecutionTime.Set(currentTimeMillis)
		lastRsyncExecutionTimeValid.Set(1)
	}
}

func tailLogFile(filePath string) error {
	fmt.Printf("Attempting to tail log file: %s\n", filePath)
	cmd := exec.Command("tail", "-F", "-n", "0", filePath)

	cmdReader, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating StdoutPipe for tail: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("error starting tail: %w", err)
	}

	fmt.Println("Successfully started tailing log file.")

	scanner := bufio.NewScanner(cmdReader)
	for scanner.Scan() {
		parseLogLine(scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("tail scanner error: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("tail process exited: %w", err)
	}

	return fmt.Errorf("tail process exited without error")
}

func main() {
	fmt.Println("Rsync Exporter starting...")

	fmt.Printf("Watching rsync log at %s\n", rsyncFilePath)
	fmt.Printf("Serving metrics on port %d\n", port)

	go func() {
		if err := setupHTTPListener(); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP listener error: %v\n", err)
			os.Exit(1)
		}
	}()

	for {
		if err := tailLogFile(rsyncFilePath); err != nil {
			fmt.Fprintf(os.Stderr, "Error tailing log: %v. Retrying in 10 seconds...\n", err)
		}
		lastRsyncExecutionTimeValid.Set(0)
		time.Sleep(10 * time.Second)
	}
}
