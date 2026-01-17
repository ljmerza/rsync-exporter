FROM golang:1.19.2-bullseye AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /exporter

FROM gcr.io/distroless/static-debian11
COPY --from=builder /exporter /exporter
ENTRYPOINT ["/exporter"]
