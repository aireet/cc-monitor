FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /claude-exporter .

FROM alpine:3.21
COPY --from=builder /claude-exporter /claude-exporter
EXPOSE 9101
CMD ["/claude-exporter"]
