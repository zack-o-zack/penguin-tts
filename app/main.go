package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	"penguin.local/app/internal/models"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := application.New(application.Options{
		Name:        "Penguin",
		Description: "Local and BYOK text to speech",
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.RegisterService(models.WailsService(app, modelConfig()))

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Penguin",
		Width:     1100,
		Height:    760,
		MinWidth:  720,
		MinHeight: 540,
		URL:       "/",
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHiddenInset,
			Backdrop: application.MacBackdropTranslucent,
		},
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
