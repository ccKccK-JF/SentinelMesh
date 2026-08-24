FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/control-plane /control-plane
EXPOSE 50051 8080
ENTRYPOINT ["/control-plane"]
