#!/usr/bin/env sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
jellyfin_version=${JELLYFIN_VERSION:-12.0.0}
target_framework=${TARGET_FRAMEWORK:-net10.0}

dotnet publish "$project_dir/JAVBeacon.Jellyfin/JAVBeacon.Jellyfin.csproj" \
  -c Release \
  -o "$project_dir/dist" \
  -p:JellyfinVersion="$jellyfin_version" \
  -p:TargetFramework="$target_framework" \
  -p:TargetFrameworks="$target_framework"
