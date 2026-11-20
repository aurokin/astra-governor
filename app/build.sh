#!/bin/sh
# Builds build/app/astra.apk with the SDK command-line tools only (no Gradle).
#
# The signing key is not in the repository. KEYSTORE points at it; without one
# a throwaway key is made, and that build cannot update an app signed with
# another key.
set -e
cd "$(dirname "$0")"
SDK=${ANDROID_HOME:-/opt/homebrew/share/android-commandlinetools}
BT=$SDK/build-tools/36.0.0
JAR=$SDK/platforms/android-36/android.jar
KEYSTORE=${KEYSTORE:-$HOME/workspace/astra-root/governor/app/debug.keystore}
out=../build/app
rm -rf "$out"
mkdir -p "$out/gen" "$out/classes" "$out/dex"
"$BT/aapt2" compile --dir res -o "$out/res.zip"
"$BT/aapt2" link -o "$out/base.apk" -I "$JAR" --manifest AndroidManifest.xml --java "$out/gen" "$out/res.zip"
javac --release 17 -Xlint:-options -cp "$JAR" -d "$out/classes" $(find src "$out/gen" -name '*.java')
"$BT/d8" --lib "$JAR" --min-api 34 --output "$out/dex" $(find "$out/classes" -name '*.class')
# Info-ZIP takes the DEX file's timestamp for its entry, so fix it: the same
# sources and key then give the same APK.
touch -t 202601010000 "$out/dex/classes.dex"
(cd "$out/dex" && TZ=UTC zip -q -X ../base.apk classes.dex)
"$BT/zipalign" -f 4 "$out/base.apk" "$out/aligned.apk"
if [ ! -f "$KEYSTORE" ]; then
  KEYSTORE=$out/throwaway.keystore
  keytool -genkeypair -keystore "$KEYSTORE" -storepass android -keypass android \
    -alias astra -keyalg RSA -keysize 2048 -validity 10000 -dname "CN=Astra" >/dev/null 2>&1
  echo "no signing key at \$KEYSTORE: signed with a throwaway key" >&2
fi
"$BT/apksigner" sign --ks "$KEYSTORE" --ks-pass pass:android --out "$out/astra.apk" "$out/aligned.apk"
ls -la "$out/astra.apk"
