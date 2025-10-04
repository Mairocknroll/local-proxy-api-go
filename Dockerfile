# syntax=docker/dockerfile:1

########################
# 1) Builder
########################
FROM golang:1.25-alpine AS builder
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata

# cache modules
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# copy source
COPY . .

# build only cmd/server
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/app ./cmd/server

########################
# 2) Runtime
########################
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget
ENV TZ=Asia/Bangkok PORT=8000 GIN_MODE=release
COPY --from=builder /bin/app /app
EXPOSE 8000
ENTRYPOINT ["/app"]
