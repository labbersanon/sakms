# Binaries are stripped (-s -w); suppress the empty debugsource subpackage.
%global debug_package %{nil}

Name:           sakms-node
Version:        %{version}
Release:        1%{?dist}
Summary:        sakms worker node daemon for GPU-accelerated media processing
License:        MIT
URL:            https://github.com/curtiswtaylorjr/sakms
Source0:        sakms-%{version}.tar.gz
Source1:        sakms-node.sysusers.conf

# sakms-node, sakms-node-tray, and the server binary remain pure Go
# (CGO_ENABLED=0) — no GL or C build requirements. Only the on-demand
# sakms-node-config GUI (the config subpackage below) requires CGO+OpenGL;
# its build-time GL/X11/Wayland toolchain deps are the %package config
# BuildRequires block further down.
BuildRequires:  golang >= 1.22
# Provides %{_unitdir} for the systemd unit file in %files below, and the
# %%sysusers_create_package macro used in %pre — COPR's minimal mock
# buildroot doesn't pull either in unless explicitly required.
BuildRequires:  systemd-rpm-macros

# --- Build-time toolchain for the sakms-node-config GUI subpackage ---
# CGO GUI toolchain: Fyne's desktop driver is GLFW (go-gl bindings) and
# needs a C compiler plus the X11/OpenGL/Wayland -devel headers at build
# time. COPR's minimal mock buildroot does NOT pull any of these in
# transitively (same gap already documented above for systemd-rpm-macros),
# so every one must be declared explicitly — a green local `rpmbuild -bb`
# on a full desktop proves nothing about completeness here (see the spec's
# packaging notes / plan B8).
BuildRequires:  gcc
BuildRequires:  pkgconf-pkg-config
BuildRequires:  libX11-devel
BuildRequires:  libXcursor-devel
BuildRequires:  libXrandr-devel
BuildRequires:  libXinerama-devel
BuildRequires:  libXi-devel
BuildRequires:  libXxf86vm-devel
BuildRequires:  mesa-libGL-devel
BuildRequires:  libxkbcommon-devel
# go-gl/glfw compiles BOTH its X11 and Wayland backends by default, so its
# cgo build references Wayland headers even though this binary runs via the
# X11/Xwayland path at runtime. wayland-devel also provides the
# wayland-scanner binary that %build runs to generate glfw's Wayland
# protocol headers (see the generation loop in %build).
BuildRequires:  wayland-devel
# wayland-scanner needs the protocol XML definitions; the system copies live
# here. (This repo vendors its own XML under go-gl/glfw's deps/wayland and
# %build generates from those, but wayland-protocols-devel is the confirmed
# system-level dependency and was missing from the original toolchain list —
# a genuine minimal-chroot build gap surfaced during Stage 1.)
BuildRequires:  wayland-protocols-devel

Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd
Requires:       python3
Requires:       curl

# rpm's file-ownership dependency generator adds Requires(pre): user(sakms-node)
# group(sakms-node) because %files below owns a directory as that user (see
# %attr(700,sakms-node,sakms-node) on %{_sysconfdir}/sakms-node). The
# sysusers.attr fileattrs generator is SUPPOSED to auto-emit a matching
# Provides from the Source1 sysusers.d fragment (see %pre), but empirically
# does not fire in this build environment (confirmed: `rpm -qp --provides`
# on a built RPM lists no user()/group() entry despite the fragment being
# correctly installed to %{_sysusersdir}). Declaring these explicitly makes
# the package self-resolvable under both dnf and plain `rpm -Uvh` regardless
# of whether that generator ever starts working here — %pre's
# %%sysusers_create_package still does the actual, idempotent user creation;
# this is belt-and-suspenders for the dependency solver only.
Provides:       user(sakms-node) = 1
Provides:       group(sakms-node) = 1

%description
sakms-node is the worker node daemon for the sakms self-hosted media
library server. It pairs with the sakms server via a one-time pairing
code, then receives GPU-accelerated phash jobs over a secure connection.

Install sakms-node-tray (a separate optional subpackage) to get a system
tray icon that displays the node's current state and pairing code.

%package tray
Summary:        System tray companion for sakms-node
Requires:       sakms-node = %{version}-%{release}
Requires:       dbus
# Provides the base hicolor theme directory structure our brand icon installs
# into, and gtk-update-icon-cache's target dir; not pulled in transitively by
# dbus/sakms-node, so declare it explicitly.
Requires:       hicolor-icon-theme
# wl-copy (wayland) or xclip/xsel (X11) for clipboard support — optional
Recommends:     wl-clipboard
Recommends:     libnotify
# The windowed configuration UI is a WEAK dependency, not a hard Requires:
# keeping it Recommends lets an operator remove sakms-node-config (and its
# GL stack) while keeping the tray, or install the tray without GL via
# --setopt=install_weak_deps=False. A plain `dnf install sakms-node-tray`
# still pulls it by default (weak deps install by default) — harmless on a
# machine that already has a display. The tray's "Open configuration…"
# handler os.Stat()s the binary first and notifies gracefully if absent,
# precisely because Recommends makes its presence non-guaranteed.
Recommends:     sakms-node-config

%description tray
sakms-node-tray is a CGo-free system tray companion (StatusNotifierItem /
dbus) that reflects the worker node lifecycle as a coloured icon:
amber = pending pairing, green = connected, red = not running.
It displays the 6-character pairing code and supports one-click copy.

%package config
Summary:        Windowed configuration UI for sakms-node
Requires:       sakms-node = %{version}-%{release}
# Runtime GL/X11 stack for Fyne's GLFW desktop driver. These live ONLY on
# this subpackage so a headless `sakms-node`-only install pulls ZERO GL
# libraries (the daemon depends on neither tray nor config).
Requires:       libX11
Requires:       libXcursor
Requires:       libXrandr
Requires:       libXinerama
Requires:       libXi
Requires:       libXxf86vm
Requires:       libxkbcommon
Requires:       mesa-libGL
Requires:       mesa-dri-drivers
# Xwayland lets a Wayland-only session host the X11 GLFW window (the config
# binary renders through the X11/Xwayland path by design).
Requires:       xorg-x11-server-Xwayland

%description config
sakms-node-config is an on-demand windowed configuration UI (Fyne) for
editing the worker node's media roots, path mappings, and dispatch-pause
state. Unlike the daemon and the tray — which stay CGo-free — this binary
alone requires CGO + OpenGL: Fyne's desktop driver is GLFW and creates a GL
context at startup. It is launched on demand from the tray's "Open
configuration…" item and from the desktop applications menu, so a GL-context
failure never affects the always-running daemon or status tray.

%prep
%autosetup -n sakms-%{version}

%build
export GOFLAGS="-mod=vendor"

# The daemon and the tray stay pure Go — build them with CGO disabled,
# exactly as before.
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o sakms-node      ./cmd/sakms-node/
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o sakms-node-tray ./cmd/sakms-node-tray/

# Generate the Wayland protocol headers that go-gl/glfw's Wayland backend
# #includes (wl_init.c / wl_window.c) but that `go build` does NOT
# auto-generate. Without these the CGO_ENABLED=1 build below fails with
# missing <name>-client-protocol.h / -code.h. Generated from the XML that
# already ships inside the vendored glfw tree, into that tree's src/ dir,
# BEFORE the cgo build. This MUST run every build: the COPR tarball is
# produced from a fresh `go mod vendor`, which ships the XML but none of
# these generated headers.
GLFW_SRC="vendor/github.com/go-gl/glfw/v3.4/glfw/glfw"
for proto in wayland xdg-shell xdg-decoration-unstable-v1 viewporter \
             relative-pointer-unstable-v1 pointer-constraints-unstable-v1 \
             fractional-scale-v1 xdg-activation-v1 idle-inhibit-unstable-v1; do
    wayland-scanner client-header "${GLFW_SRC}/deps/wayland/${proto}.xml" \
        "${GLFW_SRC}/src/${proto}-client-protocol.h"
    wayland-scanner private-code  "${GLFW_SRC}/deps/wayland/${proto}.xml" \
        "${GLFW_SRC}/src/${proto}-client-protocol-code.h"
done

# The configuration GUI alone requires CGO + OpenGL (Fyne/GLFW).
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o sakms-node-config ./cmd/sakms-node-config/

%install
install -Dm755 sakms-node        %{buildroot}%{_bindir}/sakms-node
install -Dm755 sakms-node-tray   %{buildroot}%{_bindir}/sakms-node-tray
install -Dm755 sakms-node-config %{buildroot}%{_bindir}/sakms-node-config

install -Dm644 packaging/rpm/sakms-node.service \
    %{buildroot}%{_unitdir}/sakms-node.service

install -Dm644 packaging/rpm/sakms-node-tray.desktop \
    %{buildroot}%{_sysconfdir}/xdg/autostart/sakms-node-tray.desktop

# Applications-menu launcher for the config UI (NoDisplay=false, so it is
# visible in the menu — unlike the tray's autostart-only .desktop above).
install -Dm644 packaging/rpm/sakms-node-config.desktop \
    %{buildroot}%{_datadir}/applications/sakms-node-config.desktop

# Brand icon for the tray launcher entry (Icon=sakms-node in the .desktop
# above resolves this by name via freedesktop icon-theme lookup). Copied
# straight from the frontend's single source of truth — no second checked-in
# copy — into the hicolor scalable/apps theme dir.
install -Dm644 frontend/public/favicon.svg \
    %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/sakms-node.svg

install -Dm755 packaging/rpm/post-install.sh \
    %{buildroot}%{_datadir}/sakms-node/post-install.sh

# sysusers.d fragment declaring the sakms-node system user/group (see %pre).
# Shipping this via the standard %_sysusersdir convention -- rather than a raw
# useradd shell call -- is what lets rpm's automatic file-ownership dependency
# generator resolve the user()/group() capability against THIS package's own
# sysusers.d entry instead of demanding an external provider that can never
# exist (the exact "conflicting requests: nothing provides user(sakms-node)"
# failure a raw useradd produces under both dnf and plain rpm -Uvh).
install -Dm0644 %SOURCE1 %{buildroot}%{_sysusersdir}/sakms-node.conf

# Phase 2 (OS-level namespace containment) activator. Root-run ONLY (0700
# root:root) — tighter than post-install.sh's 0755 because this helper reads
# mediaRoots (writable by the non-root daemon it contains) and writes a
# root-loaded systemd drop-in, so it must not be world-readable/executable.
# Deliberately NOT invoked from any scriptlet below (see %post) — Phase 2
# activation is a separate, explicit, manual operator action.
install -Dm700 packaging/rpm/apply-mediaroots.sh \
    %{buildroot}%{_libexecdir}/sakms-node/apply-mediaroots

install -dm700 %{buildroot}%{_sysconfdir}/sakms-node

%pre
# Security-hardening addendum: sakms-node runs as a dedicated, non-root
# system user (not User=root — see sakms-node.service) so a compromised or
# buggy daemon has only this user's own permissions, not full filesystem
# access. Uses the systemd-sysusers convention (Source1 fragment installed to
# %_sysusersdir/sakms-node.conf, see %install) via %%sysusers_create_package
# rather than a raw useradd shell call -- idempotent by design (safe on
# upgrade/reinstall, unlike a bare useradd) and, critically, this is what
# resolves rpm's automatic user()/group() file-ownership dependency against
# the package's own sysusers.d entry instead of an unsatisfiable external
# Requires (see the %install comment for the exact failure this replaces).
#
# This same invocation also creates the "sakms-media-config" shared group
# (the second, "g sakms-media-config -" line in Source1) -- the mediaRoots
# control-socket authorization boundary for sakms-node-tray (see
# sakms-node.service and the ralplan mediaRoots-UI plan's socket-perms
# section). One sysusers.d fragment, one macro call, both entries created
# idempotently; no separate group-creation step is needed here.
%sysusers_create_package %{name} %SOURCE1

%post
# NOTE: apply-mediaroots (Phase 2 OS-level namespace containment) is
# deliberately NOT invoked here or in %pre/%postun. Per Decision Driver 3 /
# Principle 3, auto-generating a mount-namespace drop-in and restarting the
# daemon inside a package transaction would silently change (and could break)
# an existing Phase-1-only install's sandbox on upgrade. Activation stays a
# separate, explicit, manual operator action: run
# %{_libexecdir}/sakms-node/apply-mediaroots after editing mediaRoots.
%systemd_post sakms-node.service
# Re-own the config directory on EVERY install/upgrade ($1 == 1 or 2), not
# just fresh installs -- this is what actually migrates an existing
# root-owned install (from before the security-hardening addendum, when
# the daemon ran as User=root) to the new non-root sakms-node user. Without
# this running unconditionally, an upgrading node's root-owned config.json
# becomes unreadable to the new non-root service user and the daemon fails
# to start (the exact config-ownership failure class this addendum's
# execution-model change was reviewed against). post-install.sh's own
# chown is fresh-install-only (it only runs there at all, per the $1 -eq 1
# guard below) and cannot cover this case by itself.
if [ -d %{_sysconfdir}/sakms-node ]; then
    chown -R sakms-node:sakms-node %{_sysconfdir}/sakms-node
fi
# Reload systemd's unit cache on EVERY install/upgrade ($1 == 1 or 2), not
# just fresh installs -- an upgrading node's already-running systemd has the
# OLD sakms-node.service cached (e.g. without Delegate=yes, see that unit
# file's comment) until told to re-read it. The systemd-enable macro used
# above only handles enable/preset on fresh install; it does not by itself
# guarantee systemd has re-parsed a changed unit file on upgrade, so this is
# called explicitly and unconditionally, same reasoning as the config-dir
# re-own immediately above.
systemctl daemon-reload || :
# Run the interactive config writer + service enabler only on fresh installs.
# No `|| true`: post-install.sh's own exit code must propagate so a genuine
# failure (e.g. no SAKMS_SERVER_URL in a non-interactive install) surfaces as
# a real %post/dnf failure, not a silently-swallowed success.
if [ $1 -eq 1 ]; then
    %{_datadir}/sakms-node/post-install.sh
fi

# Add the console/desktop user to the sakms-media-config shared group (see
# sakms-node.service, sakms-node.sysusers.conf) so sakms-node-tray, running
# as that user, can reach the mediaRoots control socket
# (/run/sakms-node/control.sock). Runs on EVERY install/upgrade ($1 == 1 or
# 2), same reasoning as the config-dir re-own above -- an upgrade from a
# pre-mediaRoots-UI package also needs this membership added, not just a
# fresh install. Best-effort console-user detection (a logind seat0 session,
# falling back to the first human-range UID) -- "which exact user is the
# console user" is inherently host-specific for a package install; if
# detection fails, the admin is told exactly how to do it by hand rather than
# the install silently leaving the tray unusable.
#
# %%sysusers_create_package (in %pre, above) already created the group
# itself; this only adds a member to it, which sysusers.d's static fragment
# cannot do for a username only known at install time.
CONSOLE_USER="$(loginctl list-sessions --no-legend 2>/dev/null | awk '$4 == "seat0" {print $3; exit}')"
if [ -z "$CONSOLE_USER" ]; then
    CONSOLE_USER="$(getent passwd | awk -F: '$3 >= 1000 && $3 < 60000 && $7 !~ /nologin|false/ {print $1; exit}')"
fi
if [ -n "$CONSOLE_USER" ]; then
    usermod -aG sakms-media-config "$CONSOLE_USER" || :
    echo "sakms-node: added '$CONSOLE_USER' to the 'sakms-media-config' group" \
         "for local mediaRoots control (sakms-node-tray)." \
         "IMPORTANT: this user must log out and back in (or reboot) before" \
         "the new group membership takes effect in their desktop session --" \
         "supplementary group membership is fixed at login/session start, so" \
         "an already-running session (or the tray started from one) will see" \
         "connect() permission-denied on the control socket until then." >&2
else
    echo "sakms-node: could not auto-detect a console/desktop user to add to" \
         "the 'sakms-media-config' group. Add the desktop user manually:" \
         "usermod -aG sakms-media-config <username>; that user must then log" \
         "out and back in (or reboot) before sakms-node-tray's local" \
         "mediaRoots control socket becomes reachable." >&2
fi

%preun
%systemd_preun sakms-node.service

%postun
%systemd_postun_with_restart sakms-node.service

%files
%license LICENSE
%doc README.md
%{_bindir}/sakms-node
%{_unitdir}/sakms-node.service
%{_datadir}/sakms-node/post-install.sh
%attr(0700,root,root) %{_libexecdir}/sakms-node/apply-mediaroots
%dir %attr(700,sakms-node,sakms-node) %{_sysconfdir}/sakms-node
%{_sysusersdir}/sakms-node.conf

%posttrans tray
# Refresh the icon-theme cache once per transaction so the freshly installed
# sakms-node.svg is picked up. Guarded with `|| :` so a missing
# gtk-update-icon-cache binary (plausible on a minimal/headless host, which
# this daemon's is commonly) doesn't fail the transaction.
gtk-update-icon-cache -q %{_datadir}/icons/hicolor &>/dev/null || :

%files tray
%{_bindir}/sakms-node-tray
%{_sysconfdir}/xdg/autostart/sakms-node-tray.desktop
%{_datadir}/icons/hicolor/scalable/apps/sakms-node.svg

# No scriptlets for the config subpackage: it ships no systemd unit (it is
# an on-demand GUI, not a service), so there is nothing to daemon-reload.
# Its Icon=sakms-node resolves against the brand icon shipped by the tray
# subpackage (the realistic install path — the tray Recommends config, so
# installing either GUI pulls the icon in); a config-only install without
# the tray falls back to a generic menu icon. The icon is deliberately NOT
# moved to the base package, which stays GL/GUI-free for headless nodes.
%files config
%{_bindir}/sakms-node-config
%{_datadir}/applications/sakms-node-config.desktop

%changelog
* %(date "+%a %b %d %Y") packager <packager@example.com> - %{version}-1
- Initial packaging
- Add sakms-node-config subpackage: the on-demand windowed configuration UI
  (Fyne/GLFW). It is the only binary that requires CGO+OpenGL; the daemon and
  tray stay CGO-free. Carries all GL/X11 runtime Requires plus
  xorg-x11-server-Xwayland on its own subpackage so a headless sakms-node
  install pulls zero GL libraries. Ships /usr/bin/sakms-node-config and a
  visible applications-menu .desktop. The tray Recommends (weak dep) it so it
  stays optional/removable. %build now generates go-gl/glfw's Wayland protocol
  headers via wayland-scanner before the CGO_ENABLED=1 build, and the config
  subpackage's GL/X11/Wayland -devel BuildRequires are declared explicitly
  (COPR's minimal buildroot does not pull them in transitively)
- Add apply-mediaroots (Phase 2 OS-level namespace containment activator) as a
  root-only helper under %{_libexecdir}/sakms-node; not auto-invoked on install
- Switch sakms-node user/group creation from a raw %pre useradd to the
  systemd-sysusers convention (Source1 sysusers.d fragment +
  %%sysusers_create_package), fixing a real install-time failure: rpm's
  automatic file-ownership dependency generator added an unsatisfiable
  Requires(postun) on user(sakms-node)/group(sakms-node) that no package
  could ever provide under the old raw-useradd approach
- Add packaging/systemd support for the sakms-node-tray mediaRoots control
  socket (/run/sakms-node/control.sock): sakms-node.service gains
  RuntimeDirectory=sakms-node, RuntimeDirectoryMode=0750, and
  SupplementaryGroups=sakms-media-config; sakms-node.sysusers.conf gains a
  "g sakms-media-config -" line creating the shared authorization group
  (created via the same %%sysusers_create_package call as the sakms-node
  user, no new macro invocation needed); %post now adds the detected
  console/desktop user to sakms-media-config on every install/upgrade, since
  group membership is the sole authorization boundary for the control socket
  (see the sakms-node-tray mediaRoots UI plan)
- IMPORTANT: a user added to sakms-media-config (by %post above, or by hand)
  must log out and back in (or reboot) before that membership takes effect
  in their desktop session -- supplementary group membership is fixed at
  Linux login/session start, so an already-running session (including one
  where the group was just granted) will see connect() permission-denied on
  the mediaRoots control socket until the next login
