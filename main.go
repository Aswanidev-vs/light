package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
	lightapp "light/internal/light"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed logo.png
var icon []byte

func main() {
	settings := lightapp.NewSettingsService(nil)
	manager := lightapp.NewTransferManager()
	discovery := lightapp.NewDiscoveryService(nil, settings)
	transfer := lightapp.NewFileTransferService(nil, manager, settings, discovery)
	qr := lightapp.NewQRCodeService(nil, settings, discovery)

	app := application.New(application.Options{
		Name:        "Light",
		Description: "Fast, local, private file sharing",
		Icon:        icon,
		Services: []application.Service{
			application.NewService(settings),
			application.NewService(manager),
			application.NewService(discovery),
			application.NewService(transfer),
			application.NewService(qr),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	settings.SetApp(app)
	discovery.SetApp(app)
	transfer.SetApp(app)
	qr.SetApp(app)

	if err := transfer.StartServer(); err != nil {
		log.Printf("Warning: transfer server failed to start: %v", err)
	}
	if err := discovery.Start(); err != nil {
		log.Printf("Warning: discovery failed to start: %v", err)
	}

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:    "Light",
		Width:    1100,
		Height:   720,
		MinWidth: 820,
		MinHeight: 600,
	})

	if err := app.Run(); err != nil {
		log.Printf("App exited with error: %v", err)
	}
}
