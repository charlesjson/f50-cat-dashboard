#!/usr/bin/env sh
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
rm -rf artifacts/package
mkdir -p artifacts/package
pnpm build:no-fonts
(
    cd manager
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o ../artifacts/package/f50-manager .
)
cp -R dist artifacts/package/
cp device/install.sh device/Manager.Service LICENSE NOTICE artifacts/package/
tar -czf artifacts/f50-cat-dashboard-arm64.tar.gz -C artifacts/package .
python3 - <<'PY'
from pathlib import Path
from zipfile import ZipFile, ZIP_DEFLATED
with ZipFile('artifacts/dist.zip','w',ZIP_DEFLATED) as z:
    for p in sorted(Path('dist').rglob('*')):
        if p.is_file(): z.write(p,p.relative_to('dist'))
PY
(
    cd artifacts
    sha256sum f50-cat-dashboard-arm64.tar.gz dist.zip > SHA256SUMS
)
echo 'Release files are in artifacts/'
