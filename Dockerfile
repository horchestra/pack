# pack in an image: the runner of an operator's jobs (runtime docker, runtime k8s). git and openssh-client are for
# the checkouts of the releases it applies; the cache of the packs and the targets' programs is a volume (/cache),
# the state is the database's (state: db) — the container itself holds nothing between runs.
FROM golang:alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# the sdk module is replaced by ./sdk: go mod download reads its go.mod
COPY sdk/go.mod sdk/go.sum ./sdk/
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pack ./cmd/pack

FROM alpine:3
RUN apk add --no-cache ca-certificates git openssh-client \
    && adduser -D -h /home/pack pack \
    && mkdir -p /cache && chown pack:pack /cache
COPY --from=build /out/pack /usr/local/bin/pack
USER pack
ENV HOME=/home/pack \
    PACK_CACHE=/cache
VOLUME ["/cache"]
WORKDIR /home/pack
ENTRYPOINT ["pack"]
CMD ["--help"]
