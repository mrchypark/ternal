FROM golang:1.27.0-bookworm AS build

WORKDIR /src
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN ./frontend/build.sh
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w -buildid=' -o /out/ternal-api ./cmd/ternal-api

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --home-dir /data --create-home --shell /usr/sbin/nologin ternal \
    && install -d -o 65532 -g 65532 -m 0700 /data/ternal
COPY --from=build --chown=65532:65532 /out/ternal-api /usr/local/bin/ternal-api

ENV TERNAL_BIND=0.0.0.0:3000 \
    TERNAL_DATA_DIR=/data/ternal
EXPOSE 3000
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/ternal-api"]
