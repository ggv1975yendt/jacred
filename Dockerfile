FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Copy all source subdirectories
COPY cmd/       ./cmd/
COPY config/    ./config/
COPY cron/      ./cron/
COPY server/    ./server/
COPY tracker/   ./tracker/

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o jacred ./cmd

# ─── Runtime image ───────────────────────────────────────────
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -S jacred && adduser -S -G jacred jacred

WORKDIR /opt/jacred

COPY --from=builder /app/jacred ./jacred
COPY wwwroot/  ./wwwroot/
COPY init.yaml ./init.yaml

RUN chown -R jacred:jacred /opt/jacred

USER jacred

EXPOSE 9117

CMD ["./jacred"]