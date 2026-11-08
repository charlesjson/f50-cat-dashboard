#!/system/bin/sh
# Run from the extracted release directory as root. No network downloads.
set -eu
package_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
clash_dir=/data/clash
ui_dir="$clash_dir/Proxy/WebUI/zashboard"
manager_dir="$clash_dir/Manager"
service="$clash_dir/Scripts/Manager.Service"
boot_file=/sdcard/ufi_tools_boot.sh
stamp=$(date +%Y%m%d-%H%M%S)
backup_dir="$clash_dir/dashboard-backups/$stamp"
staging_ui="$ui_dir.stage-$stamp"

[ "$(id -u)" = 0 ] || { echo '请以 root 运行'; exit 1; }
[ -x "$clash_dir/Proxy/Clash.Core" ] || { echo '请先安装猫猫和 Mihomo 内核'; exit 1; }
[ -f "$clash_dir/Proxy/config.yaml" ] || { echo '缺少现有 Mihomo 配置'; exit 1; }
[ -f "$package_dir/f50-manager" ] && [ -f "$package_dir/dist/index.html" ] || { echo '安装包不完整'; exit 1; }
[ -d "$ui_dir" ] || { echo '此安装器需要现有 zashboard 目录'; exit 1; }

umask 077
mkdir -p "$backup_dir" "$manager_dir"
chmod 700 "$backup_dir" "$manager_dir"
cp -p "$clash_dir/Proxy/config.yaml" "$backup_dir/config.yaml"
cp -p "$boot_file" "$backup_dir/boot.sh"
if [ -f "$service" ]; then cp -p "$service" "$backup_dir/Manager.Service"; fi
if [ -f "$manager_dir/state.json" ]; then cp -p "$manager_dir/state.json" "$backup_dir/state.json"; fi
if [ -f "$manager_dir/f50-manager" ]; then cp -p "$manager_dir/f50-manager" "$backup_dir/f50-manager"; fi
cp -p "$clash_dir/Scripts/fast_sha256" "$backup_dir/fast_sha256"
if [ -x "$service" ]; then "$service" stop; fi

rollback() {
    echo '安装未完成，正在恢复原版本'
    if [ -x "$service" ]; then "$service" stop || true; fi
    cp -p "$backup_dir/config.yaml" "$clash_dir/Proxy/config.yaml"
    cp -p "$backup_dir/fast_sha256" "$clash_dir/Scripts/fast_sha256"
    cp -p "$backup_dir/boot.sh" "$boot_file"
    if [ -d "$staging_ui" ]; then mv "$staging_ui" "$backup_dir/failed-staging-ui"; fi
    if [ -d "$backup_dir/ui" ]; then
        if [ -d "$ui_dir" ]; then mv "$ui_dir" "$backup_dir/failed-ui"; fi
        mv "$backup_dir/ui" "$ui_dir"
    fi
    if [ -f "$backup_dir/state.json" ]; then
        cp -p "$backup_dir/state.json" "$manager_dir/state.json"
    elif [ -f "$manager_dir/state.json" ]; then
        mv "$manager_dir/state.json" "$backup_dir/failed-state.json"
    fi
    "$manager_dir/f50-manager" --reload-only || true
    if [ -f "$backup_dir/f50-manager" ]; then cp -p "$backup_dir/f50-manager" "$manager_dir/f50-manager"; fi
    if [ -f "$backup_dir/Manager.Service" ]; then
        cp -p "$backup_dir/Manager.Service" "$service"
        "$service" start || true
    fi
    echo "原文件与诊断备份位于 $backup_dir"
}
trap 'rollback' EXIT
cp "$package_dir/f50-manager" "$manager_dir/f50-manager.new"
chmod 700 "$manager_dir/f50-manager.new"
mv "$manager_dir/f50-manager.new" "$manager_dir/f50-manager"
cp "$package_dir/Manager.Service" "$service"
chmod 700 "$service"

# Validates all existing nodes/rules, saves a transaction backup, and hot reloads.
"$manager_dir/f50-manager" --initialize
cp -R "$package_dir/dist" "$staging_ui"
chmod -R a+rX "$staging_ui"
mv "$ui_dir" "$backup_dir/ui"
mv "$staging_ui" "$ui_dir"
"$service" start

if ! grep -q '/data/clash/Scripts/Manager.Service start' "$boot_file"; then
    cat >> "$boot_file" <<'BOOT'

# F50_CAT_DASHBOARD_START
/data/clash/Scripts/Manager.Service start
# F50_CAT_DASHBOARD_END
BOOT
fi
trap - EXIT
echo "安装成功。面板地址：http://设备IP:7788/ui/#/config"
echo "回退备份：$backup_dir"
