//go:build windows

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

const marker = "INSOFTWARE_NETWORK_HOST_V34"
const hostBuild = "34.8-controller-sync"
const windowMode = "owned-popup-controller-sync"
const webviewPinnedCommit = "56598839c808a2340edee99204db479f410e9bf4"

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	procEnumWindows        = user32.NewProc("EnumWindows")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procIsWindow           = user32.NewProc("IsWindow")
	procIsIconic           = user32.NewProc("IsIconic")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procShowWindow         = user32.NewProc("ShowWindow")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	procClientToScreen     = user32.NewProc("ClientToScreen")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procGetClientRect      = user32.NewProc("GetClientRect")
)

const (
	gwlStyle       = -16
	gwlExStyle     = -20
	gwlpHwndParent = -8

	wsPopup        = 0x80000000
	wsClipSiblings = 0x04000000
	wsClipChildren = 0x02000000

	wsExToolWindow = 0x00000080
	wsExAppWindow  = 0x00040000

	swHide           = 0
	swShowNoActivate = 4

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
	swpShowWindow   = 0x0040
)

type point struct{ X, Y int32 }
type nativeRect struct{ Left, Top, Right, Bottom int32 }

type pageReport struct {
	Href        string  `json:"href"`
	Title       string  `json:"title"`
	ReadyState  string  `json:"readyState"`
	InnerWidth  int     `json:"innerWidth"`
	InnerHeight int     `json:"innerHeight"`
	DPR         float64 `json:"dpr"`
}

type hostState struct {
	Marker                string `json:"marker"`
	HostBuild             string `json:"hostBuild"`
	WindowMode            string `json:"windowMode"`
	WebViewPinnedCommit   string `json:"webviewPinnedCommit"`
	Ready                 bool   `json:"ready"`
	Visible               bool   `json:"visible"`
	NativeVisible         bool   `json:"nativeVisible"`
	ControllerVisible     bool   `json:"controllerVisible"`
	ControllerBounds      string `json:"controllerBounds"`
	ControllerBoundsOK    bool   `json:"controllerBoundsOk"`
	Source                string `json:"source"`
	RequestedURL          string `json:"requestedUrl"`
	Home                  string `json:"home"`
	Loading               bool   `json:"loading"`
	CanGoBack             bool   `json:"canGoBack"`
	CanGoForward          bool   `json:"canGoForward"`
	Error                 string `json:"error"`
	HostPID               int    `json:"hostPid"`
	ParentPID             int    `json:"parentPid"`
	ParentHWND            string `json:"parentHwnd"`
	WindowHWND            string `json:"windowHwnd"`
	WindowRect            string `json:"windowRect"`
	ClientRect            string `json:"clientRect"`
	RequestedRect         string `json:"requestedRect"`
	Viewport              string `json:"viewport"`
	PageReadyState        string `json:"pageReadyState"`
	PageTitle             string `json:"pageTitle"`
	Port                  int    `json:"port"`
	LastAction            string `json:"lastAction"`
	NavigationCount       int    `json:"navigationCount"`
	LastNavigationSuccess bool   `json:"lastNavigationSuccess"`
	LastWebErrorStatus    int32  `json:"lastWebErrorStatus"`
	LastNavigationID      uint64 `json:"lastNavigationId"`
	NavigationCompletedAt string `json:"navigationCompletedAt"`
	SessionClearPending   bool   `json:"sessionClearPending"`
	LastResizeAt          string `json:"lastResizeAt"`
}

type controller struct {
	mu     sync.RWMutex
	state  hostState
	w      webview2.WebView
	hwnd   uintptr
	parent uintptr
	log    *log.Logger

	localX int
	localY int
	width  int
	height int

	lastScreenX int32
	lastScreenY int32
	lastWidth   int
	lastHeight  int
}

func (c *controller) snapshot() hostState { c.mu.RLock(); defer c.mu.RUnlock(); return c.state }
func (c *controller) mutate(fn func(*hostState)) { c.mu.Lock(); fn(&c.state); c.mu.Unlock() }
func (c *controller) setError(err error) {
	if err == nil { c.mutate(func(s *hostState) { s.Error = "" }); return }
	c.log.Printf("ERROR: %v", err)
	c.mutate(func(s *hostState) { s.Error = err.Error() })
}
func (c *controller) runUI(timeout time.Duration, fn func() error) error {
	c.mu.RLock(); w := c.w; c.mu.RUnlock()
	if w == nil { return fmt.Errorf("WebView2 host nie jest jeszcze gotowy") }
	done := make(chan error, 1)
	w.Dispatch(func() { done <- fn() })
	select {
	case err := <-done: return err
	case <-time.After(timeout): return fmt.Errorf("timeout operacji UI po %s", timeout)
	}
}

func intQuery(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key)); if v == "" { return def }
	n, err := strconv.Atoi(v); if err != nil { return def }; return n
}
func jsonWrite(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value)
}
func allowedOrigin(origin string) bool {
	if strings.TrimSpace(origin) == "" { return true }
	u, err := url.Parse(origin); if err != nil || u.Scheme != "http" { return false }
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
func cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if !allowedOrigin(origin) { jsonWrite(w, http.StatusForbidden, map[string]any{"ok": false, "error": "niedozwolone Origin"}); return }
		if origin != "" { w.Header().Set("Access-Control-Allow-Origin", origin); w.Header().Set("Vary", "Origin") }
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions { w.WriteHeader(http.StatusNoContent); return }
		next(w, r)
	}
}
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost { return true }
	jsonWrite(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "wymagany POST"}); return false
}
func indexArg(i int32) uintptr { return uintptr(int64(i)) }

func windowTitle(hwnd uintptr) string {
	buf := make([]uint16, 512); n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 { return "" }; return syscall.UTF16ToString(buf[:n])
}
func findWindowByPID(pid uint32) uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		var wp uint32; _, _, _ = procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&wp)))
		if wp != pid { return 1 }
		v, _, _ := procIsWindowVisible.Call(hwnd); if v == 0 { return 1 }
		if strings.Contains(strings.ToLower(windowTitle(hwnd)), "insoftware su toolbox") { found = hwnd; return 0 }
		if found == 0 { found = hwnd }; return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0); return found
}
func waitForWindow(pid uint32, timeout time.Duration) uintptr {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) { if h := findWindowByPID(pid); h != 0 { return h }; time.Sleep(120 * time.Millisecond) }
	return 0
}
func openExternal(raw string) error {
	raw = strings.TrimSpace(raw); if raw == "" { return fmt.Errorf("brak adresu") }
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw).Start()
}

func (c *controller) styleOverlay() {
	newStyle := uintptr(wsPopup | wsClipSiblings | wsClipChildren)
	_, _, _ = procSetWindowLongPtrW.Call(c.hwnd, indexArg(gwlStyle), newStyle)
	ex, _, _ := procGetWindowLongPtrW.Call(c.hwnd, indexArg(gwlExStyle)); ex = (ex &^ wsExAppWindow) | wsExToolWindow
	_, _, _ = procSetWindowLongPtrW.Call(c.hwnd, indexArg(gwlExStyle), ex)
	_, _, _ = procSetWindowLongPtrW.Call(c.hwnd, indexArg(gwlpHwndParent), c.parent)
	_, _, _ = procSetWindowPos.Call(c.hwnd, 0, 0, 0, 2, 2, swpNoZOrder|swpNoActivate|swpFrameChanged)
	_, _, _ = procShowWindow.Call(c.hwnd, swHide)
}

func rectString(rc nativeRect) string { return fmt.Sprintf("x=%d y=%d w=%d h=%d", rc.Left, rc.Top, rc.Right-rc.Left, rc.Bottom-rc.Top) }
func (c *controller) refreshNativeDiagnostics() {
	if c.hwnd == 0 { return }
	vis, _, _ := procIsWindowVisible.Call(c.hwnd); var wr, cr nativeRect
	wok, _, _ := procGetWindowRect.Call(c.hwnd, uintptr(unsafe.Pointer(&wr))); cok, _, _ := procGetClientRect.Call(c.hwnd, uintptr(unsafe.Pointer(&cr)))
	windowRect, clientRect := "", ""; if wok != 0 { windowRect = rectString(wr) }; if cok != 0 { clientRect = rectString(cr) }
	c.mutate(func(s *hostState) { s.NativeVisible = vis != 0; s.WindowRect = windowRect; s.ClientRect = clientRect })
}
func (c *controller) refreshControllerDiagnostics() {
	c.mu.RLock(); w := c.w; c.mu.RUnlock(); if w == nil { return }
	visible, left, top, right, bottom, ok := w.ControllerState(); bounds := ""
	if ok { bounds = fmt.Sprintf("x=%d y=%d w=%d h=%d", left, top, right-left, bottom-top) }
	c.mutate(func(s *hostState) { s.ControllerVisible = visible; s.ControllerBounds = bounds; s.ControllerBoundsOK = ok })
}
func (c *controller) updateHistory() {
	c.mu.RLock(); w := c.w; c.mu.RUnlock(); if w == nil { return }
	back, forward := w.CanGoBack(), w.CanGoForward(); c.mutate(func(s *hostState) { s.CanGoBack = back; s.CanGoForward = forward })
}

func (c *controller) placeOverlay(show bool, forceResize bool) error {
	c.mu.RLock(); x, y, width, height := c.localX, c.localY, c.width, c.height; wv := c.w; c.mu.RUnlock()
	if width < 80 || height < 80 { return fmt.Errorf("odrzucono nieprawidłową geometrię Sieci: x=%d y=%d w=%d h=%d", x, y, width, height) }
	if wv == nil { return fmt.Errorf("WebView2 nie jest gotowe") }
	p := point{X: int32(x), Y: int32(y)}; ok, _, callErr := procClientToScreen.Call(c.parent, uintptr(unsafe.Pointer(&p)))
	if ok == 0 { return fmt.Errorf("ClientToScreen: %v", callErr) }
	c.mu.RLock(); geometryChanged := p.X != c.lastScreenX || p.Y != c.lastScreenY || width != c.lastWidth || height != c.lastHeight; c.mu.RUnlock()
	flags := uintptr(swpNoActivate | swpNoZOrder); if show { flags |= swpShowWindow }
	ok, _, callErr = procSetWindowPos.Call(c.hwnd, 0, uintptr(int64(p.X)), uintptr(int64(p.Y)), uintptr(width), uintptr(height), flags)
	if ok == 0 { return fmt.Errorf("SetWindowPos overlay: %v", callErr) }
	if geometryChanged || forceResize { wv.Resize(); c.mutate(func(s *hostState) { s.LastResizeAt = time.Now().Format(time.RFC3339Nano) }) }
	_ = wv.NotifyParentPositionChanged()
	if err := wv.SetControllerVisible(show); err != nil { return fmt.Errorf("Controller.IsVisible: %w", err) }
	if show { _, _, _ = procShowWindow.Call(c.hwnd, swShowNoActivate) } else { _, _, _ = procShowWindow.Call(c.hwnd, swHide) }
	c.mu.Lock(); c.lastScreenX, c.lastScreenY, c.lastWidth, c.lastHeight = p.X, p.Y, width, height; c.mu.Unlock()
	c.refreshNativeDiagnostics(); c.refreshControllerDiagnostics(); return nil
}

func (c *controller) setBoundsFromRequest(r *http.Request) error {
	x, y := intQuery(r, "x", 0), intQuery(r, "y", 0); width, height := intQuery(r, "w", 0), intQuery(r, "h", 0)
	if width < 80 || height < 80 { return fmt.Errorf("nieprawidłowy prostokąt Sieci: x=%d y=%d w=%d h=%d", x, y, width, height) }
	c.mu.Lock(); c.localX, c.localY, c.width, c.height = x, y, width, height; c.mu.Unlock()
	c.mutate(func(s *hostState) { s.RequestedRect = fmt.Sprintf("x=%d y=%d w=%d h=%d", x, y, width, height) }); return nil
}

func (c *controller) handlers(profilePath, clearFlag string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", cors(func(w http.ResponseWriter, r *http.Request) { c.refreshNativeDiagnostics(); c.refreshControllerDiagnostics(); c.updateHistory(); jsonWrite(w, 200, c.snapshot()) }))
	mux.HandleFunc("/state", cors(func(w http.ResponseWriter, r *http.Request) { c.refreshNativeDiagnostics(); c.refreshControllerDiagnostics(); c.updateHistory(); jsonWrite(w, 200, c.snapshot()) }))
	mux.HandleFunc("/runtime", cors(func(w http.ResponseWriter, r *http.Request) { s := c.snapshot(); jsonWrite(w, 200, map[string]any{"ok": true, "hostBuild": hostBuild, "windowMode": windowMode, "webviewPinnedCommit": webviewPinnedCommit, "profilePath": profilePath, "sessionClearPending": s.SessionClearPending}) }))
	mux.HandleFunc("/bounds", cors(func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) { return }; if err := c.setBoundsFromRequest(r); err != nil { jsonWrite(w, 422, map[string]any{"ok": false, "error": err.Error()}); return }
		desired := c.snapshot().Visible; err := c.runUI(4*time.Second, func() error { return c.placeOverlay(desired, true) })
		if err != nil { c.setError(err); jsonWrite(w, 503, map[string]any{"ok": false, "error": err.Error()}); return }
		c.mutate(func(s *hostState) { s.LastAction = "bounds"; s.Error = "" }); jsonWrite(w, 200, map[string]any{"ok": true, "state": c.snapshot()})
	}))
	mux.HandleFunc("/show", cors(func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) { return }; q := r.URL.Query(); target := strings.TrimSpace(q.Get("url")); home := strings.TrimSpace(q.Get("home"))
		if target == "" { jsonWrite(w, 400, map[string]any{"ok": false, "error": "brak url"}); return }
		if err := c.setBoundsFromRequest(r); err != nil { jsonWrite(w, 422, map[string]any{"ok": false, "error": err.Error()}); return }
		before := c.snapshot(); navigate := before.Source == "" || before.RequestedURL != target
		err := c.runUI(4*time.Second, func() error {
			c.mutate(func(s *hostState) { s.Visible = true; s.LastAction = "show"; s.Error = ""; s.RequestedURL = target; if home != "" { s.Home = home } else if s.Home == "" { s.Home = target }; if navigate { s.Loading = true; s.NavigationCount++ } })
			if err := c.placeOverlay(true, true); err != nil { return err }; if navigate { c.w.Navigate(target) } else { c.updateHistory() }; return nil
		})
		if err != nil { c.setError(err); jsonWrite(w, 503, map[string]any{"ok": false, "error": err.Error()}); return }
		c.log.Printf("show overlay x=%d y=%d w=%d h=%d navigate=%t url=%s", c.localX, c.localY, c.width, c.height, navigate, target)
		jsonWrite(w, 200, map[string]any{"ok": true, "navigateCalled": navigate, "state": c.snapshot()})
	}))
	mux.HandleFunc("/hide", cors(func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) { return }
		err := c.runUI(3*time.Second, func() error { if err := c.w.SetControllerVisible(false); err != nil { return err }; _, _, _ = procShowWindow.Call(c.hwnd, swHide); c.mutate(func(s *hostState) { s.Visible = false; s.LastAction = "hide" }); c.refreshNativeDiagnostics(); c.refreshControllerDiagnostics(); return nil })
		if err != nil { c.setError(err); jsonWrite(w, 503, map[string]any{"ok": false, "error": err.Error()}); return }; jsonWrite(w, 200, map[string]any{"ok": true, "state": c.snapshot()})
	}))
	mux.HandleFunc("/action", cors(func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) { return }
		q := r.URL.Query(); action := strings.ToLower(strings.TrimSpace(q.Get("action"))); home := strings.TrimSpace(q.Get("home")); target := strings.TrimSpace(q.Get("url"))
		if action == "external" { if target == "" { target = home }; if target == "" { target = c.snapshot().Source }; if err := openExternal(target); err != nil { c.setError(err); jsonWrite(w, 500, map[string]any{"ok": false, "error": err.Error()}); return }; c.mutate(func(s *hostState) { s.LastAction = "external" }); jsonWrite(w, 200, map[string]any{"ok": true}); return }
		err := c.runUI(4*time.Second, func() error {
			c.mutate(func(s *hostState) { s.LastAction = action; s.Error = "" })
			switch action {
			case "home": if home == "" { home = c.snapshot().Home }; if home == "" { return fmt.Errorf("brak strony startowej") }; c.mutate(func(s *hostState) { s.RequestedURL = home; s.Home = home; s.Loading = true; s.NavigationCount++ }); c.w.Navigate(home)
			case "reload": c.mutate(func(s *hostState) { s.Loading = true }); c.w.Reload()
			case "back": if !c.w.CanGoBack() { return nil }; c.mutate(func(s *hostState) { s.Loading = true }); c.w.GoBack()
			case "forward": if !c.w.CanGoForward() { return nil }; c.mutate(func(s *hostState) { s.Loading = true }); c.w.GoForward()
			case "navigate": if target == "" { return fmt.Errorf("brak adresu") }; c.mutate(func(s *hostState) { s.RequestedURL = target; s.Loading = true; s.NavigationCount++ }); c.w.Navigate(target)
			default: return fmt.Errorf("nieznana akcja: %s", action)
			}; return nil
		})
		if err != nil { c.setError(err); jsonWrite(w, 400, map[string]any{"ok": false, "error": err.Error()}); return }; jsonWrite(w, 200, map[string]any{"ok": true, "state": c.snapshot()})
	}))
	mux.HandleFunc("/clear-session", cors(func(w http.ResponseWriter, r *http.Request) {
		if !requirePost(w, r) { return }
		if err := os.WriteFile(clearFlag, []byte(time.Now().Format(time.RFC3339)), 0644); err != nil { jsonWrite(w, 500, map[string]any{"ok": false, "error": err.Error()}); return }
		c.mutate(func(s *hostState) { s.SessionClearPending = true; s.LastAction = "clear-session" }); jsonWrite(w, 200, map[string]any{"ok": true, "restartRequired": true})
	}))
	return mux
}

func main() {
	runtime.LockOSThread()
	parentPID := flag.Int("parent-pid", 0, "PID głównego Toolboxa"); port := flag.Int("port", 53434, "port lokalnego API"); flag.Parse()
	local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); if local == "" { local = os.TempDir() }
	base := filepath.Join(local, "INSOFTWARE SU Toolbox"); logs := filepath.Join(base, "Logs"); profilePath := filepath.Join(base, "NetworkWebView2Profile"); clearFlag := filepath.Join(base, "NetworkWebView2Profile.clear")
	_ = os.MkdirAll(logs, 0755)
	if _, err := os.Stat(clearFlag); err == nil { _ = os.RemoveAll(profilePath); _ = os.Remove(clearFlag) }
	_ = os.MkdirAll(profilePath, 0755)
	logFile, _ := os.OpenFile(filepath.Join(logs, "networkhost-v34.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); logger := log.New(logFile, "", log.LstdFlags|log.Lmicroseconds)
	logger.Printf("=== START %s build=%s pid=%d parentPID=%d port=%d ===", marker, hostBuild, os.Getpid(), *parentPID, *port)
	c := &controller{log: logger}; c.state = hostState{Marker: marker, HostBuild: hostBuild, WindowMode: windowMode, WebViewPinnedCommit: webviewPinnedCommit, HostPID: os.Getpid(), ParentPID: *parentPID, Port: *port}
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: c.handlers(profilePath, clearFlag), ReadHeaderTimeout: 3 * time.Second}
	go func() { logger.Printf("HTTP listening on %s", srv.Addr); if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed { logger.Printf("HTTP ERROR: %v", err) } }()
	if *parentPID <= 0 { c.setError(fmt.Errorf("brak poprawnego --parent-pid")); time.Sleep(2 * time.Second); return }
	parent := waitForWindow(uint32(*parentPID), 20*time.Second); if parent == 0 { c.setError(fmt.Errorf("nie znaleziono okna procesu PID %d", *parentPID)); time.Sleep(2 * time.Second); return }
	c.parent = parent; c.mutate(func(s *hostState) { s.ParentHWND = fmt.Sprintf("0x%X", parent) }); logger.Printf("parent hwnd=0x%X title=%q", parent, windowTitle(parent))
	w := webview2.NewWithOptions(webview2.WebViewOptions{Debug: false, AutoFocus: false, DataPath: profilePath, WindowOptions: webview2.WindowOptions{Title: "INSOFTWARE Network Overlay", Width: 2, Height: 2}})
	if w == nil { c.setError(fmt.Errorf("go-webview2 zwrócił nil")); time.Sleep(2 * time.Second); return }
	defer w.Destroy(); hwnd := uintptr(w.Window()); c.hwnd = hwnd; c.mu.Lock(); c.w = w; c.state.WindowHWND = fmt.Sprintf("0x%X", hwnd); c.mu.Unlock(); c.styleOverlay(); w.Resize(); _ = w.SetControllerVisible(false); c.refreshNativeDiagnostics(); c.refreshControllerDiagnostics(); logger.Printf("overlay hwnd=0x%X owner=0x%X", hwnd, parent)
	if err := w.Bind("__toolboxReportPageState", func(raw string) {
		var p pageReport; if err := json.Unmarshal([]byte(raw), &p); err != nil { logger.Printf("page report parse: %v", err); return }
		href := strings.TrimSpace(p.Href); c.mutate(func(s *hostState) { if href != "" && href != "about:blank" { s.Source = href }; s.Viewport = fmt.Sprintf("%dx%d @%.2f", p.InnerWidth, p.InnerHeight, p.DPR); s.PageReadyState = p.ReadyState; s.PageTitle = p.Title })
	}); err != nil { c.setError(fmt.Errorf("Bind page state: %w", err)); return }
	w.Init(`(()=>{const report=()=>{try{window.__toolboxReportPageState(JSON.stringify({href:String(location.href),title:String(document.title||""),readyState:String(document.readyState||""),innerWidth:Number(window.innerWidth||0),innerHeight:Number(window.innerHeight||0),dpr:Number(window.devicePixelRatio||1)})).catch(()=>{})}catch{}};window.__toolboxReportPageStateNow=report;addEventListener('DOMContentLoaded',report);addEventListener('load',report);addEventListener('pageshow',report);addEventListener('popstate',report);addEventListener('hashchange',report);addEventListener('resize',report);setInterval(report,1200);report()})()`)
	w.SetNavigationCompletedCallback(func(success bool, webErrorStatus int32, navigationID uint64) {
		c.mutate(func(s *hostState) { s.Loading = false; s.LastNavigationSuccess = success; s.LastWebErrorStatus = webErrorStatus; s.LastNavigationID = navigationID; s.NavigationCompletedAt = time.Now().Format(time.RFC3339Nano); if !success { s.Error = fmt.Sprintf("NavigationCompleted: WebErrorStatus=%d", webErrorStatus) } else if strings.HasPrefix(s.Error, "NavigationCompleted:") { s.Error = "" } })
		c.updateHistory(); c.refreshControllerDiagnostics(); w.Eval(`window.__toolboxReportPageStateNow&&window.__toolboxReportPageStateNow()`); logger.Printf("navigation completed id=%d success=%t status=%d", navigationID, success, webErrorStatus)
	})
	c.mutate(func(s *hostState) { s.Ready = true; s.LastAction = "ready"; s.Error = "" }); logger.Printf("WebView ready overlay hwnd=0x%X", hwnd)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond); defer ticker.Stop()
		for range ticker.C {
			ok, _, _ := procIsWindow.Call(parent); if ok == 0 { logger.Printf("parent disappeared; terminating"); w.Terminate(); return }
			if !c.snapshot().Visible { continue }
			iconic, _, _ := procIsIconic.Call(parent)
			if iconic != 0 { _ = c.runUI(2*time.Second, func() error { _ = c.w.SetControllerVisible(false); _, _, _ = procShowWindow.Call(c.hwnd, swHide); c.refreshNativeDiagnostics(); c.refreshControllerDiagnostics(); return nil }); continue }
			_ = c.runUI(2*time.Second, func() error { return c.placeOverlay(true, false) })
		}
	}()
	w.Run(); logger.Printf("=== STOP %s build=%s ===", marker, hostBuild)
}
