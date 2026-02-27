# Build stage
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o whats-bot .

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY --from=builder /app/whats-bot .

# Persistent volume for WhatsApp session (store.db)
VOLUME ["/app/data"]

EXPOSE 8080

ENTRYPOINT ["/app/whats-bot"]
CMD ["-port", "8080", "-store", "/app/data/store.db"]
