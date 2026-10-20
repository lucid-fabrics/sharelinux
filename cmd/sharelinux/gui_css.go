//go:build linux

package main

// stylesheet holds the few rules libadwaita has no class for: the coloured icon
// tiles, the status dot, the stats card, the copy card and the shake of a
// wrong password. Everything else is stock Adwaita.
const stylesheet = `
.oss-tile {
	min-width: 40px;
	min-height: 40px;
	border-radius: 10px;
	color: white;
}
.oss-tile.large {
	min-width: 64px;
	min-height: 64px;
	border-radius: 16px;
}
.oss-tile.tile-blue { background: #1c71d8; }
.oss-tile.tile-purple { background: #613583; }
.oss-tile.tile-green { background: #26a269; }
.oss-tile.tile-orange { background: #c64600; }
.oss-tile.tile-teal { background: #1a8a8f; }
.oss-tile.tile-pink { background: #c0307a; }
.oss-tile.tile-slate { background: #4a4a4a; }

.oss-dot {
	min-width: 8px;
	min-height: 8px;
	border-radius: 9999px;
	background: alpha(currentColor, 0.35);
}
.oss-dot.online { background: @success_color; }
.oss-status.online { color: @success_color; }

.oss-stats {
	background: rgba(20, 20, 20, 0.82);
	color: white;
	border-radius: 12px;
	padding: 12px 14px;
	box-shadow: 0 4px 16px rgba(0, 0, 0, 0.35);
}
.oss-stats label.value {
	font-family: monospace;
	font-size: 12px;
}
.oss-stats label.name {
	color: rgba(255, 255, 255, 0.72);
	font-size: 12px;
}

.oss-copy-card {
	background: @popover_bg_color;
	color: @popover_fg_color;
	border-radius: 12px;
	padding: 10px 12px 12px 16px;
	box-shadow: 0 6px 20px rgba(0, 0, 0, 0.4);
}

.oss-connected { color: @success_color; font-size: 12px; }
.oss-title-line { font-weight: bold; font-size: 14px; }

.oss-keycap {
	padding: 2px 8px;
	border-radius: 6px;
	border: 1px solid alpha(currentColor, 0.25);
	background: alpha(currentColor, 0.08);
	font-size: 13px;
}

@keyframes oss-shake {
	0% { transform: translateX(0); }
	15% { transform: translateX(-8px); }
	30% { transform: translateX(7px); }
	45% { transform: translateX(-6px); }
	60% { transform: translateX(5px); }
	75% { transform: translateX(-3px); }
	100% { transform: translateX(0); }
}
.oss-shake {
	animation: oss-shake 0.4s ease-out 1;
}
`
