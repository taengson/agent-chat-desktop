package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[ChatEvent](chatEventName)
	application.RegisterEvent[AgenticEvaluationEvent](agenticEvaluationEventName)
}

func main() {
	chatService := NewApp()
	app := application.New(application.Options{
		Name:        "Agent Chat",
		Description: "A simple desktop client for local and OpenAI-compatible AI models",
		Services: []application.Service{
			application.NewService(chatService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Agent Chat",
		Width:            1180,
		Height:           780,
		MinWidth:         920,
		MinHeight:        640,
		BackgroundColour: application.NewRGB(247, 247, 245),
		URL:              "/",
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
