FROM golang:1.23 AS build-go
ENV CGO_ENABLED=0
ARG BUILD_VERSION

WORKDIR /app
RUN go env -w GOMODCACHE=/root/.cache/go-build

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build go mod download

COPY . /app
RUN --mount=type=cache,target=/root/.cache/go-build go build -o backstream

FROM gcr.io/distroless/base:nonroot
USER nonroot
COPY --from=build-go /app/backstream /backstream

WORKDIR /
ENV BACKSTREAM_ADDR="0.0.0.0:8000"
ENV BACKSTREAM_LOG_FORMAT="json"
EXPOSE 8000

ENTRYPOINT ["/backstream"]
