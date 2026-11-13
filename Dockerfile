# Use the official Golang image as the base image
FROM golang:1.25-alpine AS builder

# Enable toolchain download if required by dependencies
ENV GOTOOLCHAIN=auto

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the application statically for Alpine
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# SECURITY: Use a pinned Alpine version (not :latest) for reproducible builds
FROM alpine:3.20

# Install CA certificates for outbound TLS (MongoDB Atlas, Firebase, GCS, Sentry) and timezone data
RUN apk add --no-cache ca-certificates tzdata

# SECURITY: Create and switch to a non-root user
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

# Copy the binary from the builder stage (secrets must be injected at runtime, never baked into the image)
COPY --from=builder /app/main .

# SECURITY: Set ownership to non-root user
RUN chown -R appuser:appgroup /app

USER appuser

# Default to production so containers fail closed (e.g. requiring GCS_BUCKET_NAME and
# MONGO_URI instead of falling back to ephemeral ./uploads/ or localhost). Override with
# `-e APP_ENV=development` for local container testing only.
ENV APP_ENV=production

# Expose the port the app runs on
EXPOSE 8080

# Command to run the application
CMD ["./main"]
