#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
# Run as the normal dev user. Source/tool preparation is documented in README.md.
set -euo pipefail
cd "$(dirname "$0")"
test "$(id -u)" != 0
test "$(uname -s)" = Linux
test "$(uname -m)" = x86_64
build_root="$PWD/.build"
test "$(git -C "$build_root/source" rev-parse HEAD)" = 47351e59e2965e350f9d4098f15ecbe6b3850f6f
cmake_bin="${CMAKE:-$build_root/tools/bin/cmake}"
export LD_LIBRARY_PATH="$build_root/tools/lib:${LD_LIBRARY_PATH:-}"
patch_digest="$(sha256sum instrument.py native_observer.h | sha256sum | cut -d ' ' -f1)"
if [ "$(cat "$build_root/instrumented/.native-instrumented" 2>/dev/null || true)" != "$patch_digest" ]; then
    mkdir -p "$build_root/instrumented"
    cp -a "$build_root/source/." "$build_root/instrumented/"
    python3 instrument.py "$build_root/instrumented"
    echo "$patch_digest" > "$build_root/instrumented/.native-instrumented"
fi
"$cmake_bin" -S "$build_root/instrumented" -B "$build_root/static" \
    -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX=/opt/libyang \
    -DCMAKE_C_FLAGS="-I$PWD" -DENABLE_TESTS=OFF -DENABLE_VALGRIND_TESTS=OFF \
    -DENABLE_TOOLS=OFF -DENABLE_YANGLINT_INTERACTIVE=OFF
"$cmake_bin" --build "$build_root/static" -j2
git -C "$build_root/instrumented" diff --no-ext-diff > "$build_root/instrumentation.patch"
gcc -DNDEBUG -O3 -Wall -Wextra -Wpedantic -std=c11 -Wl,--wrap=free -I"$build_root/instrumented/src" \
    -I"$build_root/static/libyang" -I"$build_root/static/compat" -I"$build_root/instrumented/src/plugins_exts" \
    -o "$build_root/driver.new" driver.c ../cjson/cJSON.c "$build_root/static/libyang.a" -lpcre2-8 -lm -lpthread -ldl
mv "$build_root/driver.new" "$build_root/driver"
# The untouched oracle image's Release objects provide the interference control.
find "$build_root/config/CMakeFiles/yangobj.dir" -name '*.o' -print0 | xargs -0 ar rcs "$build_root/libyang-unmodified.a"
gcc -DNR_UNMODIFIED -DNDEBUG -O3 -Wall -Wextra -Wpedantic -std=c11 -Wl,--wrap=free -I"$build_root/source/src" \
    -I"$build_root/config/libyang" -I"$build_root/config/compat" -I"$build_root/source/src/plugins_exts" \
    -o "$build_root/driver-unmodified.new" driver.c ../cjson/cJSON.c "$build_root/libyang-unmodified.a" -lpcre2-8 -lm -lpthread -ldl
mv "$build_root/driver-unmodified.new" "$build_root/driver-unmodified"
{ git -C "$build_root/source" rev-parse HEAD; sha256sum instrument.py native_observer.h driver.c "$build_root/instrumentation.patch"; \
  gcc -dumpfullversion; "$cmake_bin" --version | head -1; uname -m; \
  "$build_root/driver" --abi; \
  sed -n '/^CMAKE_BUILD_TYPE:/p; /^BUILD_SHARED_LIBS:/p; /^CMAKE_C_FLAGS/p' "$build_root/static/CMakeCache.txt"; \
  cat "$build_root/static/CMakeFiles/yang.dir/flags.make"; } > "$build_root/BUILDINFO"
