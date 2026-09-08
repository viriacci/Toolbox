//go:build windows

package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "log"
    "net/http"
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
const hostBuild = "34.2-child-from-create"

var (
    user32                 = syscall.NewLazyDLL("user32.dll")
    procEnumWindows        = user32.NewProc("EnumWindows")
    procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
    procIsWindowVisible    = user32.NewProc("IsWindowVisible")
    procIsWindow           = user32.NewProc("IsWindow")
    procSetWindowPos       = user32.NewProc("SetWindowPos")
    procShowWindow         = user32.NewProc("ShowWindow")
    procSetFocus           = user32.NewProc("SetFocus")
    procGetWindowTextW     = user32.NewProc("GetWindowTextW")
    procRedrawWindow       = user32.NewProc("RedrawWindow")
    procUpdateWindow       = user32.NewProc("UpdateWindow")
)

const (
    swHide = 0
    swShow = 5
    swpNoZOrder = 0x0004
    swpNoActivate = 0x0010
    rdwInvalidate = 0x0001
    rdwAllChildren = 0x0080
    rdwUpdateNow = 0x0100
)

type hostState struct {
    Marker          string `json:"marker"`
    HostBuild       string `json:"hostBuild"`
    WindowMode      string `json:"windowMode"`
    Ready           bool   `json:"ready"`
    Visible         bool   `json:"visible"`
    Source          string `json:"source"`
    RequestedURL    string `json:"requestedUrl"`
    Home            string `json:"home"`
    Loading         bool   `json:"loading"`
    CanGoBack       bool   `json:"canGoBack"`
    CanGoForward    bool   `json:"canGoForward"`
    Error           string `json:"error"`
    ParentPID       int    `json:"parentPid"`
    ParentHWND      string `json:"parentHwnd"`
    WindowHWND      string `json:"windowHwnd"`
    Port            int    `json:"port"`
    LastAction      string `json:"lastAction"`
    NavigationCount int    `json:"navigationCount"`
}

type controller struct {
    mu sync.RWMutex
    state hostState
    w webview2.WebView
    hwnd uintptr
    parent uintptr
    log *log.Logger
}

func (c *controller) snapshot() hostState { c.mu.RLock(); defer c.mu.RUnlock(); return c.state }
func (c *controller) mutate(fn func(*hostState)) { c.mu.Lock(); fn(&c.state); c.mu.Unlock() }
func (c *controller) setError(err error) {
    if err == nil { c.mutate(func(s *hostState){ s.Error="" }); return }
    c.log.Printf("ERROR: %v", err)
    c.mutate(func(s *hostState){ s.Error=err.Error() })
}
func (c *controller) runUI(timeout time.Duration, fn func() error) error {
    c.mu.RLock(); w := c.w; c.mu.RUnlock()
    if w == nil { return fmt.Errorf("WebView2 host nie jest jeszcze gotowy") }
    done := make(chan error,1)
    w.Dispatch(func(){ done <- fn() })
    select { case err:=<-done: return err; case <-time.After(timeout): return fmt.Errorf("timeout operacji UI po %s",timeout) }
}

func intQuery(r *http.Request,key string,def int) int { v:=strings.TrimSpace(r.URL.Query().Get(key)); if v=="" {return def}; n,err:=strconv.Atoi(v); if err!=nil{return def}; return n }
func jsonWrite(w http.ResponseWriter,status int,value any){ w.Header().Set("Content-Type","application/json; charset=utf-8"); w.WriteHeader(status); _=json.NewEncoder(w).Encode(value) }
func cors(next http.HandlerFunc) http.HandlerFunc { return func(w http.ResponseWriter,r *http.Request){ w.Header().Set("Access-Control-Allow-Origin","*"); w.Header().Set("Access-Control-Allow-Methods","GET, POST, OPTIONS"); w.Header().Set("Access-Control-Allow-Headers","Content-Type"); w.Header().Set("Cache-Control","no-store"); if r.Method==http.MethodOptions {w.WriteHeader(http.StatusNoContent); return}; next(w,r) } }

func windowTitle(hwnd uintptr) string { buf:=make([]uint16,512); n,_,_:=procGetWindowTextW.Call(hwnd,uintptr(unsafe.Pointer(&buf[0])),uintptr(len(buf))); if n==0{return ""}; return syscall.UTF16ToString(buf[:n]) }
func findWindowByPID(pid uint32) uintptr { var found uintptr; cb:=syscall.NewCallback(func(hwnd,lparam uintptr) uintptr { var wp uint32; _,_,_=procGetWindowThreadPID.Call(hwnd,uintptr(unsafe.Pointer(&wp))); if wp!=pid{return 1}; visible,_,_:=procIsWindowVisible.Call(hwnd); if visible==0{return 1}; found=hwnd; return 0 }); _,_,_=procEnumWindows.Call(cb,0); return found }
func waitForWindow(pid uint32,timeout time.Duration) uintptr { deadline:=time.Now().Add(timeout); for time.Now().Before(deadline){ if hwnd:=findWindowByPID(pid); hwnd!=0{return hwnd}; time.Sleep(120*time.Millisecond) }; return 0 }

func moveChild(hwnd uintptr,x,y,width,height int,show bool){
    if width<2{width=2}; if height<2{height=2}
    _,_,_=procSetWindowPos.Call(hwnd,0,uintptr(x),uintptr(y),uintptr(width),uintptr(height),swpNoZOrder|swpNoActivate)
    _,_,_=procRedrawWindow.Call(hwnd,0,0,rdwInvalidate|rdwAllChildren|rdwUpdateNow)
    _,_,_=procUpdateWindow.Call(hwnd)
    if show { _,_,_=procShowWindow.Call(hwnd,swShow); _,_,_=procSetFocus.Call(hwnd) }
}
func openExternal(url string) error { url=strings.TrimSpace(url); if url==""{return fmt.Errorf("brak adresu")}; return exec.Command("rundll32.exe","url.dll,FileProtocolHandler",url).Start() }

func (c *controller) handlers() http.Handler {
    mux:=http.NewServeMux()
    mux.HandleFunc("/health",cors(func(w http.ResponseWriter,r *http.Request){jsonWrite(w,200,c.snapshot())}))
    mux.HandleFunc("/state",cors(func(w http.ResponseWriter,r *http.Request){jsonWrite(w,200,c.snapshot())}))
    mux.HandleFunc("/show",cors(func(w http.ResponseWriter,r *http.Request){
        if r.Method!=http.MethodPost {jsonWrite(w,405,map[string]any{"ok":false}); return}
        q:=r.URL.Query(); url:=strings.TrimSpace(q.Get("url")); home:=strings.TrimSpace(q.Get("home")); x,y:=intQuery(r,"x",0),intQuery(r,"y",0); width,height:=intQuery(r,"w",2),intQuery(r,"h",2)
        if url=="" {jsonWrite(w,400,map[string]any{"ok":false,"error":"brak url"}); return}
        err:=c.runUI(4*time.Second,func() error{
            moveChild(c.hwnd,x,y,width,height,true)
            c.mutate(func(s *hostState){s.Visible=true;s.RequestedURL=url;if home!=""{s.Home=home}else if s.Home==""{s.Home=url};s.Loading=true;s.LastAction="show";s.Error="";s.NavigationCount++})
            c.w.Navigate(url)
            return nil
        })
        if err!=nil {c.setError(err);jsonWrite(w,503,map[string]any{"ok":false,"error":err.Error()});return}
        c.log.Printf("show child x=%d y=%d w=%d h=%d url=%s",x,y,width,height,url)
        jsonWrite(w,200,map[string]any{"ok":true,"navigateCalled":true,"state":c.snapshot()})
    }))
    mux.HandleFunc("/hide",cors(func(w http.ResponseWriter,r *http.Request){
        err:=c.runUI(3*time.Second,func() error{_,_,_=procShowWindow.Call(c.hwnd,swHide);c.mutate(func(s *hostState){s.Visible=false;s.LastAction="hide"});return nil})
        if err!=nil{c.setError(err);jsonWrite(w,503,map[string]any{"ok":false,"error":err.Error()});return};jsonWrite(w,200,map[string]any{"ok":true})
    }))
    mux.HandleFunc("/action",cors(func(w http.ResponseWriter,r *http.Request){
        q:=r.URL.Query(); action:=strings.ToLower(strings.TrimSpace(q.Get("action"))); home:=strings.TrimSpace(q.Get("home")); target:=strings.TrimSpace(q.Get("url"))
        if action=="external" {if target==""{target=home};if target==""{target=c.snapshot().Source};if err:=openExternal(target);err!=nil{c.setError(err);jsonWrite(w,500,map[string]any{"ok":false,"error":err.Error()});return};c.mutate(func(s *hostState){s.LastAction="external"});jsonWrite(w,200,map[string]any{"ok":true});return}
        err:=c.runUI(4*time.Second,func() error{
            c.mutate(func(s *hostState){s.LastAction=action;s.Error=""})
            switch action {
            case "home": if home==""{home=c.snapshot().Home};if home==""{return fmt.Errorf("brak strony startowej")};c.mutate(func(s *hostState){s.RequestedURL=home;s.Home=home;s.Loading=true;s.NavigationCount++});c.w.Navigate(home)
            case "reload": c.mutate(func(s *hostState){s.Loading=true});c.w.Eval("location.reload()")
            case "back": c.mutate(func(s *hostState){s.Loading=true});c.w.Eval("history.back()")
            case "forward": c.mutate(func(s *hostState){s.Loading=true});c.w.Eval("history.forward()")
            case "navigate": if target==""{return fmt.Errorf("brak adresu")};c.mutate(func(s *hostState){s.RequestedURL=target;s.Loading=true;s.NavigationCount++});c.w.Navigate(target)
            default:return fmt.Errorf("nieznana akcja: %s",action)
            }
            return nil
        })
        if err!=nil{c.setError(err);jsonWrite(w,400,map[string]any{"ok":false,"error":err.Error()});return};jsonWrite(w,200,map[string]any{"ok":true,"state":c.snapshot()})
    }))
    return mux
}

func main(){
    runtime.LockOSThread()
    parentPID:=flag.Int("parent-pid",0,"PID głównego Toolboxa");port:=flag.Int("port",53434,"port lokalnego API");flag.Parse()
    local:=strings.TrimSpace(os.Getenv("LOCALAPPDATA"));if local==""{local=os.TempDir()};base:=filepath.Join(local,"INSOFTWARE SU Toolbox");_=os.MkdirAll(filepath.Join(base,"Logs"),0755);_=os.MkdirAll(filepath.Join(base,"NetworkWebView2Profile"),0755)
    logFile,_:=os.OpenFile(filepath.Join(base,"Logs","networkhost-v34.log"),os.O_CREATE|os.O_WRONLY|os.O_APPEND,0644);logger:=log.New(logFile,"",log.LstdFlags|log.Lmicroseconds);logger.Printf("=== START %s build=%s parentPID=%d port=%d ===",marker,hostBuild,*parentPID,*port)
    c:=&controller{log:logger};c.state=hostState{Marker:marker,HostBuild:hostBuild,WindowMode:"native-child-before-webview2",ParentPID:*parentPID,Port:*port}
    srv:=&http.Server{Addr:fmt.Sprintf("127.0.0.1:%d",*port),Handler:c.handlers(),ReadHeaderTimeout:3*time.Second};go func(){logger.Printf("HTTP listening on %s",srv.Addr);if err:=srv.ListenAndServe();err!=nil&&err!=http.ErrServerClosed{logger.Printf("HTTP ERROR: %v",err)}}()
    if *parentPID<=0{c.setError(fmt.Errorf("brak poprawnego --parent-pid"));time.Sleep(2*time.Second);return}
    parent:=waitForWindow(uint32(*parentPID),20*time.Second);if parent==0{c.setError(fmt.Errorf("nie znaleziono okna procesu PID %d",*parentPID));time.Sleep(2*time.Second);return};logger.Printf("parent hwnd=0x%X title=%q",parent,windowTitle(parent));c.parent=parent;c.mutate(func(s *hostState){s.ParentHWND=fmt.Sprintf("0x%X",parent)})
    dataPath:=filepath.Join(base,"NetworkWebView2Profile")
    w:=webview2.NewWithOptions(webview2.WebViewOptions{Window:unsafe.Pointer(parent),Debug:false,AutoFocus:true,DataPath:dataPath,WindowOptions:webview2.WindowOptions{Title:"INSOFTWARE Network Host",Width:2,Height:2}})
    if w==nil{c.setError(fmt.Errorf("go-webview2 zwrócił nil przy tworzeniu natywnego child WebView2"));time.Sleep(2*time.Second);return};defer w.Destroy()
    hwnd:=uintptr(w.Window());c.hwnd=hwnd;c.mu.Lock();c.w=w;c.state.WindowHWND=fmt.Sprintf("0x%X",hwnd);c.mu.Unlock();_,_,_=procShowWindow.Call(hwnd,swHide)
    if err:=w.Bind("__toolboxReportLocation",func(url string){url=strings.TrimSpace(url);if url==""||url=="about:blank"{return};c.mutate(func(s *hostState){s.Source=url;s.Loading=false;s.CanGoBack=s.NavigationCount>1;s.CanGoForward=true;s.Error=""});logger.Printf("source=%s",url)});err!=nil{c.setError(fmt.Errorf("Bind source callback: %w",err));return}
    w.Init(`(()=>{const report=()=>{try{window.__toolboxReportLocation(String(location.href)).catch(()=>{})}catch{}};addEventListener('DOMContentLoaded',report);addEventListener('load',report);addEventListener('pageshow',report);addEventListener('popstate',report);addEventListener('hashchange',report);setInterval(report,700);report()})()`)
    c.mutate(func(s *hostState){s.Ready=true;s.LastAction="ready";s.Error=""});logger.Printf("WebView ready child hwnd=0x%X parent=0x%X",hwnd,parent)
    go func(){ticker:=time.NewTicker(time.Second);defer ticker.Stop();for range ticker.C{ok,_,_:=procIsWindow.Call(parent);if ok==0{logger.Printf("parent window disappeared; terminating");w.Terminate();return}}}()
    w.Run();logger.Printf("=== STOP %s build=%s ===",marker,hostBuild)
}
