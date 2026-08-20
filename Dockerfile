FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
COPY config/example.yaml /app/config/example.yaml
COPY open-design /app/open-design
EXPOSE 8080 8081
ENTRYPOINT ["/app/gateway"]
CMD ["-config", "/app/config/example.yaml"]
