# Overview

### Description
rsync metrics exporter for **Prometheus.io**

### Setup

You will need to run rsync with the `--stats` option since this exporter is parsing these two lines:

```bash
2023/12/22 01:18:25 [2224747] sent 39,889,034,403 bytes  received 5,146,208 bytes  70,546,738.48 bytes/sec
2023/12/22 01:18:25 [2224747] total size is 199,212,300,476  speedup is 4.99
```

You will also need to log to a file since the exporter is reading the file. So the command at a minimum would look like this:

```bash
rsync --stats /source /destination > /logs/rsync.log
```


```yaml
rsync_exporter:
    image: lmerza/rsync-exporter:latest
    container_name: rsync_exporter
    restart: unless-stopped
    ports:
      - 9150:9150
    volumes:
      - ./volumes/rsync_exporter/rsync.log:/logs/rsync.log:ro
```

Since you are logging to a file, you can use logrotate to manage the file size.

```bash
/logs/rsync.log {
    rotate 5
    daily
    compress
    missingok
    notifempty
    copytruncate
}
```


### Metrics

| Metric | Description |
| ------ | ----------- |
rsync_bytes_sent_total | Total bytes sent
rsync_bytes_received_total | Total bytes received
rsync_total_size | Total size of files to be transferred
rsync_last_sync | Last sync time
rsync_last_sync_valid | Is last sync time is valid


### Alerts

```yaml
groups:
  - name: Server Alerts
    rules:
      # Alert if rsync sync hasnt happened within 24 hours
      - alert: Server Rsync Stale
        expr: |
          (time() - (rsync_last_sync / 1000) > 86400) and (rsync_last_sync_valid == 1)
        labels:
          severity: critical
        annotations:
          summary: "Rsync sync is stale"
          description: "The last rsync sync is more than 24 hours old."
```