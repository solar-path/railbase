syntax=docker/dockerfile:1.7

# ---- build stage ----
FROM golang:1.26-alpine AS build

WORKDIR /src

# Module cache layer
COPY go.mod go.sum* ./
RUN go mod download

# Source
COPY . .

ARG COMMIT=dev
ARG TAG=v0.0.0-dev
ARG DATE=unknown
ARG BUILD_TAGS=""

ENV CGO_ENABLED=0
RUN go build \
        -tags "${BUILD_TAGS}" \
        -trimpath \
        -ldflags="-s -w \
            -X github.com/railbase/railbase/internal/buildinfo.Commit=${COMMIT} \
            -X github.com/railbase/railbase/internal/buildinfo.Tag=${TAG} \
            -X github.com/railbase/railbase/internal/buildinfo.Date=${DATE}" \
        -o /out/railbase ./cmd/railbase

# ---- runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S railbase && adduser -S -G railbase railbase

COPY --from=build /out/railbase /usr/local/bin/railbase

USER railbase
WORKDIR /var/lib/railbase

EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/railbase"]
CMD ["serve"]
