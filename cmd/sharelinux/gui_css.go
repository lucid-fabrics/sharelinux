//go:build linux

package main

// stylesheet is the "Night Glass" look: a dark ground, soft cards, one blue
// accent and glass panels over the Mac's picture. The colours come from the
// design board; libadwaita's own variables are set here so its dialogs, menus
// and entries follow.
const stylesheet = `
:root {
	--window-bg-color: #0B0E13;
	--window-fg-color: #E8ECF2;
	--view-bg-color: #0B0E13;
	--view-fg-color: #E8ECF2;
	--headerbar-bg-color: #0B0E13;
	--headerbar-fg-color: #E8ECF2;
	--headerbar-backdrop-color: #0B0E13;
	--headerbar-border-color: transparent;
	--headerbar-shade-color: transparent;
	--card-bg-color: #131922;
	--card-fg-color: #E8ECF2;
	--card-shade-color: rgba(0, 0, 0, 0.3);
	--dialog-bg-color: #131922;
	--dialog-fg-color: #E8ECF2;
	--popover-bg-color: #1C2433;
	--popover-fg-color: #E8ECF2;
	--sidebar-bg-color: #10141B;
	--sidebar-fg-color: #E8ECF2;
	--sidebar-backdrop-color: #10141B;
	--sidebar-shade-color: transparent;
	--accent-bg-color: #5B8CFF;
	--accent-fg-color: #0B0E13;
	--accent-color: #8FB0FF;
	--success-color: #34D399;
	--destructive-bg-color: #3A1D22;
	--destructive-fg-color: #FF9A9A;
}

window, dialog {
	font-family: Inter, "Adwaita Sans", system-ui, sans-serif;
	font-size: 14px;
}

headerbar {
	min-height: 40px;
	background: transparent;
	box-shadow: none;
	border: none;
}

/* ---- sidebar ---- */
.oss-sidebar {
	background: #10141B;
	border-right: 1px solid #1E2530;
	padding: 20px 14px 16px 14px;
}
.oss-brand-name { font-weight: 700; font-size: 16px; letter-spacing: -0.01em; }
.oss-brand-tag { font-size: 12px; color: #8A94A3; }
list.oss-nav { background: transparent; }
list.oss-nav > row {
	min-height: 44px;
	padding: 0 12px;
	margin-bottom: 6px;
	border-radius: 10px;
	color: #B4BDCA;
	background: transparent;
}
list.oss-nav > row:hover { background: rgba(255, 255, 255, 0.04); }
list.oss-nav > row:selected { background: #1A2232; color: #E8ECF2; font-weight: 600; }
list.oss-nav > row:selected image { color: #8FB0FF; }
.oss-count {
	font-size: 12px;
	font-weight: 600;
	color: #8FB0FF;
	background: #22304D;
	padding: 2px 8px;
	border-radius: 9999px;
}
.oss-helper-card {
	padding: 14px;
	border-radius: 12px;
	background: #151A22;
	border: 1px solid #222A35;
}
.oss-helper-card:hover { border-color: #2C3646; }
.oss-helper-title { font-weight: 600; font-size: 13px; }
.oss-helper-body { font-size: 12px; color: #8A94A3; }

/* ---- page ---- */
.oss-page-title { font-size: 26px; font-weight: 700; letter-spacing: -0.02em; }
.oss-overline { font-size: 12px; font-weight: 600; letter-spacing: 0.06em; color: #8A94A3; }
.oss-section-title { font-size: 15px; font-weight: 600; color: #B4BDCA; }
.oss-muted { color: #8A94A3; }
.oss-caption { font-size: 12px; color: #8A94A3; }
.oss-hint-line { font-size: 13px; color: #8A94A3; }

entry.oss-search, searchentry.oss-search {
	min-height: 44px;
	padding: 0 14px;
	border-radius: 10px;
	background: #151A22;
	border: 1px solid #222A35;
	color: #E8ECF2;
	box-shadow: none;
}
searchentry.oss-search:focus-within { border-color: #5B8CFF; }
searchentry.oss-search image { color: #8A94A3; }

/* ---- buttons ---- */
button.oss-primary {
	min-height: 44px;
	padding: 0 18px;
	border-radius: 10px;
	border: none;
	background: #5B8CFF;
	color: #0B0E13;
	font-weight: 600;
	box-shadow: none;
}
button.oss-primary:hover { background: #7099FF; }
button.oss-primary.hero { min-height: 48px; padding: 0 22px; border-radius: 12px; font-weight: 700; font-size: 15px; }
button.oss-secondary {
	min-height: 48px;
	padding: 0 18px;
	border-radius: 12px;
	border: 1px solid #2C3646;
	background: #161C26;
	color: #E8ECF2;
	font-weight: 600;
	box-shadow: none;
}
button.oss-secondary:hover { background: #1C2433; }
button.oss-soft {
	min-height: 44px;
	border-radius: 10px;
	border: none;
	background: #22304D;
	color: #CFE0FF;
	font-weight: 600;
	box-shadow: none;
}
button.oss-soft:hover { background: #2A3B5E; }
button.oss-ghost {
	min-height: 44px;
	border-radius: 10px;
	border: 1px solid #2C3646;
	background: transparent;
	color: #B4BDCA;
	font-weight: 600;
	box-shadow: none;
}
button.oss-ghost:hover { background: rgba(255, 255, 255, 0.04); }
button.oss-icon {
	min-width: 36px;
	min-height: 36px;
	padding: 0;
	border-radius: 10px;
	background: transparent;
	color: #8A94A3;
	box-shadow: none;
}
button.oss-icon:hover { background: rgba(255, 255, 255, 0.06); color: #E8ECF2; }

/* ---- cards ---- */
.oss-card {
	padding: 18px;
	border-radius: 16px;
	background: #131922;
	border: 1px solid #222A35;
}
.oss-hero {
	padding: 24px;
	border-radius: 20px;
	background: #131922;
	border: 1px solid #222A35;
}
.oss-art {
	min-height: 110px;
	border-radius: 12px;
	background: #0F141C;
}
.oss-card-name { font-weight: 600; font-size: 15px; }
.oss-hero-name { font-weight: 700; font-size: 28px; letter-spacing: -0.02em; }
.oss-status-line { font-size: 12px; color: #8A94A3; }
.oss-hero .oss-status-line { font-size: 14px; color: #B4BDCA; }

.oss-thumb {
	border-radius: 14px;
	background: #0E1420;
	border: 1px solid #263042;
}
.oss-thumb-empty {
	border-radius: 14px;
	background: linear-gradient(160deg, #2B3F78 0%, #121A2E 55%, #0B0F1A 100%);
	border: 1px solid #263042;
}
.oss-thumb-badge {
	font-size: 11px;
	font-weight: 600;
	letter-spacing: 0.04em;
	color: #CFD8E6;
	background: rgba(11, 14, 19, 0.6);
	padding: 4px 8px;
	border-radius: 6px;
}

.oss-dot {
	min-width: 8px;
	min-height: 8px;
	border-radius: 9999px;
	background: #5B6575;
}
.oss-dot.online { background: #34D399; }
.oss-status.online { color: #34D399; }

flowboxchild { padding: 0; }
.oss-chip {
	padding: 6px 10px;
	border-radius: 8px;
	background: #1C2433;
	color: #C9D4E6;
	font-size: 12px;
	font-weight: 600;
}
.oss-chip.hdr { background: #2A2340; color: #D9CCFF; }
.oss-chip.success { color: #34D399; background: alpha(#34D399, 0.12); }
.oss-chip.warning { color: @warning_color; background: alpha(@warning_color, 0.12); }

.oss-keycap {
	padding: 2px 7px;
	border-radius: 6px;
	border: 1px solid #2C3646;
	background: #1C2433;
	color: #CFD8E6;
	font-size: 12px;
}

/* ---- session ---- */
.oss-glass {
	background: rgba(16, 20, 27, 0.9);
	border: 1px solid rgba(255, 255, 255, 0.10);
	box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
	color: #E8ECF2;
}
.oss-bar { padding: 6px; border-radius: 16px; }
.oss-bar-name { font-weight: 600; font-size: 13px; }
.oss-bar-delay { font-size: 12px; color: #9AA4B2; }
.oss-bar-sep { min-width: 1px; min-height: 24px; background: rgba(255, 255, 255, 0.12); margin: 0 4px; }
button.oss-tool, menubutton.oss-tool > button {
	min-width: 44px;
	min-height: 44px;
	padding: 0;
	border-radius: 10px;
	border: none;
	background: transparent;
	color: #E8ECF2;
	box-shadow: none;
}
button.oss-tool:hover, menubutton.oss-tool > button:hover { background: rgba(255, 255, 255, 0.08); }
button.oss-tool.on, menubutton.oss-tool > button.on { background: #22304D; color: #CFE0FF; }
button.oss-tool.danger { background: #3A1D22; color: #FF9A9A; }
button.oss-tool.danger:hover { background: #4A252B; }
button.oss-tool:disabled { opacity: 0.4; }

.oss-stats {
	padding: 16px;
	border-radius: 14px;
	background: rgba(16, 20, 27, 0.9);
	border: 1px solid rgba(255, 255, 255, 0.10);
	box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
	color: #E8ECF2;
}
.oss-stats .overline { font-size: 11px; font-weight: 600; letter-spacing: 0.06em; color: #9AA4B2; }
.oss-stats label.name { color: #9AA4B2; font-size: 12px; }
.oss-stats label.value { font-weight: 600; font-size: 12px; }

.oss-hint {
	padding: 10px 16px;
	border-radius: 12px;
	background: rgba(16, 20, 27, 0.9);
	border: 1px solid rgba(255, 255, 255, 0.10);
	font-size: 13px;
	color: #CFD8E6;
}

.oss-copy-card {
	background: rgba(16, 20, 27, 0.9);
	border: 1px solid rgba(255, 255, 255, 0.10);
	color: #E8ECF2;
	border-radius: 14px;
	padding: 10px 12px 12px 16px;
	box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
}

.oss-connected { color: #34D399; font-size: 12px; }
.oss-title-line { font-weight: bold; font-size: 14px; }

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
