#!/bin/sh
# Updates the plumber pin in mise.toml to the latest stable release.
#
# The project's rule (see AGENTS.md, RELEASING.md): plumber is always pinned
# to an exact version and the pin is updated manually — run this script
# locally (mise run update-plumber) and validate the policy with the new
# version before landing the bump.
#
# Exit codes: 0 = up to date or updated, 1 = error (API unreachable, weird
# version...). A downgrade is never applied.
set -eu

mise_toml="$(dirname "$0")/../mise.toml"

tag=$(curl -fsSL https://api.github.com/repos/getplumber/plumber/releases/latest |
	sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [ -z "$tag" ]; then
	echo "error: could not read the latest plumber release from GitHub" >&2
	exit 1
fi
latest=${tag#v}

pinned=$(sed -n 's/.*"github:getplumber\/plumber" = "\([^"]*\)".*/\1/p' "$mise_toml")
if [ -z "$pinned" ]; then
	echo "error: no 'github:getplumber/plumber' pin found in $mise_toml" >&2
	exit 1
fi

if [ "$pinned" = "$latest" ]; then
	echo "plumber already at the latest release: $latest"
	exit 0
fi

# Refuse to downgrade (sort -V: the newest version sorts last).
if [ "$(printf '%s\n%s\n' "$pinned" "$latest" | sort -V | tail -n1)" != "$latest" ]; then
	echo "refusing to downgrade: pinned $pinned > latest $latest"
	exit 0
fi

sed -i "s|\"github:getplumber/plumber\" = \"[^\"]*\"|\"github:getplumber/plumber\" = \"$latest\"|" "$mise_toml"
echo "plumber pin updated: $pinned -> $latest"
