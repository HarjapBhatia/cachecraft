# Stage 1: Build the Go binary
FROM golang:alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
ENV GOTOOLCHAIN=auto
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/cachecraft ./cmd/server

# Stage 2: Minimal runtime container
FROM alpine:3.20

RUN apk --no-cache add ca-certificates curl

WORKDIR /app

COPY --from=builder /app/cachecraft /app/cachecraft

EXPOSE 8080

ENTRYPOINT ["/app/cachecraft"]
