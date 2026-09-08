from pathlib import Path
import sys

mod = Path(sys.argv[1])
common = mod/'common.go'
webview = mod/'webview.go'
chromium = mod/'pkg/edge/chromium.go'
controller = mod/'pkg/edge/ICoreWebView2Controller.go'
navargs = mod/'pkg/edge/ICoreWebView2NavigationCompletedEventArgs.go'

def replace_once(path, old, new, label):
    s=path.read_text(encoding='utf-8')
    if old not in s:
        raise SystemExit(f'missing {label} in {path}')
    s=s.replace(old,new,1)
    path.write_text(s,encoding='utf-8')

replace_once(common,
'''\t// Bind binds a callback function so that it will appear under the given name\n''',
'''\t// Resize synchronizes the WebView2 controller bounds with the native host window.\n\tResize()\n\n\t// SetControllerVisible explicitly controls ICoreWebView2Controller::IsVisible.\n\tSetControllerVisible(visible bool) error\n\n\t// ControllerState returns controller visibility and bounds (left, top, right, bottom).\n\tControllerState() (visible bool, left, top, right, bottom int32, ok bool)\n\n\t// NotifyParentPositionChanged informs WebView2 that the host window moved.\n\tNotifyParentPositionChanged() error\n\n\t// SetNavigationCompletedCallback installs a native NavigationCompleted observer.\n\tSetNavigationCompletedCallback(callback func(success bool, webErrorStatus int32, navigationID uint64))\n\n\t// Native navigation/history helpers.\n\tCanGoBack() bool\n\tCanGoForward() bool\n\tGoBack()\n\tGoForward()\n\tReload()\n\n\t// Bind binds a callback function so that it will appear under the given name\n''','common interface')

replace_once(webview,
'''func (w *webview) Navigate(url string) {\n\tw.browser.Navigate(url)\n}\n''',
'''func (w *webview) Navigate(url string) {\n\tw.browser.Navigate(url)\n}\n\nfunc (w *webview) Resize() {\n\tw.browser.Resize()\n}\n\nfunc (w *webview) SetControllerVisible(visible bool) error {\n\tif c, ok := w.browser.(*edge.Chromium); ok {\n\t\tif visible {\n\t\t\treturn c.Show()\n\t\t}\n\t\treturn c.Hide()\n\t}\n\treturn nil\n}\n\nfunc (w *webview) ControllerState() (visible bool, left, top, right, bottom int32, ok bool) {\n\tc, yes := w.browser.(*edge.Chromium)\n\tif !yes || c.GetController() == nil {\n\t\treturn false, 0, 0, 0, 0, false\n\t}\n\tcontroller := c.GetController()\n\tv, err := controller.GetIsVisible()\n\tif err != nil {\n\t\treturn false, 0, 0, 0, 0, false\n\t}\n\tb, err := controller.GetBounds()\n\tif err != nil || b == nil {\n\t\treturn v, 0, 0, 0, 0, false\n\t}\n\treturn v, b.Left, b.Top, b.Right, b.Bottom, true\n}\n\nfunc (w *webview) NotifyParentPositionChanged() error {\n\treturn w.browser.NotifyParentWindowPositionChanged()\n}\n\nfunc (w *webview) SetNavigationCompletedCallback(callback func(success bool, webErrorStatus int32, navigationID uint64)) {\n\tc, ok := w.browser.(*edge.Chromium)\n\tif !ok {\n\t\treturn\n\t}\n\tif callback == nil {\n\t\tc.NavigationCompletedCallback = nil\n\t\treturn\n\t}\n\tc.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, args *edge.ICoreWebView2NavigationCompletedEventArgs) {\n\t\tif args == nil {\n\t\t\tcallback(false, -1, 0)\n\t\t\treturn\n\t\t}\n\t\tcallback(args.IsSuccess(), args.WebErrorStatus(), args.NavigationID())\n\t}\n}\n\nfunc (w *webview) CanGoBack() bool {\n\tif c, ok := w.browser.(*edge.Chromium); ok { return c.CanGoBack() }\n\treturn false\n}\nfunc (w *webview) CanGoForward() bool {\n\tif c, ok := w.browser.(*edge.Chromium); ok { return c.CanGoForward() }\n\treturn false\n}\nfunc (w *webview) GoBack() { if c, ok := w.browser.(*edge.Chromium); ok { c.GoBack() } }\nfunc (w *webview) GoForward() { if c, ok := w.browser.(*edge.Chromium); ok { c.GoForward() } }\nfunc (w *webview) Reload() { if c, ok := w.browser.(*edge.Chromium); ok { c.Reload() } }\n''','webview forwarding')

replace_once(controller,
'''func (i *ICoreWebView2Controller) PutIsVisible(isVisible bool) error {\n''',
'''func (i *ICoreWebView2Controller) GetIsVisible() (bool, error) {\n\tvar value int32\n\t_, _, err := i.vtbl.GetIsVisible.Call(\n\t\tuintptr(unsafe.Pointer(i)),\n\t\tuintptr(unsafe.Pointer(&value)),\n\t)\n\tif err != windows.ERROR_SUCCESS {\n\t\treturn false, err\n\t}\n\treturn value != 0, nil\n}\n\nfunc (i *ICoreWebView2Controller) PutIsVisible(isVisible bool) error {\n''','controller visible getter')

replace_once(navargs,
'''func (i *ICoreWebView2NavigationCompletedEventArgs) AddRef() uintptr {\n\tr, _, _ := i.vtbl.AddRef.Call()\n\treturn r\n}\n''',
'''func (i *ICoreWebView2NavigationCompletedEventArgs) AddRef() uintptr {\n\tr, _, _ := i.vtbl.AddRef.Call()\n\treturn r\n}\n\nfunc (i *ICoreWebView2NavigationCompletedEventArgs) IsSuccess() bool {\n\tvar value int32\n\t_, _, _ = i.vtbl.GetIsSuccess.Call(uintptr(unsafe.Pointer(i)), uintptr(unsafe.Pointer(&value)))\n\treturn value != 0\n}\n\nfunc (i *ICoreWebView2NavigationCompletedEventArgs) WebErrorStatus() int32 {\n\tvar value int32\n\t_, _, _ = i.vtbl.GetWebErrorStatus.Call(uintptr(unsafe.Pointer(i)), uintptr(unsafe.Pointer(&value)))\n\treturn value\n}\n\nfunc (i *ICoreWebView2NavigationCompletedEventArgs) NavigationID() uint64 {\n\tvar value uint64\n\t_, _, _ = i.vtbl.GetNavigationId.Call(uintptr(unsafe.Pointer(i)), uintptr(unsafe.Pointer(&value)))\n\treturn value\n}\n''','nav args getters')
s=navargs.read_text(encoding='utf-8')
if 'import "unsafe"' not in s:
    s=s.replace('package edge\n','package edge\n\nimport "unsafe"\n',1)
    navargs.write_text(s,encoding='utf-8')

replace_once(chromium,
'''func (e *Chromium) NavigateToString(htmlContent string) {\n''',
'''func (e *Chromium) CanGoBack() bool {\n\tif e.webview == nil { return false }\n\tvar value int32\n\t_, _, _ = e.webview.vtbl.GetCanGoBack.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(&value)))\n\treturn value != 0\n}\n\nfunc (e *Chromium) CanGoForward() bool {\n\tif e.webview == nil { return false }\n\tvar value int32\n\t_, _, _ = e.webview.vtbl.GetCanGoForward.Call(uintptr(unsafe.Pointer(e.webview)), uintptr(unsafe.Pointer(&value)))\n\treturn value != 0\n}\n\nfunc (e *Chromium) GoBack() {\n\tif e.webview != nil { _, _, _ = e.webview.vtbl.GoBack.Call(uintptr(unsafe.Pointer(e.webview))) }\n}\nfunc (e *Chromium) GoForward() {\n\tif e.webview != nil { _, _, _ = e.webview.vtbl.GoForward.Call(uintptr(unsafe.Pointer(e.webview))) }\n}\nfunc (e *Chromium) Reload() {\n\tif e.webview != nil { _, _, _ = e.webview.vtbl.Reload.Call(uintptr(unsafe.Pointer(e.webview))) }\n}\n\nfunc (e *Chromium) NavigateToString(htmlContent string) {\n''','chromium history')

s=webview.read_text(encoding='utf-8')
s=s.replace('chromium.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)',
            'chromium.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateDefault)',1)
webview.write_text(s,encoding='utf-8')

print('patched go-webview2 for V34.8')
