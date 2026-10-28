# Dockerfile
# Builds a lightweight, production-ready container image for the CacheCraft Go application.
#
# WHAT IS A MULTI-STAGE DOCKER BUILD?
# In Python or JavaScript, you typically copy source code and install node_modules
# or pip packages directly into the final container. The container ends up containing
# compilers, npm, pip, and dev dependencies, making it 500MB to 1GB in size.
#
# In Go, we use a "multi-stage build":
#   Stage 1 (builder): Uses the full Go compiler image (~300MB) to compile
#   our Go source code into a single, self-contained binary file.
#
#   Stage 2 (final): Uses a tiny Alpine Linux image (~7MB). We copy ONLY the
#   compiled binary from Stage 1 into Stage 2. The compiler and source files
#   are thrown away.
#
# Result: The final container is only about 15MB to 20MB, boots in 5 milliseconds,
# and contains zero unnecessary tools, reducing memory usage and security risk.

# ==============================================================================
# Stage 1: Build the Go binary
# ==============================================================================
FROM golang:alpine AS builder

# Set working directory inside the container
WORKDIR /build

# Copy dependency manifests first.
# Docker caches layers: if go.mod and go.sum have not changed,
# Docker reuses the cached downloaded dependencies layer.
COPY go.mod go.sum ./
ENV GOTOOLCHAIN=auto
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Compile the Go application.
# Explanation of flags:
#   CGO_ENABLED=0: Disables C bindings, producing a 100% statically linked binary
#                  that can run on any Linux kernel without needing glibc installed.
#   GOOS=linux:    Ensures the target operating system is Linux (even when building
#                  from Windows or macOS).
#   -ldflags="-w -s":
#       -w turns off DWARF debugging information.
#       -s turns off the symbol table.
#       Both flags strip unnecessary metadata to reduce the binary size by ~30%.
#   -o /app/cachecraft: The destination path for the compiled binary.
#   ./cmd/server:  The path to the main package entry point.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/cachecraft ./cmd/server

# ==============================================================================
# Stage 2: Final lightweight runtime container
# ==============================================================================
FROM alpine:3.20

# Install ca-certificates (for HTTPS/TLS if needed) and curl (for health checks).
RUN apk --no-cache add ca-certificates curl

WORKDIR /app

# Copy ONLY the compiled binary from the builder stage
COPY --from=builder /app/cachecraft /app/cachecraft

# Expose port 8080 (the port Chi HTTP server listens on inside the container)
EXPOSE 8080

# Execute the binary directly when the container launches
ENTRYPOINT ["/app/cachecraft"]
