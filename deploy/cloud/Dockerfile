# syntax=docker/dockerfile:1

FROM golang:1.25.7-bookworm AS build
WORKDIR /src/backend

COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY backend/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/ao-cloud ./cmd/ao-cloud && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/ao-cloud-migrate ./cmd/ao-cloud-migrate
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/ao ./cmd/ao

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=nonroot:nonroot /out/ao-cloud /ao-cloud
COPY --from=build --chown=nonroot:nonroot /out/ao-cloud-migrate /ao-cloud-migrate
COPY --from=build --chown=nonroot:nonroot /out/ao /ao
EXPOSE 8080
ENTRYPOINT ["/ao-cloud"]
