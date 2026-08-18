FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/teslamate-dawarich ./cmd/teslamate-dawarich

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app && \
    mkdir -p /data && chown app:app /data
COPY --from=build /out/teslamate-dawarich /usr/local/bin/teslamate-dawarich
USER app
VOLUME ["/data"]
ENTRYPOINT ["teslamate-dawarich"]
