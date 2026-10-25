# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w -X brunoplay/internal/api.Version=${VERSION}" -o /out/brunoplay ./cmd/brunoplay

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
USER app
COPY --from=build /out/brunoplay /usr/local/bin/brunoplay
EXPOSE 8080
ENTRYPOINT ["brunoplay"]
CMD ["api"]
