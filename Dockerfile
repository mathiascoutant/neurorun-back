ARG APP_VERSION=0.0.0

# Runtime Debian plutôt qu’Alpine : évite des échecs TLS (« remote error: tls: internal error »)
# vers MongoDB Atlas depuis certains hôtes Docker.
# Même famille de versions que premierdelan-back (Go 1.21 + driver 1.13) pour le même comportement TLS vers Atlas.
FROM golang:1.21-bookworm AS builder
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
ARG APP_VERSION
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${APP_VERSION}" -o /runapp .

FROM debian:bookworm-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
ARG APP_VERSION
LABEL org.opencontainers.image.version="${APP_VERSION}"
COPY --from=builder /runapp /usr/local/bin/runapp
EXPOSE 8080
ENV PORT=8080
ENTRYPOINT ["/usr/local/bin/runapp"]
