# Packaging OpenWrt

Paquet OpenWrt natif (buildroot) : `Makefile` au format du buildroot OpenWrt
+ script procd. La compilation Go passe par le système de build Go du feed
`packages` (`golang-package.mk`), qui cross-compile pour la cible du SDK ;
le binaire est statique (pas de cgo), seul le *métadonné* du paquet dépend
de l'architecture.

## Ce que la CI produit

Quatre paquets par release, produits avec les SDK officiels OpenWrt
(`.github/workflows/release.yml`, job `openwrt`) :

| SDK                        | Architecture du paquet        | Artefact |
|----------------------------|-------------------------------|----------|
| release 24.10 (`armsr/armv8`) | `aarch64_generic`       | `.ipk`   |
| release 24.10 (`armsr/armv7`) | `arm_cortex-a15_neon-vfpv4` | `.ipk` |
| release 25.12 (`armsr/armv8`) | `aarch64_generic`       | `.apk`   |
| release 25.12 (`armsr/armv7`) | `arm_cortex-a15_neon-vfpv4` | `.apk` |

OpenWrt 24.10 utilise encore `opkg` (`.ipk`) ; la génération 25.12 est
passée à apk-tools (`.apk`). Les deux SDK sont épinglés par version ; le
sha256 de chaque téléchargement est vérifié contre le `sha256sums` publié
par upstream.

**Architecture ARM 32 bits** : OpenWrt découpe arm par classe de SoC
(`arm_cortex-a7`, `arm_cortex-a9`, `arm_cortex-a15_neon-vfpv4`…). Le paquet
arm32 ci-dessus déclare `arm_cortex-a15_neon-vfpv4` (cible générique
`armsr/armv7`) ; le binaire étant statique ARMv7, il tourne sur n'importe
quel ARMv7, mais l'installeur vérifie l'architecture déclarée. Pour une
autre sous-architecture, reconstruire avec le SDK correspondant (§ suivant).

## Construire à la main

Prérequis : un SDK OpenWrt correspondant à la cible visée (à télécharger
sur downloads.openwrt.org, section *SDK* de la cible), ~2 Go de RAM, git.
Le feed `packages` fournit le support Go ; la construction de
`golang/host` (toolchain Go) prend quelques minutes la première fois.

```sh
VERSION=1.0.0                       # la version du tag git visé
COMMIT=$(git rev-list -n1 v${VERSION})   # ou tout SHA du dépôt

# 1. Copier les sources du paquet dans l'arbre du SDK
cp -r packaging/openwrt /tmp/pkgspec
sed -i "s/^PKG_VERSION:=.*/PKG_VERSION:=$VERSION/"    /tmp/pkgspec/Makefile
sed -i "s/^PKG_SOURCE_VERSION:=.*/PKG_SOURCE_VERSION:=$COMMIT/" /tmp/pkgspec/Makefile
mkdir -p <sdk>/package/ycast-go
cp /tmp/pkgspec/Makefile <sdk>/package/ycast-go/
cp packaging/openwrt/files/* <sdk>/package/ycast-go/files/

# 2. Dans le SDK
cd <sdk>
./scripts/feeds update packages
./scripts/feeds install golang
echo 'CONFIG_PACKAGE_ycast-go=m' >> .config
make defconfig
make package/ycast-go/compile V=s
```

Le paquet sort dans `bin/packages/<arch>/` (`ycast-go_<version>-<release>_<arch>.ipk`
ou `ycast-go-<version>-<release>.apk` selon la génération du SDK).

## Contenu du paquet

- `/usr/bin/ycast-go` — binaire statique Go ;
- `/etc/ycast/stations.yml.example` — configuration d'exemple (la vraie
  `stations.yml` est à créer par l'admin, comme sur les autres paquets) ;
- `/etc/init.d/ycast` — service procd (respawn, logs dans syslog) ;
  démarre sur `-c /etc/ycast/stations.yml -p 80 -cache /tmp/ycast-icons`.

`DEPENDS:=+ca-bundle` : le trust store système utilisé par Go pour le HTTPS
vers radio-browser.info.

Le service n'est **pas** activé/démarré à l'installation : configurer
`stations.yml` et la redirection DNS d'abord, puis
`/etc/init.d/ycast enable && /etc/init.d/ycast start`.
Sur un SDK 25.12, le `.apk` produit n'est pas signé (le SDK n'a pas de
clé) : l'installer avec `apk add --allow-untrusted <fichier>.apk`.

## Version

`PKG_VERSION` et `PKG_SOURCE_VERSION` (SHA complet du commit) sont réécrits
par la CI au moment du tag ; à la main, suivre les commandes ci-dessus.
`PKG_RELEASE` est le numéro de révision du *paquet* (bumpé quand seul le
packaging change).
