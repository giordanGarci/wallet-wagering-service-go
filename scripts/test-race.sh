#!/bin/sh
# Runs `go test -race` inside golang:<go.mod version>, for hosts without a C
# toolchain (the race detector needs CGO). Arguments go to `go test`.
#
#   scripts/test-race.sh                          # unit tests, ./...
#   scripts/test-race.sh -tags=integration ./...  # needs the compose infra up
#
# With an integration/e2e tag the container joins the compose network
# (COMPOSE_NETWORK, default wallet_default) and the harness targets the
# services by name.
set -eu

root=$(cd "$(dirname "$0")/.." && { pwd -W 2>/dev/null || pwd; })
image="golang:$(awk '/^go /{print $2}' "$root/go.mod")"
[ $# -eq 0 ] && set -- ./...

export MSYS_NO_PATHCONV=1
case " $* " in
  *integration*|*e2e*)
    exec docker run --rm \
      --network "${COMPOSE_NETWORK:-wallet_default}" \
      -e "TEST_DATABASE_URL=postgres://wallet:wallet@postgres:5432/wallet?sslmode=disable" \
      -e "TEST_AWS_ENDPOINT_URL=http://ministack:4566" \
      -v "$root:/src" -w /src \
      -v wallet-gomodcache:/go/pkg/mod -v wallet-gobuildcache:/root/.cache/go-build \
      "$image" go test -race "$@"
    ;;
  *)
    exec docker run --rm \
      -v "$root:/src" -w /src \
      -v wallet-gomodcache:/go/pkg/mod -v wallet-gobuildcache:/root/.cache/go-build \
      "$image" go test -race "$@"
    ;;
esac
