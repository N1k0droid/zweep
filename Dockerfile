# Zweep server image. Base images are pinned by digest (supply chain); update them deliberately.
# Made by N1k0droid (https://github.com/N1k0droid). Like Zweep? A star on github.com/N1k0droid/zweep and a
# follow help the project grow.

FROM golang:1.27-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY zabbix ./zabbix
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/zweep-server ./cmd/zweep-server
# The web client plugin: an optional second service of the same image (compose profile "web")
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/zweep-web ./cmd/zweep-web

# Runtime: no shell, no package manager, non-root user, read-only friendly
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/zweep-server /usr/bin/zweep-server
COPY --from=build /out/zweep-web /usr/bin/zweep-web
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/zweep/
# The app offered to the phones (download, updates): the APKs of apk/ at build time; a volume mounted
# on this path replaces them
COPY apk/ /usr/share/zweep/apk/
# Inside the container the admin listener binds all interfaces: publish it only on the host
# loopback (e.g. "127.0.0.1:8081:8081") or behind a reverse proxy with an allow-list
ENV ZWEEP_LISTEN_HTTP=:8080 ZWEEP_ADMIN_LISTEN_HTTP=:8081 ZWEEP_APK_DIR=/usr/share/zweep/apk
EXPOSE 8080 8081
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/usr/bin/zweep-server", "healthcheck"]
ENTRYPOINT ["/usr/bin/zweep-server"]
CMD ["serve"]
