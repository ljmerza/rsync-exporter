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
	portPtr = 9150
	rsyncFilePath = "/logs/rsync.log"
)

var (
	bytesSentCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "rsync_bytes_sent_total",
		Help: "Total bytes sent",
	})

	bytesReceivedCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "rsync_bytes_received_total",
		Help: "Total bytes received",
	})

	totalSizeGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_total_size",
		Help: "Total size synced",
	})

	lastRsyncExecutionTime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "rsync_last_sync",
		Help: "Last rsync sync time",
	})
)

func setupHTTPListener() error {
	fmt.Println("Starting HTTP listener for Prometheus metrics...")
	err := http.ListenAndServe(":"+strconv.Itoa(portPtr), promhttp.Handler())
	if err != nil {
			return fmt.Errorf("error starting HTTP server: %w", err)
	}
	return nil
}

func parseLogLine(logLine string) {
	parts := strings.Fields(logLine)

	if len(parts) < 2 {
			return
	}

	// Check if the line contains "sent" and "received" information
	if parts[3] == "sent" && parts[6] == "received" {
			sentBytes, err := strconv.ParseFloat(strings.ReplaceAll(parts[4], ",", ""), 64)
			if err != nil {
					fmt.Fprintf(os.Stderr, "error parsing sent bytes: %v\n", err)
					return
			}

			fmt.Printf("Sent bytes: %f\n", sentBytes)
			bytesSentCounter.Add(sentBytes)

			receivedBytes, err := strconv.ParseFloat(strings.ReplaceAll(parts[7], ",", ""), 64)
			if err != nil {
					fmt.Fprintf(os.Stderr, "error parsing received bytes: %v\n", err)
					return
			}

			fmt.Printf("Received bytes: %f\n", receivedBytes)
			bytesReceivedCounter.Add(receivedBytes)
	}

	// Check if the line contains "total size" information
	if parts[3] == "total" && parts[4] == "size" {
			totalSizeBytes, err := strconv.ParseFloat(strings.ReplaceAll(parts[6], ",", ""), 64)
			if err != nil {
					fmt.Fprintf(os.Stderr, "error parsing total size bytes: %v\n", err)
					return
			}

			fmt.Printf("Total size bytes: %f\n", totalSizeBytes)
			totalSizeGauge.Set(totalSizeBytes)

			fmt.Printf("Setting last sync time to %d\n", time.Now().UnixNano() / 1e6)
			lastRsyncExecutionTime.Set(float64(time.Now().UnixNano()) / 1e6)
	}
}


func tailLogFile(filePath string) error {
	fmt.Printf("Attempting to tail log file: %s\n", filePath)
	cmd := exec.Command("tail", "-f", "-n", "+1", filePath)

	cmdReader, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("error creating StdoutPipe for tail: %w", err)
	}

	scanner := bufio.NewScanner(cmdReader)
	go func() {
		i := 0
		for scanner.Scan() {
			line := scanner.Text()
			i++
			parseLogLine(line)
		}
	}()

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("error starting tail: %w", err)
	}

	go func() {
		err = cmd.Wait()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Tail command finished with error: %v\n", err)
		}
	}()

	fmt.Println("Successfully started tailing log file.")
	
	return nil
}

func main() {
	fmt.Println("Rsync Exporter starting...")

	for {
		err := tailLogFile(rsyncFilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v. Retrying in 10 seconds...\n", err)
			time.Sleep(10 * time.Second)
		} else {
			fmt.Println("Log file monitoring started successfully.")
			break
		}
	}

	go func() {
		err := setupHTTPListener()
		if err != nil {
				fmt.Fprintf(os.Stderr, "HTTP listener error: %v\n", err)
		}
	}()

	select {}

}
