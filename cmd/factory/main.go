package main

import (
	"fmt"
	"os"

	"github.com/barbarosydev/ai-factory/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: factory <doctor|run|once|status>")
		os.Exit(2)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "doctor":
		fmt.Printf("ai-factory config ok (temporal=%s litellm=%s repo=%s)\n",
			cfg.TemporalAddress, cfg.LiteLLMBaseURL, cfg.WebsitesRepoPath)
	case "run", "once", "status":
		fmt.Fprintf(os.Stderr, "subcommand %q is scheduled for Phases 5-6\n", os.Args[1])
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
