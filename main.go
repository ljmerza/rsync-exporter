package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	port          = 9150
	rsyncFilePath = "/logs/rsync.log"
	rsyncLogDir   = ""
	debug         = false
)

// unitDef defines a byte unit with its multiplier and recognized suffixes
type unitDef struct {
	multiplier float64
	suffixes   []string
}

// byteUnits defines recognized byte unit suffixes (package-level to avoid allocation per call)
var byteUnits = []unitDef{
	{multiplier: 1024, suffixes: []string{"KIB", "KI", "KB", "K"}},
	{multiplier: 1024 * 1024, suffixes: []string{"MIB", "MI", "MB", "M"}},
	{multiplier: 1024 * 1024 * 1024, suffixes: []string{"GIB", "GI", "GB", "G"}},
	{multiplier: 1024 * 1024 * 1024 * 1024, suffixes: []string{"TIB", "TI", "TB", "T"}},
}

func init() {
	if portEnv := os.Getenv("RSYNC_EXPORTER_PORT"); portEnv != "" {
		parsedPort, err := strconv.Atoi(portEnv)
		if err != nil || parsedPort <= 0 || parsedPort > 65535 {
			fmt.Fprintf(os.Stderr, "invalid RSYNC_EXPORTER_PORT value %q, using default %d\n", portEnv, port)
		} else {
			port = parsedPort
		}
	}

	if dirEnv := os.Getenv("RSYNC_LOG_DIR"); dirEnv != "" {
		rsyncLogDir = dirEnv
	} else if pathEnv := os.Getenv("RSYNC_LOG_PATH"); pathEnv != "" {
		rsyncFilePath = pathEnv
	}

	if debugEnv := os.Getenv("RSYNC_DEBUG"); debugEnv != "" {
		debugEnv = strings.ToLower(debugEnv)
		debug = debugEnv == "true" || debugEnv == "1"
	}
}

var (
	bytesSentGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rsync_last_sent_bytes",
		Help: "Bytes sent during the most recent rsync run",
	}, []string{"rsync_job"})

	bytesReceivedGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rsync_last_received_bytes",
		Help: "Bytes received during the most recent rsync run",
	}, []string{"rsync_job"})

	totalSizeGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rsync_last_total_size_bytes",
		Help: "Total size synced during the most recent rsync run",
	}, []string{"rsync_job"})

	lastRsyncExecutionTime = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rsync_last_sync",
		Help: "Time of the last rsync attempt (success or failure)",
	}, []string{"rsync_job"})

	lastRsyncExecutionTimeValid = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rsync_last_sync_valid",
		Help: "Indicates if the last rsync sync time is valid",
	}, []string{"rsync_job"})

	lastRsyncExitCode = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rsync_last_exit_code",
		Help: "Exit code of the most recent rsync run (0 or 24 = success)",
	}, []string{"rsync_job"})
)

// debugf prints a message only when debug mode is enabled
func debugf(format string, args ...interface{}) {
	if debug {
		fmt.Printf(format, args...)
	}
}

func parseBytesToken(token string) (float64, error) {
	cleaned := strings.TrimSpace(token)
	cleaned = strings.Trim(cleaned, ",")
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	if cleaned == "" {
		return 0, fmt.Errorf("empty token")
	}

	multiplier := 1.0
	upper := strings.ToUpper(cleaned)

	for _, unit := range byteUnits {
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

	result := value * multiplier
	if result < 0 {
		return 0, fmt.Errorf("negative byte value: %f", result)
	}

	return result, nil
}

func setupHTTPListener(ctx context.Context, errCh chan<- error) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	server := &http.Server{
		Addr:    ":" + strconv.Itoa(port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP server shutdown error: %v\n", err)
		}
	}()

	fmt.Println("Starting HTTP listener for Prometheus metrics...")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("error starting HTTP server: %w", err)
	}
}

func parseLogLine(logLine string, jobName string) {
	tokens := strings.Fields(logLine)
	if len(tokens) == 0 {
		return
	}

	// Exit-code sentinel written by rsync-job as the final log line:
	// "rsync-exit-code: N". This is the authoritative success/failure signal.
	// Being the last line, it overrides any transient valid=1 set by an earlier
	// "total size is 0" summary from a job that still failed.
	if tokens[0] == "rsync-exit-code:" {
		if len(tokens) < 2 {
			return
		}

		code, err := strconv.Atoi(tokens[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error parsing rsync exit code: %v\n", err)
			return
		}

		debugf("[%s] Exit code: %d\n", jobName, code)
		lastRsyncExitCode.WithLabelValues(jobName).Set(float64(code))

		// Stamp every completed run as an attempt so staleness reflects "last attempt".
		lastRsyncExecutionTime.WithLabelValues(jobName).Set(float64(time.Now().Unix()))

		// Exit code 24 = "vanished files" — normal for live Docker volumes.
		if code == 0 || code == 24 {
			lastRsyncExecutionTimeValid.WithLabelValues(jobName).Set(1)
		} else {
			lastRsyncExecutionTimeValid.WithLabelValues(jobName).Set(0)
		}
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

			debugf("[%s] Sent bytes: %f\n", jobName, value)
			bytesSentGauge.WithLabelValues(jobName).Set(value)

		case "received":
			if idx+1 >= len(tokens) {
				continue
			}

			value, err := parseBytesToken(tokens[idx+1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error parsing received bytes: %v\n", err)
				continue
			}

			debugf("[%s] Received bytes: %f\n", jobName, value)
			bytesReceivedGauge.WithLabelValues(jobName).Set(value)

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
				lastRsyncExecutionTimeValid.WithLabelValues(jobName).Set(0)
				continue
			}

			debugf("[%s] Total size bytes: %f\n", jobName, value)
			totalSizeGauge.WithLabelValues(jobName).Set(value)

			currentTimeSeconds := float64(time.Now().Unix())
			debugf("[%s] Setting last sync time to %f\n", jobName, currentTimeSeconds)
			lastRsyncExecutionTime.WithLabelValues(jobName).Set(currentTimeSeconds)
			lastRsyncExecutionTimeValid.WithLabelValues(jobName).Set(1)
		}
	}
}

func tailLogFile(ctx context.Context, filePath string, jobName string, fromStart bool) error {
	debugf("Attempting to tail log file: %s (job: %s)\n", filePath, jobName)

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

	if !fromStart {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return fmt.Errorf("error seeking log file: %w", err)
		}
	}

	fmt.Printf("Successfully started tailing log file: %s (job: %s)\n", filePath, jobName)

	reader := bufio.NewReader(file)
	var pending string

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

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
					parseLogLine(line, jobName)
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
						if file != nil {
							if err := file.Close(); err != nil {
								fmt.Fprintf(os.Stderr, "error closing deleted log file handle: %v\n", err)
							}
							file = nil
						}

						for {
							select {
							case <-ctx.Done():
								return ctx.Err()
							case <-time.After(1 * time.Second):
							}

							newInfo, statErr = os.Stat(filePath)
							if statErr == nil {
								break
							}
							if !os.IsNotExist(statErr) {
								return fmt.Errorf("error stating log file while waiting: %w", statErr)
							}
						}

						reopened, openErr := os.Open(filePath)
						if openErr != nil {
							return fmt.Errorf("error reopening log file after deletion: %w", openErr)
						}

						file = reopened
						fileInfo, err = file.Stat()
						if err != nil {
							return fmt.Errorf("error stating reopened log file: %w", err)
						}
						reader.Reset(file)
						pending = ""
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

// jobNameFromPath extracts the job name from a log file path by stripping the directory and .log extension.
func jobNameFromPath(filePath string) string {
	base := filepath.Base(filePath)
	return strings.TrimSuffix(base, ".log")
}

// scanLogDir returns all .log file paths in the given directory.
func scanLogDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("error reading log directory: %w", err)
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	return paths, nil
}

type jobWatcher struct {
	cancel context.CancelFunc
}

// watchDirectory scans for .log files and manages a tail goroutine per file.
// It rescans every 30 seconds to pick up new files or clean up removed ones.
func watchDirectory(ctx context.Context, dir string) {
	var mu sync.Mutex
	watchers := make(map[string]*jobWatcher) // keyed by file path

	startWatcher := func(filePath string) {
		jobName := jobNameFromPath(filePath)
		watchCtx, watchCancel := context.WithCancel(ctx)

		mu.Lock()
		watchers[filePath] = &jobWatcher{cancel: watchCancel}
		mu.Unlock()

		go func() {
			defer func() {
				mu.Lock()
				delete(watchers, filePath)
				mu.Unlock()
			}()

			for {
				select {
				case <-watchCtx.Done():
					return
				default:
				}

				if err := tailLogFile(watchCtx, filePath, jobName, true); err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					fmt.Fprintf(os.Stderr, "Error tailing %s: %v. Retrying in 10 seconds...\n", filePath, err)
				}
				lastRsyncExecutionTimeValid.WithLabelValues(jobName).Set(0)

				select {
				case <-watchCtx.Done():
					return
				case <-time.After(10 * time.Second):
				}
			}
		}()
	}

	// Initial scan
	paths, err := scanLogDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Initial directory scan failed: %v\n", err)
	} else {
		for _, p := range paths {
			startWatcher(p)
		}
	}

	if len(paths) == 0 {
		fmt.Printf("No .log files found in %s yet, will rescan...\n", dir)
	}

	// Periodic rescan
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, w := range watchers {
				w.cancel()
			}
			mu.Unlock()
			return
		case <-ticker.C:
			paths, err := scanLogDir(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Directory rescan failed: %v\n", err)
				continue
			}

			currentPaths := make(map[string]bool)
			for _, p := range paths {
				currentPaths[p] = true
			}

			mu.Lock()
			// Start watchers for new files
			for _, p := range paths {
				if _, exists := watchers[p]; !exists {
					fmt.Printf("Discovered new log file: %s\n", p)
					mu.Unlock()
					startWatcher(p)
					mu.Lock()
				}
			}

			// Clean up watchers for removed files
			for p, w := range watchers {
				if !currentPaths[p] {
					fmt.Printf("Log file removed: %s, stopping watcher\n", p)
					jobName := jobNameFromPath(p)
					w.cancel()
					bytesSentGauge.DeleteLabelValues(jobName)
					bytesReceivedGauge.DeleteLabelValues(jobName)
					totalSizeGauge.DeleteLabelValues(jobName)
					lastRsyncExecutionTime.DeleteLabelValues(jobName)
					lastRsyncExecutionTimeValid.DeleteLabelValues(jobName)
					lastRsyncExitCode.DeleteLabelValues(jobName)
				}
			}
			mu.Unlock()
		}
	}
}

// tailSingleFile runs a single-file tail loop with retry, used in legacy mode.
func tailSingleFile(ctx context.Context, filePath string, jobName string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := tailLogFile(ctx, filePath, jobName, false); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			fmt.Fprintf(os.Stderr, "Error tailing log: %v. Retrying in 10 seconds...\n", err)
		}
		lastRsyncExecutionTimeValid.WithLabelValues(jobName).Set(0)

		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func main() {
	fmt.Println("Rsync Exporter starting...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Channel for HTTP server errors
	httpErrCh := make(chan error, 1)

	go setupHTTPListener(ctx, httpErrCh)

	if rsyncLogDir != "" {
		fmt.Printf("Watching log directory: %s\n", rsyncLogDir)
		fmt.Printf("Serving metrics on port %d\n", port)
		go watchDirectory(ctx, rsyncLogDir)
	} else {
		jobName := jobNameFromPath(rsyncFilePath)
		fmt.Printf("Watching rsync log at %s (job: %s)\n", rsyncFilePath, jobName)
		fmt.Printf("Serving metrics on port %d\n", port)
		go tailSingleFile(ctx, rsyncFilePath, jobName)
	}

	// Wait for shutdown signal or HTTP error
	select {
	case sig := <-sigCh:
		fmt.Printf("Received signal %v, shutting down...\n", sig)
	case err := <-httpErrCh:
		fmt.Fprintf(os.Stderr, "HTTP listener error: %v\n", err)
	}

	cancel()
	time.Sleep(100 * time.Millisecond) // Allow goroutines to clean up
	fmt.Println("Shutdown complete.")
}
