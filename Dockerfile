# syntax=docker/dockerfile:1

# Discern's image, in three stages:
#
#   build        compiles Discern. Go cross-compiles, so this stage runs on
#                the build machine's own CPU, even for an arm64 image.
#   makemkv-oss  compiles MakeMKV's open-source libraries (LGPL and GPL),
#                which makemkvcon loads.
#   final        Debian slim with only what runs: Discern, those libraries,
#                MKVToolNix and a headless Java.
#
# makemkvcon itself, MakeMKV's proprietary part, is not in the image.
# Discern downloads it at first start, once the user accepts MakeMKV's EULA
# (decisions D6 and D14).
#
# The base images are pinned by digest as well as tag, so a rebuild uses the
# same bytes until the pin is changed on purpose.

ARG GOLANG_IMAGE=golang:1.27.4-trixie@sha256:b54b2639de8c1bfe538ec5ffc45f29b1932235ac5f6d63c059853646677112d9
ARG DEBIAN_IMAGE=debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

FROM --platform=$BUILDPLATFORM ${GOLANG_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/discern ./cmd/discern


FROM ${DEBIAN_IMAGE} AS makemkv-oss
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential pkg-config ca-certificates curl \
      libssl-dev libexpat1-dev zlib1g-dev libavcodec-dev libavutil-dev \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
# pin.env holds the MakeMKV version and its tarballs' SHA-256, taken from
# MakeMKV's GPG-signed checksum file (see internal/makemkv/install).
COPY internal/makemkv/install/pin.env scripts/fetch-makemkv-oss.sh ./
RUN set -eu; . ./pin.env; \
    f=$(sh ./fetch-makemkv-oss.sh ./pin.env); \
    tar -xzf "$f"; \
    cd "makemkv-oss-${MAKEMKV_VERSION}"; \
    ./configure --prefix=/usr --disable-gui; \
    make -j"$(nproc)"; \
    make install DESTDIR=/out; \
    install -D -m 644 License.txt /out/usr/share/doc/makemkv-oss/License.txt; \
    printf 'Built from https://www.makemkv.com/download/%s\nSHA-256 %s\n' "$f" "$MAKEMKV_OSS_SHA256" \
      > /out/usr/share/doc/makemkv-oss/SOURCE


FROM ${DEBIAN_IMAGE}
# - default-jre-headless: MakeMKV runs a disc's Java menu (BD-J) with its
#   blues.jar to find the real playlist on some discs. Debian's slim images
#   leave out man pages, and Java's install fails without this folder.
# - mkvtoolnix: mkvmerge, for checking rips and the remux fallback.
# - The libraries are what MakeMKV's oss libraries link against; the ldd
#   check below fails the build if one is missing.
# - libcurl: makemkvcon loads it when it is there (unverified what for;
#   possibly its key and update downloads), so it is included rather than
#   finding out on a 4K disc.
RUN mkdir -p /usr/share/man/man1 \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      default-jre-headless \
      mkvtoolnix \
      libavcodec61 libavutil59 libexpat1 libssl3t64 zlib1g libstdc++6 libcurl4t64 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=makemkv-oss /out/ /
# Each file must exist. ldd's own exit status is ignored, because it fails on
# a static program ("not a dynamic executable"), which has nothing to miss.
RUN ldconfig \
 && for f in /usr/lib/libmakemkv.so.1 /usr/lib/libdriveio.so.0 /usr/bin/mmgplsrv /usr/bin/mmccextr; do \
      test -e "$f" || { echo "missing $f" >&2; exit 1; }; \
      ldd "$f" 2>&1 || true; \
    done > /tmp/ldd.txt \
 && ! grep 'not found' /tmp/ldd.txt \
 && rm /tmp/ldd.txt

# The user Discern runs as. Compose usually overrides it with `user:` to the
# owner of the media folders; any user works as long as it can write /config.
RUN useradd --system --uid 10001 --user-group --no-create-home --home-dir /config \
      --shell /usr/sbin/nologin discern \
 && install -d -o 10001 -g 10001 /config

COPY --from=build /out/discern /usr/local/bin/discern
COPY LICENSE /usr/share/doc/discern/LICENSE

# MakeMKV keeps its settings (and the key) in $HOME/.MakeMKV, so HOME points
# at the persistent volume.
ENV HOME=/config \
    DISCERN_DATA_DIR=/config
USER 10001:10001
EXPOSE 8790
# The first start downloads makemkvcon before the web server starts, so the
# health check allows two minutes for it.
HEALTHCHECK --interval=30s --timeout=5s --start-period=2m --retries=3 \
  CMD ["discern", "healthcheck"]
ENTRYPOINT ["discern"]
