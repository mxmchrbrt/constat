# constat shells out to restic (and, for verify_with targets, to a container
# runtime) rather than linking against a library — that decision is in
# CLAUDE.md and it means this image is only useful if restic is actually on
# PATH inside it. A constat image without restic would build fine and fail
# every single target at runtime, which is a worse failure than a slightly
# heavier image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/constat ./cmd/constat

FROM alpine:3.20
RUN apk add --no-cache restic ca-certificates
COPY --from=build /out/constat /usr/local/bin/constat

# No verify_with target works from inside this container unless the host's
# container runtime is reachable from it too (e.g. the docker socket
# bind-mounted in) — documented in README.md rather than solved here, since
# docker-in-docker is exactly the kind of complexity v0 chose to avoid.

RUN adduser -D -H constat
USER constat

ENTRYPOINT ["/usr/local/bin/constat"]
