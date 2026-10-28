package main

import (
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var mainWindow *application.WebviewWindow

func main() {
	srv, err := NewServer()
	if err != nil {
		log.Fatal(err)
	}
	url, err := srv.Start()
	if err != nil {
		log.Fatal(err)
	}

	app := application.New(application.Options{
		Name:        "wailsterm",
		Description: "Terminal / SSH / SFTP client: electerm UI on a Go backend",
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// widget servers (file/FTP/SSH) must not outlive the app
		OnShutdown: srv.widgets.stopAll,
	})

	// The UI (electerm's client) is served by srv; the window simply loads it.
	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "electerm",
		Width:            1200,
		Height:           760,
		MinWidth:         320,
		MinHeight:        400,
		BackgroundColour: application.NewRGB(0, 0, 0),
		URL:              url,
		DevToolsEnabled:  true,
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
