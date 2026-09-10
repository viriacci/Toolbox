from pathlib import Path
import re, sys
p=Path(sys.argv[1])
s=p.read_text(encoding='utf-8')

def one(old,new,label):
    global s
    if old not in s:
        raise SystemExit(f'missing {label}')
    s=s.replace(old,new,1)

one('const hostBuild = "34.9-multitab-media"','const hostBuild = "34.9.3-preload-fast"','host build')
one('const windowMode = "owned-popup-multitab"','const windowMode = "owned-popup-multitab-preload"','window mode')
one('''\tPipMessage            string `json:"pipMessage"`\n}''','''\tPipMessage            string `json:"pipMessage"`\n\tPreloaded             bool   `json:"preloaded"`\n\tReadyAfterMs          int64  `json:"readyAfterMs"`\n\tCreatedAt             string `json:"createdAt"`\n}''','tab perf fields')
one('''\tcreateMu            sync.Mutex\n\tsessionClearPending bool''','''\tcreateGate          chan struct{}\n\tsessionClearPending bool''','create gate field')
s=s.replace('case <-time.After(10 * time.Second):','case <-time.After(35 * time.Second):',1)
s=s.replace('case <-time.After(20 * time.Second):','case <-time.After(35 * time.Second):',1)
one('''\tt := &tab{id: id, host: h, ready: make(chan struct{})}\n\tt.state = tabState{ID: id}''','''\tt := &tab{id: id, host: h, ready: make(chan struct{})}\n\tt.state = tabState{ID: id, CreatedAt: time.Now().Format(time.RFC3339Nano)}''','created at')
one('''\truntime.LockOSThread()\n\th.createMu.Lock()\n\tw := webview2.NewWithOptions(webview2.WebViewOptions{Debug: false, AutoFocus: false, DataPath: h.profilePath, WindowOptions: webview2.WindowOptions{Title: "INSOFTWARE Network " + t.id, Width: 2, Height: 2}})\n\th.createMu.Unlock()''','''\truntime.LockOSThread()\n\tstarted := time.Now()\n\th.createGate <- struct{}{}\n\tw := webview2.NewWithOptions(webview2.WebViewOptions{Debug: false, AutoFocus: false, DataPath: h.profilePath, WindowOptions: webview2.WindowOptions{Title: "INSOFTWARE Network " + t.id, Width: 2, Height: 2}})\n\t<-h.createGate''','parallel init gate')
one('''\tw.Resize()\n\t_ = w.SetControllerVisible(true)\n\t_, _, _ = procShowWindow.Call(hwnd, swHide)''','''\tw.Resize()\n\t_ = w.SetControllerVisible(false)\n\t_, _, _ = procShowWindow.Call(hwnd, swHide)''','initial controller hidden')
one('''\tt.mutate(func(s *tabState) { s.Ready = true; s.LastAction = "ready"; s.Error = "" })''','''\tt.mutate(func(s *tabState) { s.Ready = true; s.LastAction = "ready"; s.Error = ""; s.ReadyAfterMs = time.Since(started).Milliseconds() })''','ready timing')
one('''\tnavigate := before.Source == "" || before.RequestedURL != target''','''\tnavigate := before.RequestedURL != target || (before.Source == "" && !before.Loading)''','activate preload-aware')
one('''function medias(){try{var a=Array.from(document.querySelectorAll("video,audio"));var pw=window.documentPictureInPicture&&window.documentPictureInPicture.window;if(pw&&pw.document)a=a.concat(Array.from(pw.document.querySelectorAll("video,audio")));return Array.from(new Set(a))}catch(e){return []}}''','''function medias(){try{return Array.from(document.querySelectorAll("video,audio"))}catch(e){return []}}''','media list pip')
one('''pipSupported:!!(window.documentPictureInPicture||document.pictureInPictureEnabled)''','''pipSupported:!!document.pictureInPictureEnabled''','pip support')
pat=r'async function openPip\(\)\{.*?\}\s*function armPip\(msg\)\{'
m=re.search(pat,s,flags=re.S)
if not m:
    raise SystemExit('missing openPip function')
replacement='''async function openPip(){var v=bestVideo();if(!v)throw new Error("Nie znaleziono elementu wideo na tej karcie");if(document.pictureInPictureElement){try{await document.exitPictureInPicture()}catch(e){};TB.pipOpen=false;TB.pipMessage="";report();return true}if(document.pictureInPictureEnabled&&v.requestPictureInPicture){await v.requestPictureInPicture();TB.pipOpen=true;TB.pipMessage="Standardowy PiP";report();v.addEventListener("leavepictureinpicture",function(){TB.pipOpen=false;TB.pipMessage="";report()},{once:true});return true}throw new Error("Ta strona/runtime nie obsługuje standardowego PiP")}\nfunction armPip(msg){'''
s=s[:m.start()]+replacement+s[m.end():]
needle='''func (h *host) handlers() http.Handler {\n'''
if needle not in s:
    raise SystemExit('missing handlers')
preload=r'''func (h *host) preloadTab(id, target, home string) {
\tgo func() {
\t\tt, err := h.ensureTab(id)
\t\tif err != nil {
\t\t\th.log.Printf("PRELOAD %s init error: %v", id, err)
\t\t\treturn
\t\t}
\t\ttarget = strings.TrimSpace(target)
\t\tif target == "" {
\t\t\treturn
\t\t}
\t\tbefore := t.snapshot()
\t\tif before.RequestedURL == target && (before.Loading || before.Source != "") {
\t\t\treturn
\t\t}
\t\terr = t.runUI(5*time.Second, func() error {
\t\t\tt.mutate(func(s *tabState) {
\t\t\t\ts.Preloaded = true
\t\t\t\ts.Visible = false
\t\t\t\ts.RequestedURL = target
\t\t\t\tif home != "" { s.Home = home } else if s.Home == "" { s.Home = target }
\t\t\t\ts.Loading = true
\t\t\t\ts.LastAction = "preload"
\t\t\t\ts.NavigationCount++
\t\t\t})
\t\t\t_ = t.w.SetControllerVisible(false)
\t\t\t_, _, _ = procShowWindow.Call(t.hwnd, swHide)
\t\t\tt.w.Navigate(target)
\t\t\treturn nil
\t\t})
\t\tif err != nil {
\t\t\tt.setError(err)
\t\t\th.log.Printf("PRELOAD %s navigation error: %v", id, err)
\t\t\treturn
\t\t}
\t\th.log.Printf("PRELOAD %s started url=%s", id, target)
\t}()
}

'''
s=s.replace(needle,preload+needle,1)
one('''"multitab": true, "mediaBackground": true, "documentPip": true''','''"multitab": true, "mediaBackground": true, "documentPip": false, "standardPip": true, "backgroundPreload": true, "initConcurrency": 2, "sharedUserDataBrowserProcess": true''','runtime capabilities')
needle='''\tmux.HandleFunc("/tab/activate", cors(func(w http.ResponseWriter, r *http.Request) {'''
if needle not in s:
    raise SystemExit('missing activate route')
route='''\tmux.HandleFunc("/tab/preload", cors(func(w http.ResponseWriter, r *http.Request) {\n\t\tif !requirePost(w, r) { return }\n\t\tq := r.URL.Query(); id := validTabID(q.Get("tab")); target := strings.TrimSpace(q.Get("url")); home := strings.TrimSpace(q.Get("home"))\n\t\tif id == "" || target == "" { jsonWrite(w, 400, map[string]any{"ok": false, "error": "brak tab/url"}); return }\n\t\th.preloadTab(id, target, home)\n\t\tjsonWrite(w, http.StatusAccepted, map[string]any{"ok": true, "accepted": true, "tab": id})\n\t}))\n'''
s=s.replace(needle,route+needle,1)
one('''h := &host{tabs: map[string]*tab{}, parent: parent, parentPID: *parentPID, port: *port, log: logger, profilePath: profilePath, clearFlag: clearFlag}''','''h := &host{tabs: map[string]*tab{}, parent: parent, parentPID: *parentPID, port: *port, log: logger, profilePath: profilePath, clearFlag: clearFlag, createGate: make(chan struct{}, 2)}''','host init gate')
p.write_text(s,encoding='utf-8')
print('patched V34.9 -> V34.9.3')
