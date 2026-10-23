FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /simworkflow ./cmd/simworkflow

FROM alpine:3.23
RUN apk add --no-cache ca-certificates
COPY --from=build /simworkflow /usr/local/bin/simworkflow
ENTRYPOINT ["/usr/local/bin/simworkflow"]
