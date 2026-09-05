# muster -- stdlib-only static binary on scratch.  The only network
# I/O is the in-cluster Kubernetes API (authenticated with the mounted
# ServiceAccount CA, so no system roots needed) and the plaintext
# metrics listener.
#
# CI (.github/workflows/ci.yml) builds and pushes this to
# ghcr.io/jeffbstewart/muster on push to main and on version tags.
# To build locally from the repo root:  docker build -t muster:dev .
#
# The builder is pinned by digest (golang:1.26): it controls the output
# binary, so pin it like a dependency.
FROM golang:1.26@sha256:dc2521c2a906db43073b8b4d99f491b6341cf15610b6ebbab187c45153f9959e AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /muster ./cmd/muster

FROM scratch
COPY --from=build /muster /muster
USER 65534:65534
ENTRYPOINT ["/muster"]
