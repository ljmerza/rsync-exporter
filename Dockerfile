FROM golang:1.19.2-bullseye

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -o /exporter

CMD ["/exporter"]
