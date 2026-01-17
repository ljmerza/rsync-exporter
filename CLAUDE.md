# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test Commands

```bash
# Run tests
go test -v ./...

# Build locally
go build -o exporter

# Build Docker image
docker build -t rsync-exporter .

# Run the exporter (requires log file)
./exporter
```

## Environment Variables

- `RSYNC_EXPORTER_PORT` - HTTP port for metrics (default: 9150)
- `RSYNC_LOG_PATH` - Path to rsync log file (default: /logs/rsync.log)

## Architecture

Single-file Go application that:
1. Tails an rsync log file watching for stats output
2. Parses `sent X bytes received Y bytes` and `total size is Z` lines
3. Exposes parsed values as Prometheus gauges on `/metrics`

Key functions in `main.go`:
- `parseBytesToken()` - Converts byte strings with units (K/M/G/T, KB/MiB/etc) to float64
- `parseLogLine()` - Extracts sent/received/total metrics from log lines
- `tailLogFile()` - Handles log tailing with rotation/truncation detection

The exporter handles logrotate by detecting inode changes and file truncation.
