FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/controlnode ./cmd/controlnode
RUN CGO_ENABLED=0 go build -o /out/controlnode ./cmd/controlnode
FROM alpine:3.20
COPY --from=build /out/controlnode /usr/local/bin/controlnode
EXPOSE 8000
ENTRYPOINT ["controlnode"]
