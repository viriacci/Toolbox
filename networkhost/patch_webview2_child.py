from pathlib import Path
import os, glob, stat

modcache = Path(os.environ["GOMODCACHE"])
paths = sorted(glob.glob(str(modcache / "github.com" / "jchv" / "go-webview2@*")))
if not paths:
    raise SystemExit("go-webview2 module not found")
root = Path(paths[-1])
p = root / "webview.go"
s = p.read_text(encoding="utf-8")

old = '''\tif !w.CreateWithOptions(options.WindowOptions) {\n\t\treturn nil\n\t}\n'''
new = '''\tif options.Window != nil {\n\t\tif !w.CreateChildWithOptions(options.WindowOptions, uintptr(options.Window)) {\n\t\t\treturn nil\n\t\t}\n\t} else if !w.CreateWithOptions(options.WindowOptions) {\n\t\treturn nil\n\t}\n'''
if old not in s:
    raise SystemExit("NewWithOptions insertion point not found")
s = s.replace(old, new, 1)

needle = 'func (w *webview) CreateWithOptions(opts WindowOptions) bool {'
if needle not in s:
    raise SystemExit("CreateWithOptions insertion point not found")
child = r'''func (w *webview) CreateChildWithOptions(opts WindowOptions, parent uintptr) bool {
	var hinstance windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinstance)

	className, _ := windows.UTF16PtrFromString("webview")
	wc := w32.WndClassExW{
		CbSize:        uint32(unsafe.Sizeof(w32.WndClassExW{})),
		HInstance:     hinstance,
		LpszClassName: className,
		LpfnWndProc:   windows.NewCallback(wndproc),
	}
	_, _, _ = w32.User32RegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	windowName, _ := windows.UTF16PtrFromString(opts.Title)
	windowWidth := opts.Width
	if windowWidth == 0 { windowWidth = 2 }
	windowHeight := opts.Height
	if windowHeight == 0 { windowHeight = 2 }

	const wsChild = 0x40000000
	const wsClipSiblings = 0x04000000
	const wsClipChildren = 0x02000000
	w.hwnd, _, _ = w32.User32CreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		wsChild|wsClipSiblings|wsClipChildren,
		0, 0,
		uintptr(windowWidth), uintptr(windowHeight),
		parent,
		0,
		uintptr(hinstance),
		0,
	)
	if w.hwnd == 0 { return false }
	setWindowContext(w.hwnd, w)
	if !w.browser.Embed(w.hwnd) { return false }
	w.browser.Resize()
	return true
}

'''
s = s.replace(needle, child + needle, 1)

os.chmod(p, stat.S_IWRITE | stat.S_IREAD)
p.write_text(s, encoding="utf-8")
print(f"patched {p}")
