package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/barbarosydev/ai-factory/internal/config"
	"github.com/barbarosydev/ai-factory/internal/controlplane"
	"github.com/barbarosydev/ai-factory/internal/gitworktree"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: factory <doctor|run|once|status|clean-failed> [flags]")
		os.Exit(2)
	}

	cfg, err := config.LoadFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ExitOnError)
		strictGitHub := fs.Bool("strict-github", false, "fail if GitHub App credentials are missing")
		_ = fs.Parse(args)

		if !controlplane.RunDoctor(ctx, cfg, "", *strictGitHub, os.Stdout) {
			os.Exit(1)
		}

	case "run", "once":
		opts := parseRunOptions(cmd, cfg, args, cmd == "once")
		if err := controlplane.Run(ctx, cfg, opts, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "%s error: %v\n", cmd, err)
			os.Exit(1)
		}

	case "status":
		if err := controlplane.PrintStatus(ctx, cfg, "", os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "status error: %v\n", err)
			os.Exit(1)
		}

	case "clean-failed":
		mgr := gitworktree.NewManager(cfg.WebsitesRepoPath, 0, nil)
		removed, err := mgr.CleanFailedSnapshots(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "clean-failed error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("removed %d archived failed snapshot(s) in %s\n", removed, cfg.WebsitesRepoPath)

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q (expected doctor, run, once, status, clean-failed)\n", cmd)
		os.Exit(2)
	}
}

func parseRunOptions(name string, cfg config.Config, args []string, defaultOnce bool) controlplane.RunOptions {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	mode := fs.String("mode", string(cfg.AutonomyMode), "autonomy mode (review or auto)")
	skipRemoteCI := fs.Bool("skip-remote-ci", cfg.SkipRemoteCI, "skip polling remote GitHub Check Runs in auto mode")
	once := fs.Bool("once", defaultOnce, "process at most one issue and exit when finished")
	issue := fs.Int("issue", 0, "target GitHub issue number (optional with --once)")
	_ = fs.Parse(args)
	return controlplane.RunOptions{
		AutonomyMode: config.AutonomyMode(*mode),
		SkipRemoteCI: *skipRemoteCI,
		Once:         *once,
		IssueNumber:  *issue,
	}
}
