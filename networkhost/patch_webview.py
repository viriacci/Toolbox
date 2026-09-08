from __future__ import annotations

import pathlib
import re
import sys

if len(sys.argv) != 2:
    raise SystemExit("usage: patch_webview.py <go-webview2-module-dir>")

root = pathlib.Path(sys.argv[1])
common_path = root / "common.go"
webview_path = root / "webview.go"

if not common_path.exists() or not webview_path.exists():
    raise SystemExit(f"go-webview2 files not found under {root}")

common = common_path.read_text(encoding="utf-8")
if not re.search(r"(?m)^\s*Resize\(\)\s*$", common):
    common, count = re.subn(
        r"(?m)^(\s*Window\(\) unsafe\.Pointer\s*)$",
        r"\1\n\n\t// Resize synchronizes the WebView2 controller bounds with the existing HWND.\n\tResize()",
        common,
        count=1,
    )
    if count != 1:
        raise SystemExit("failed to add Resize() to WebView interface")
common_path.write_text(common, encoding="utf-8")

wv = webview_path.read_text(encoding="utf-8")
pattern = re.compile(r"if !w\.CreateWithOptions\(options\.WindowOptions\) \{\s*return nil\s*\}")
replacement = """if options.Window != nil {
		w.hwnd = uintptr(options.Window)
		if !w.browser.Embed(w.hwnd) {
			return nil
		}
		w.browser.Resize()
	} else if !w.CreateWithOptions(options.WindowOptions) {
		return nil
	}"""
wv, count = pattern.subn(replacement, wv, count=1)
if count != 1 and "if options.Window != nil" not in wv:
    raise SystemExit("failed to patch NewWithOptions existing HWND path")

if "func (w *webview) Resize()" not in wv:
    needle = "func (w *webview) Destroy() {"
    if needle not in wv:
        raise SystemExit("Destroy() marker not found")
    method = """func (w *webview) Resize() {
	w.browser.Resize()
}

"""
    wv = wv.replace(needle, method + needle, 1)
webview_path.write_text(wv, encoding="utf-8")

check_common = common_path.read_text(encoding="utf-8")
check_wv = webview_path.read_text(encoding="utf-8")
for marker in ("Resize()",):
    if marker not in check_common:
        raise SystemExit(f"missing common marker: {marker}")
for marker in ("if options.Window != nil", "func (w *webview) Resize()"):
    if marker not in check_wv:
        raise SystemExit(f"missing webview marker: {marker}")

print(f"patched go-webview2 in {root}")
