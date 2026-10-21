# Builds sharelinux on Debian 14 and runs it under Xvfb for headless checks.
#   docker build -f cmd/sharelinux/Dockerfile -t sharelinux .
#   docker run --rm sharelinux                      # -list of saved Macs
#   docker run --rm sharelinux sharelinux-check     # the window under Xvfb against the mock servers, tiles and video
# The viewer needs GTK 4.14 and libadwaita 1.7 (the mode switch is an AdwToggleGroup); Debian 14 and
# Ubuntu 26.04 ship newer ones.
FROM debian:forky AS build
COPY --from=golang:1.27 /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
RUN apt-get update && apt-get install -y --no-install-recommends \
	ca-certificates git gcc pkg-config libgtk-4-dev libadwaita-1-dev libgirepository1.0-dev libx11-dev libxfixes-dev libxi-dev libwayland-dev \
	libavcodec-dev libswscale-dev libva-dev libepoxy-dev \
	&& rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./cmd/sharelinux/... && go test ./cmd/sharelinux/... \
	&& go build -o /out/sharelinux ./cmd/sharelinux \
	&& go build -o /out/mockrfb ./cmd/sharelinux/internal/mockrfb \
	&& go build -o /out/mockapple ./cmd/sharelinux/internal/mockapple

FROM debian:forky
RUN apt-get update && apt-get install -y --no-install-recommends \
	libgtk-4-1 libadwaita-1-0 libavcodec63 libswscale10 libva2 libva-drm2 xvfb xdotool imagemagick x11-utils xclip dbus-x11 gnome-keyring \
	libgl1-mesa-dri adwaita-icon-theme fonts-dejavu-core ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sharelinux /out/mockrfb /out/mockapple /usr/local/bin/
COPY cmd/sharelinux/docker-check.sh /usr/local/bin/sharelinux-check
# Xvfb has no GPU, so GSK draws with its software renderer.
ENV DISPLAY=:99 GDK_BACKEND=x11 GSK_RENDERER=cairo LIBGL_ALWAYS_SOFTWARE=1 NO_AT_BRIDGE=1
ENTRYPOINT ["sh", "-c", "Xvfb :99 -screen 0 1280x800x24 & sleep 1; exec dbus-run-session -- \"$@\"", "--"]
CMD ["sharelinux", "-list"]
