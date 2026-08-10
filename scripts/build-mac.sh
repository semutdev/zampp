#!/bin/bash

# Hentikan script otomatis jika ada proses yang gagal (error)
set -e

# --- KONFIGURASI ---
APP_NAME="ZAMPP"
BUILD_DIR="build/bin"
APP_BUNDLE="$BUILD_DIR/$APP_NAME.app"
DMG_NAME="${APP_NAME}-macOS.dmg"
TEMP_DIR="dmg_temp"

# Warna untuk output terminal
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# --- PINDAH KE PROJECT ROOT ---
# Agar relative path (build/bin, dmg_temp) resolve dengan benar walau
# script dijalankan dari folder lain (mis: cd scripts && ./build-mac.sh).
cd "$(dirname "$0")/.."

# --- TRAP CLEANUP ---
# Kalau ada error di tengah proses (mis: hdiutil gagal), pastikan folder
# sementara dmg_temp tetap ke-cleanup.
trap 'rm -rf "$TEMP_DIR"' EXIT

# --- VALIDASI DEPENDENCIES ---
command -v wails >/dev/null 2>&1 || {
    echo -e "${RED}❌ Error: Wails CLI tidak ditemukan. Install dulu: brew install wails/tap/wails${NC}"
    exit 1
}
command -v hdiutil >/dev/null 2>&1 || {
    echo -e "${RED}❌ Error: hdiutil tidak ditemukan. Tool ini bawaan macOS, jalankan script ini di Mac.${NC}"
    exit 1
}

echo -e "${BLUE}🚀 Memulai proses Build & Release $APP_NAME...${NC}\n"

# 1. WAILS BUILD
echo -e "${YELLOW}📦 [1/3] Membangun aplikasi dengan Wails (Clean Build)...${NC}"
wails build -clean

# Cek apakah folder .app berhasil dibuat
if [ ! -d "$APP_BUNDLE" ]; then
    echo -e "${RED}❌ Error: $APP_BUNDLE tidak ditemukan. Proses Wails Build gagal!${NC}"
    exit 1
fi

# 2. PERSIAPAN FOLDER DMG & CREATE DMG
echo -e "${YELLOW}📂 [2/3] Menyiapkan struktur folder DMG & membuat DMG...${NC}"
rm -rf "$TEMP_DIR"
mkdir -p "$TEMP_DIR"

echo -e "   📋 Menyalin file .app..."
cp -R "$APP_BUNDLE" "$TEMP_DIR/"

echo -e "   🔗 Membuat shortcut folder Applications..."
ln -s /Applications "$TEMP_DIR/Applications"

echo -e "   💿 Mengompresi menjadi file DMG..."
# Hapus file DMG lama jika sudah ada agar tidak bentrok
rm -f "$BUILD_DIR/$DMG_NAME"
hdiutil create -volname "$APP_NAME" -srcfolder "$TEMP_DIR" -ov -format UDZO "$BUILD_DIR/$DMG_NAME"

# 3. CLEANUP (BERSIH-BERSIH)
# Catatan: trap di atas juga akan handle ini, tapi tetap eksplisit di akhir
# untuk konsistensi visual output.
echo -e "${YELLOW}🧹 [3/3] Membersihkan file sementara...${NC}"
rm -rf "$TEMP_DIR"

# Ambil absolute path untuk output yang lebih informatif
ABSOLUTE_DMG_PATH="$(pwd)/$BUILD_DIR/$DMG_NAME"

echo -e "\n${GREEN}✅ SELESAI! $APP_NAME berhasil di-build.${NC}"
echo -e "📁 Lokasi file DMG Anda: ${BLUE}$ABSOLUTE_DMG_PATH${NC}\n"