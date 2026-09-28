FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/datanode ./cmd/datanode
RUN CGO_ENABLED=0 go build -o /out/datanode ./cmd/datanode
FROM alpine:3.20
COPY --from=build /out/datanode /usr/local/bin/datanode
RUN mkdir -p /data
EXPOSE 8001
ENTRYPOINT ["datanode"]
