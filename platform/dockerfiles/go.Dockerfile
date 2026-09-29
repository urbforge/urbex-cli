# Urbex runtime image for `runtime: go` - see
# docs/decisions/0018-image-build-komodo-gitea-registry.md in urbforge/urbex.
# Builds ./cmd/<service> if it exists (multi-service repos), else the
# module root. The binary must listen on $PORT.
FROM docker.io/library/golang:1.26 AS build
ARG SERVICE
WORKDIR /src
COPY . .
RUN if [ -n "$SERVICE" ] && [ -d "./cmd/$SERVICE" ]; then pkg="./cmd/$SERVICE"; else pkg="."; fi \
 && CGO_ENABLED=0 go build -o /out/app "$pkg"

FROM docker.io/library/alpine:3.22
RUN adduser -D -H app
COPY --from=build /out/app /usr/local/bin/app
USER app
ENTRYPOINT ["/usr/local/bin/app"]
