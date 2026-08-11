package main

import (
	"embed"
	"os"
	"os/signal"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Catch SIGINT (Ctrl-C) and stop all engines so that
	// nginx/httpd/mysqld/php do not leak as orphans bound to 8000/3307/8001.
	//
	// IMPORTANT: We deliberately do NOT listen for SIGTERM or SIGHUP here.
	// With Apache started via -DFOREGROUND and nginx via `daemon off;`, those
	// processes are direct children of this Go process. When the user clicks
	// Stop, StopApache/StopNginx send SIGTERM to the child; that signal can
	// propagate (or be observed via SIGHUP from workers) by the parent Go
	// process. If we listened for SIGTERM/SIGHUP, the signal handler would
	// fire during a normal Stop click, call stopAllEngines again, and then
	// os.Exit(0) — which force-closes the app every time the user Stops a
	// service. Wails' OnShutdown hook + this SIGINT handler are enough.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT)
	go func() {
		<-sigCh
		app.stopAllEngines()
		os.Exit(0)
	}()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "ZAMPP",
		Width:  620,
		Height: 360,
		DisableResize: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 245, G: 245, B: 247, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
	// Final safety net: stop engines if wails.Run returns without going
	// through OnShutdown (e.g. runtime panic during close).
	app.stopAllEngines()
}
