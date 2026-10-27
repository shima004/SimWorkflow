FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /simworkflow ./cmd/simworkflow

FROM alpine:3.23
LABEL io.simworkflow.config-bundle="v1" \
      org.opencontainers.image.source="https://github.com/shima004/SimWorkflow" \
      org.opencontainers.image.title="SimWorkflow worker" \
      org.opencontainers.image.description="RoboCup Rescue Simulation experiment runner and CLI"
RUN apk add --no-cache ca-certificates
COPY --from=build /simworkflow /usr/local/bin/simworkflow
ENTRYPOINT ["/usr/local/bin/simworkflow"]
