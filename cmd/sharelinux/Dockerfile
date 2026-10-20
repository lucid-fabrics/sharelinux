# Builds sharelinux on Debian 14 and runs it under Xvfb for headless checks.
#   docker build -f cmd/sharelinux/Dockerfile -t sharelinux .
#   docker run --rm -e OSS_HOST -e OSS_USER -e OSS_PASSWORD sharelinux
# go-sdl3 needs SDL 3.4 or newer, which Debian 14 (forky) and Ubuntu 26.04 ship.
FROM debian:forky AS build
COPY --from=golang:1.27 /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git gcc libsdl3-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./cmd/sharelinux && go build -o /out/sharelinux ./cmd/sharelinux && go build -o /out/mockrfb ./cmd/sharelinux/internal/mockrfb

FROM debian:forky
RUN apt-get update && apt-get install -y --no-install-recommends \
	libsdl3-0 xvfb xdotool imagemagick x11-utils xclip dbus-x11 ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sharelinux /out/mockrfb /usr/local/bin/
ENV DISPLAY=:99 SDL_VIDEODRIVER=x11
ENTRYPOINT ["sh", "-c", "Xvfb :99 -screen 0 1280x800x24 & sleep 1; exec \"$@\"", "--"]
CMD ["sharelinux", "-list"]
