package window

import (
	"sync"

	"github.com/jchv/go-webview2"
)

var (
	mu       sync.Mutex
	instance webview2.WebView
	opened   bool
)

// Open открывает окно с Web UI. Если окно уже открыто — фокусирует его.
func Open(url string) {
	mu.Lock()
	if opened {
		mu.Unlock()
		return
	}
	opened = true
	mu.Unlock()

	go func() {
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			Debug:  false,
			Window: nil,
		})
		if w == nil {
			mu.Lock()
			opened = false
			mu.Unlock()
			return
		}
		defer func() {
			w.Destroy()
			mu.Lock()
			opened = false
			instance = nil
			mu.Unlock()
		}()

		mu.Lock()
		instance = w
		mu.Unlock()

		w.SetTitle("Proxy Control")
		w.SetSize(960, 640, webview2.HintNone)
		w.Navigate(url)
		w.Run()
	}()
}

// Close закрывает окно если оно открыто
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		instance.Terminate()
	}
}
