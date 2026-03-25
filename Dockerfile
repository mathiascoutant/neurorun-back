# syntax=docker/dockerfile:1

ARG APP_VERSION=0.0.0

FROM golang:1.21-alpine AS builder
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG APP_VERSION
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${APP_VERSION}" -o /runapp .

FROM alpine:3.19
RUN apk --no-cache add ca-certificates
ARG APP_VERSION
LABEL org.opencontainers.image.version="${APP_VERSION}"
COPY --from=builder /runapp /usr/local/bin/runapp
EXPOSE 8080
ENV PORT=8080
ENTRYPOINT ["/usr/local/bin/runapp"]
