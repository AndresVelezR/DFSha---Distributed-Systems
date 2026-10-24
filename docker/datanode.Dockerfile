FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/datanode ./cmd/datanode
FROM alpine:3.23
RUN apk add --no-cache ca-certificates wget && mkdir -p /data
COPY --from=build /out/datanode /usr/local/bin/datanode
EXPOSE 8001
ENTRYPOINT ["datanode"]
