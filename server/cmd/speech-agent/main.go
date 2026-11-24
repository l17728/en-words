package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"enwords/internal/speechagent"
)

func main() {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		fmt.Fprintln(os.Stderr, "speech-agent configuration error: user config directory unavailable")
		os.Exit(2)
	}
	server := flag.String("server", "", "server HTTPS origin, e.g. https://words.example.com")
	console := flag.String("console", "http://127.0.0.1:8090", "loopback Model Console origin")
	spool := flag.String("spool", filepath.Join(configDir, "DingDangWords", "speech-agent"), "private durable spool directory")
	caFile := flag.String("ca-file", "", "optional PEM CA bundle to extend system trust roots")
	name := flag.String("name", "local-tts-runner", "display name shown to administrators")
	flag.Parse()

	runner, err := speechagent.NewRunner(speechagent.RunnerConfig{
		ServerOrigin:    *server,
		ConsoleOrigin:   *console,
		SpoolDir:        *spool,
		CAFile:          *caFile,
		EnrollmentToken: os.Getenv("ENWORDS_SPEECH_ENROLLMENT_TOKEN"),
		AgentToken:      os.Getenv("ENWORDS_SPEECH_AGENT_TOKEN"),
		DisplayName:     *name,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "speech-agent configuration error:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "speech-agent stopped:", err)
		os.Exit(1)
	}
}
