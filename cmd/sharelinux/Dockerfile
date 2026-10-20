# Builds sharelinux on Debian 14 and runs it under Xvfb for headless checks.
#   docker build -f cmd/sharelinux/Dockerfile -t sharelinux .
#   docker run --rm sharelinux                      # -list of saved Macs
#   docker run --rm --entrypoint sharelinux-check sharelinux   # window under Xvfb against the mock server
# GTK 4.14 and libadwaita 1.5 are the least the viewer uses (AdwDialog, AdwPreferencesDialog and the
# toggle group need 1.5, 1.5 and 1.7); Debian 14 and Ubuntu 26.04 ship newer ones.
FROM debian:forky AS build
COPY --from=golang:1.27 /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
RUN apt-get update && apt-get install -y --no-install-recommends \
	ca-certificates git gcc pkg-config libgtk-4-dev libadwaita-1-dev libx11-dev libxfixes-dev \
	&& rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./cmd/sharelinux && go test ./cmd/sharelinux \
	&& go build -o /out/sharelinux ./cmd/sharelinux \
	&& go build -o /out/mockrfb ./cmd/sharelinux/internal/mockrfb

FROM debian:forky
RUN apt-get update && apt-get install -y --no-install-recommends \
	libgtk-4-1 libadwaita-1-0 xvfb xdotool imagemagick x11-utils xclip dbus-x11 gnome-keyring \
	libgl1-mesa-dri adwaita-icon-theme fonts-dejavu-core ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sharelinux /out/mockrfb /usr/local/bin/
COPY cmd/sharelinux/docker-check.sh /usr/local/bin/sharelinux-check
# Xvfb has no GPU, so GSK draws with its software renderer.
ENV DISPLAY=:99 GDK_BACKEND=x11 GSK_RENDERER=cairo LIBGL_ALWAYS_SOFTWARE=1 NO_AT_BRIDGE=1
ENTRYPOINT ["sh", "-c", "Xvfb :99 -screen 0 1280x800x24 & sleep 1; exec dbus-run-session -- \"$@\"", "--"]
CMD ["sharelinux", "-list"]
