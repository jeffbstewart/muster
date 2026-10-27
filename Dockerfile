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
FROM golang:1.26.10@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /muster ./cmd/muster

FROM scratch
COPY --from=build /muster /muster
USER 65534:65534
ENTRYPOINT ["/muster"]
