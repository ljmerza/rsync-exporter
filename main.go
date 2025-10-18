package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

func parseBytesToken(token string) (float64, error) {
	cleaned := strings.TrimSpace(token)
	cleaned = strings.Trim(cleaned, ",")
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	if cleaned == "" {
		return 0, fmt.Errorf("empty token")
	}

	type unitDef struct {
		multiplier float64
		suffixes   []string
	}

	units := []unitDef{
		{multiplier: 1024, suffixes: []string{"KIB", "KI", "KB", "K"}},
		{multiplier: 1024 * 1024, suffixes: []string{"MIB", "MI", "MB", "M"}},
		{multiplier: 1024 * 1024 * 1024, suffixes: []string{"GIB", "GI", "GB", "G"}},
		{multiplier: 1024 * 1024 * 1024 * 1024, suffixes: []string{"TIB", "TI", "TB", "T"}},
	}

	multiplier := 1.0
	upper := strings.ToUpper(cleaned)

	for _, unit := range units {
		for _, suffix := range unit.suffixes {
			if strings.HasSuffix(upper, suffix) {
				cut := len(cleaned) - len(suffix)
				if cut < 0 {
					cut = 0
				}
				cleaned = cleaned[:cut]
				upper = upper[:cut]
				multiplier = unit.multiplier
				break
			}
		}
		if multiplier != 1.0 {
			break
		}
	}

	if strings.HasSuffix(upper, "B") {
		cut := len(cleaned) - 1
		if cut < 0 {
			cut = 0
		}
		cleaned = cleaned[:cut]
		upper = upper[:cut]
	}

	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return 0, fmt.Errorf("no numeric value in token")
	}

	value, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, err
	}

	return value * multiplier, nil
}

func setupHTTPListener() error {
	fmt.Println("Starting HTTP listener for Prometheus metrics...")
	err := http.ListenAndServe(":"+strconv.Itoa(port), promhttp.Handler())
	if err != nil {
		return fmt.Errorf("error starting HTTP server: %w", err)
	}
	return nil
}

func parseLogLine(logLine string) {
	tokens := strings.Fields(logLine)
	if len(tokens) == 0 {
		return
	}

	for idx := 0; idx < len(tokens); idx++ {
		switch tokens[idx] {
		case "sent":
			if idx+1 >= len(tokens) {
				continue
			}

			value, err := parseBytesToken(tokens[idx+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error parsing sent bytes: %v\n", err)
				continue
			}

			fmt.Printf("Sent bytes: %f\n", value)
			bytesSentGauge.Set(value)

		case "received":
			if idx+1 >= len(tokens) {
				continue
			}

			value, err := parseBytesToken(tokens[idx+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error parsing received bytes: %v\n", err)
				continue
			}

			fmt.Printf("Received bytes: %f\n", value)
			bytesReceivedGauge.Set(value)

		case "total":
			if idx+1 >= len(tokens) || tokens[idx+1] != "size" {
				continue
			}

			valueIdx := idx + 2
			if valueIdx < len(tokens) && tokens[valueIdx] == "is" {
				valueIdx++
			}

			if valueIdx >= len(tokens) {
				continue
			}

			value, err := parseBytesToken(tokens[valueIdx])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error parsing total size bytes: %v\n", err)
				lastRsyncExecutionTimeValid.Set(0)
				continue
			}

			fmt.Printf("Total size bytes: %f\n", value)
			totalSizeGauge.Set(value)

			currentTimeSeconds := float64(time.Now().Unix())
			fmt.Printf("Setting last sync time to %f\n", currentTimeSeconds)
			lastRsyncExecutionTime.Set(currentTimeSeconds)
			lastRsyncExecutionTimeValid.Set(1)
		}
	}

}

func tailLogFile(filePath string) error {
	fmt.Printf("Attempting to tail log file: %s\n", filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("error opening log file: %w", err)
	}
	defer func() {
		if file != nil {
			if err := file.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "error closing log file handle: %v\n", err)
			}
		}
	}()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("error stating log file: %w", err)
	}

	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("error seeking log file: %w", err)
	}

	fmt.Println("Successfully started tailing log file.")

	reader := bufio.NewReader(file)
	var pending string

	for {
		chunk, readErr := reader.ReadString('\n')
		if len(chunk) > 0 {
			pending += chunk

			for {
				newlineIdx := strings.IndexByte(pending, '\n')
				if newlineIdx == -1 {
					break
				}

				line := strings.TrimRight(pending[:newlineIdx], "\r")
				if line != "" {
					parseLogLine(line)
				}
				pending = pending[newlineIdx+1:]
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				time.Sleep(500 * time.Millisecond)

				newInfo, statErr := os.Stat(filePath)
				if statErr != nil {
					if os.IsNotExist(statErr) {
						continue
					}
					return fmt.Errorf("error stating log file: %w", statErr)
				}

				if !os.SameFile(fileInfo, newInfo) {
					if err := file.Close(); err != nil {
						fmt.Fprintf(os.Stderr, "error closing old log file handle: %v\n", err)
					}
					file = nil

					reopened, openErr := os.Open(filePath)
					if openErr != nil {
						return fmt.Errorf("error reopening rotated log file: %w", openErr)
					}

					file = reopened
					reader.Reset(file)
					fileInfo = newInfo
					pending = ""
				} else {
					currentOffset, seekErr := file.Seek(0, io.SeekCurrent)
					if seekErr != nil {
						return fmt.Errorf("error checking current offset: %w", seekErr)
					}

					if currentOffset > newInfo.Size() {
						if _, err := file.Seek(0, io.SeekStart); err != nil {
							return fmt.Errorf("error seeking after truncation: %w", err)
						}
						reader.Reset(file)
						pending = ""
					}
				}

				continue
			}

			return fmt.Errorf("log reader error: %w", readErr)
		}
	}
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
