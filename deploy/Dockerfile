FROM node:24-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN mkdir -p /src/internal/webui/dist && npm run build

FROM golang:1.26-alpine AS go-builder
WORKDIR /src
ARG VERSION=0.1.0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-builder /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tidegate ./cmd/tidegate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
  && addgroup -g 65532 -S tidegate \
  && adduser -u 65532 -S -G tidegate -h /data tidegate \
  && mkdir -p /data \
  && chown -R 65532:65532 /data
COPY --from=go-builder /out/tidegate /usr/local/bin/tidegate
USER 65532:65532
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080 7443
ENV TIDEGATE_DATA=/data \
    TIDEGATE_LISTEN=0.0.0.0:8080 \
    TIDEGATE_CLUSTER_LISTEN=0.0.0.0:7443 \
    TIDEGATE_NO_OPEN=true
ENTRYPOINT ["/usr/local/bin/tidegate"]
CMD ["serve", "--tls-self-signed"]
