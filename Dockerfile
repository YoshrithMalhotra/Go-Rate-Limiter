# Build stage
FROM golang:1.26-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /app

# Copy dependency definition files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Build a statically linked Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gobouncer ./cmd/api/main.go

# Production stage
FROM alpine:3.19

# Add non-root user for security
RUN adduser -D -u 10001 appuser

WORKDIR /app

# Copy the binary from the builder stage
COPY --from=builder /app/gobouncer .

# Copy default policy file to standard config location
COPY --from=builder /app/config/policies.example.json ./config/policies.example.json

# Use non-root user
USER appuser

# Expose port (default 8080)
EXPOSE 8080

# Run the app
ENTRYPOINT ["./gobouncer"]
