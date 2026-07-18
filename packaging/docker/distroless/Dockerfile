FROM docker.io/library/golang:trixie AS build
ADD . /go/src/app
WORKDIR /go/src/app
RUN make miniflux
# Create and pre-set ownership of /var/lib/miniflux to nonroot (UID 65532).
# Works out-of-the-box with anonymous volumes; external volume mounts require
# the host directory to be owned by UID 65532 (e.g. `chown 65532 /host/path`).
RUN mkdir -p /var/lib/miniflux && chown 65532:65532 /var/lib/miniflux

FROM gcr.io/distroless/base-debian13:nonroot

LABEL org.opencontainers.image.title=Miniflux
LABEL org.opencontainers.image.description="Miniflux is a minimalist and opinionated feed reader"
LABEL org.opencontainers.image.vendor="Frédéric Guillot"
LABEL org.opencontainers.image.licenses=Apache-2.0
LABEL org.opencontainers.image.url=https://miniflux.app
LABEL org.opencontainers.image.source=https://github.com/miniflux/v2
LABEL org.opencontainers.image.documentation=https://miniflux.app/docs/

EXPOSE 8080
ENV LISTEN_ADDR=0.0.0.0:8080
ENV DATABASE_URL=/var/lib/miniflux/miniflux.db

COPY --from=build --chown=65532:65532 /var/lib/miniflux /var/lib/miniflux
COPY --from=build /go/src/app/miniflux /usr/bin/miniflux
VOLUME /var/lib/miniflux
CMD ["/usr/bin/miniflux"]
