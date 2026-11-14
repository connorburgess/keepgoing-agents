#!/bin/sh
# Builds KeepgoingMenu.app inside this package's .build directory, so the
# menu runs from an app bundle and can post notifications. It never installs,
# registers or opens the app: it only writes .build/KeepgoingMenu.app.
# Remove it with: rm -rf integrations/macos/.build/KeepgoingMenu.app
set -eu
package=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# The menu's version, from the one place it is kept. It becomes the app's
# CFBundleShortVersionString, which the menu reports to the CLI.
menu_version=$(cat "$package/VERSION")
case "$menu_version" in
  *[!0-9.]* | "" | .* | *. | *..*) echo "VERSION must be a plain version such as 0.1.0, not '$menu_version'" >&2; exit 1 ;;
esac
swift build --package-path "$package" -c release
app="$package/.build/KeepgoingMenu.app"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
cp "$package/.build/release/KeepgoingMenu" "$app/Contents/MacOS/KeepgoingMenu"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key>
	<string>local.keepgoing.menu-preview</string>
	<key>CFBundleName</key>
	<string>keepgoing Menu</string>
	<key>CFBundleExecutable</key>
	<string>KeepgoingMenu</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$menu_version</string>
	<key>CFBundleVersion</key>
	<string>0</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>LSUIElement</key>
	<true/>
</dict>
</plist>
PLIST
# An ad-hoc signature, for this computer only: macOS needs a signed bundle to
# keep its notification permission.
codesign --force --sign - "$app"
echo "Built $app"
echo "Open it with: $app/Contents/MacOS/KeepgoingMenu --executable /path/to/keepgoing"
