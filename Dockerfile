# muster -- stdlib-only static binary on scratch.  The only network
# I/O is the in-cluster Kubernetes API (authenticated with the mounted
# ServiceAccount CA, so no system roots needed) and the plaintext
# metrics listener.
#
# CI (.github/workflows/ci.yml) builds and pushes this to
# ghcr.io/jeffbstewart/muster on push to main and on version tags.
# To build locally from the repo root:  docker build -t muster:dev .
#
# The builder is pinned by digest (golang:1.26.10): it controls the output
# binary, so pin it like a dependency.
FROM golang:1.26.11@sha256:6327cb290853a5d13091c436b88259b4f35ff70e49b1f8d9e5d1dd1757e9c6ba AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /muster ./cmd/muster

FROM scratch
COPY --from=build /muster /muster
USER 65534:65534
ENTRYPOINT ["/muster"]
