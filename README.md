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
    build:
      context: ./projects/rsync-prometheus-exporter
      dockerfile: Dockerfile
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
