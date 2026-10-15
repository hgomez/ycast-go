# OpenWrt package for ycast-go.
#
# This is a buildroot package Makefile: it is meant to be dropped into an
# OpenWrt SDK (buildroot or SDK) tree, e.g. as package/ycast-go/Makefile —
# see README.md next to this file for the exact commands.
#
# The binary is built by the OpenWrt Go build system (packages feed), which
# cross-compiles for the SDK's target (GOARCH/GOARM/CPU flags handled there),
# so the resulting package carries the SDK's architecture (aarch64_generic,
# arm_cortex-a15_neon-vfpv4, mips_24kc…). The binary itself is static (no
# cgo): only the package metadata is tied to the architecture.

include $(TOPDIR)/rules.mk

PKG_NAME:=ycast-go
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

# Sources are fetched from the upstream git repository at the commit set
# below (a full SHA; CI rewrites this line at release time). A commit pin
# instead of a GitHub codeload tarball: those tarballs are not byte-stable
# over time, so they cannot be checksummed reliably.
PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=https://github.com/hgomez/ycast-go.git
PKG_SOURCE_VERSION:=0000000000000000000000000000000000000000

PKG_MAINTAINER:=Henri Gomez <henri.gomez@gmail.com>
PKG_LICENSE:=GPL-3.0-or-later
PKG_LICENSE_FILES:=LICENSE

# Go cross-compilation support: golang/host provides the Go toolchain
# (built once in the SDK tree, cached in build_dir/).
PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
# ycast-go is pure Go; the MIPS16 optimizer is irrelevant and harmful.
PKG_USE_MIPS16:=0

GO_PKG:=github.com/hgomez/ycast-go
GO_PKG_BUILD_PKG:=github.com/hgomez/ycast-go/cmd/ycast-go
GO_PKG_LDFLAGS:=-s -w
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
# The Go build system lives in the packages feed, not in the base buildroot.
# It is fetched with:
#   ./scripts/feeds update packages && ./scripts/feeds install golang
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/ycast-go
  SECTION:=net
  CATEGORY:=Network
  TITLE:=vTuner replacement for network receivers
  URL:=https://github.com/hgomez/ycast-go
  # ca-bundle provides the system trust store Go uses for HTTPS to
  # radio-browser.info; the relayed streams are fetched by ycast-go itself.
  DEPENDS:=+ca-bundle
endef

define Package/ycast-go/description
  Self-hosted replacement for the vTuner internet radio service used by
  network receivers (Denon, Marantz, Yamaha, Onkyo...). Serves the XML
  menus from a local stations.yml and the radio-browser.info directory,
  and relays HTTPS streams the receivers cannot fetch themselves.
  Remember: the receiver must resolve its vTuner domains (e.g.
  denon.vtuner.com) to this box via dnsmasq, and port 80 must be free
  (move LuCI's uhttpd to 8080 first).
endef

define Package/ycast-go/install
	$(call GoPackage/Package/Install,$(1))
	$(INSTALL_DIR) $(1)/etc/ycast $(1)/etc/init.d
	$(INSTALL_DATA) ./files/stations.yml.example $(1)/etc/ycast/stations.yml.example
	$(INSTALL_BIN) ./files/ycast-go.init $(1)/etc/init.d/ycast
endef

$(eval $(call BuildPackage,ycast-go))
