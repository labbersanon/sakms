Name:           sakms-node
Version:        %{version}
Release:        1%{?dist}
Summary:        sakms worker node daemon for GPU-accelerated media processing
License:        MIT
URL:            https://github.com/curtiswtaylorjr/sakms
Source0:        sakms-%{version}.tar.gz

# sakms-node and sakms-node-tray are pure Go (CGO_ENABLED=0);
# no GL or C build requirements needed.
BuildRequires:  golang >= 1.22

Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd

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
# wl-copy (wayland) or xclip/xsel (X11) for clipboard support — optional
Recommends:     wl-clipboard
Recommends:     libnotify

%description tray
sakms-node-tray is a CGo-free system tray companion (StatusNotifierItem /
dbus) that reflects the worker node lifecycle as a coloured icon:
amber = pending pairing, green = connected, red = not running.
It displays the 6-character pairing code and supports one-click copy.

%prep
%autosetup -n sakms-%{version}

%build
export CGO_ENABLED=0
export GOFLAGS="-mod=mod"

go build -trimpath -ldflags "-s -w" -o sakms-node     ./cmd/sakms-node/
go build -trimpath -ldflags "-s -w" -o sakms-node-tray ./cmd/sakms-node-tray/

%install
install -Dm755 sakms-node      %{buildroot}%{_bindir}/sakms-node
install -Dm755 sakms-node-tray %{buildroot}%{_bindir}/sakms-node-tray

install -Dm644 packaging/rpm/sakms-node.service \
    %{buildroot}%{_unitdir}/sakms-node.service

install -Dm644 packaging/rpm/sakms-node-tray.desktop \
    %{buildroot}%{_sysconfdir}/xdg/autostart/sakms-node-tray.desktop

install -Dm755 packaging/rpm/post-install.sh \
    %{buildroot}%{_datadir}/sakms-node/post-install.sh

install -dm700 %{buildroot}%{_sysconfdir}/sakms-node

%post
%systemd_post sakms-node.service
# Run the interactive config writer + service enabler only on fresh installs.
if [ $1 -eq 1 ]; then
    %{_datadir}/sakms-node/post-install.sh || true
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
%dir %attr(700,root,root) %{_sysconfdir}/sakms-node

%files tray
%{_bindir}/sakms-node-tray
%{_sysconfdir}/xdg/autostart/sakms-node-tray.desktop

%changelog
* %(date "+%a %b %d %Y") packager <packager@example.com> - %{version}-1
- Initial packaging
