FROM golang:1.26-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/m3652api ./cmd/m3652api

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/m3652api /app/m3652api
COPY version.txt /app/version.txt

EXPOSE 8217

CMD ["./m3652api", "serve", "-c", "/app/config.yaml"]
