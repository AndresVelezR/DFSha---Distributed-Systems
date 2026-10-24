FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controlnode ./cmd/controlnode
FROM alpine:3.23
RUN apk add --no-cache ca-certificates wget && mkdir -p /metadata
COPY --from=build /out/controlnode /usr/local/bin/controlnode
EXPOSE 8000 9000
ENTRYPOINT ["controlnode"]
