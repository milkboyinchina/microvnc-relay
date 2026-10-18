# Development-only image for the Gate 0 data-plane daisy-chain harness.
# Builds with -tags dev so synthetic targets and fault endpoints exist.
# Production packaging stays in Phase 4.
FROM golang:1.26 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -tags dev -o /out/microvnc-relay ./cmd/microvnc-relay
RUN CGO_ENABLED=0 go build -tags dev -o /out/synthetic-vnc ./cmd/synthetic-vnc

FROM alpine:3.20

RUN apk add --no-cache ca-certificates iproute2
COPY --from=build /out/microvnc-relay /usr/local/bin/microvnc-relay
COPY --from=build /out/synthetic-vnc /usr/local/bin/synthetic-vnc

ENTRYPOINT ["/usr/local/bin/microvnc-relay"]
