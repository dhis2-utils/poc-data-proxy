# The hub, the fake central and the calibration benchmarks in one image, for the
# performance test (docs/PERFORMANCE.md). Build from the repository root:
#   docker build -f perf/Dockerfile -t facility-hub-perf .
# On Apple Silicon the image is linux/arm64, the Raspberry Pi's architecture.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -o /out/hub ./cmd/hub \
 && go build -o /out/fakecentral ./cmd/fakecentral \
 && go test -c -o /out/store.bench ./internal/store

FROM alpine:3.22
RUN apk add --no-cache fio curl
COPY --from=build /out/ /usr/local/bin/
COPY perf/hub.yaml /etc/hub/hub.yaml
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["hub"]
CMD ["serve", "-config", "/etc/hub/hub.yaml"]
