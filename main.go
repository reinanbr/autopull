package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/reinanbr/autopull/autopull"
)

var version = "v1.2.16"

const usage = `Usage: autopull [command] [config_path]

Commands:
  (none)           start watching (default config: ./config_auto_pull.json)
  daemon           start watching in background (detached)
  start            alias for 'daemon'
  init             create config_auto_pull.json for the current git repo
  status           show daemon status and stats
  stop             send SIGTERM to the running daemon
  logs [N]         print last N lines of the log (default: 50)
  dry-run          validate config and connectivity without pulling
  service ...      systemd integration (install/start/stop/status/logs/list)
  --version, -v    print version

Token for private repos:
  Set AUTOPULL_TOKEN or GITHUB_TOKEN in environment or in .env inside repo_path.
  Never put the token in config_auto_pull.json.
`

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		autopull.RunWatcher("config_auto_pull.json")
		return
	}

	switch args[0] {
	case "--version", "-v":
		fmt.Println("autopull", version)
		return

	case "--help", "-h":
		fmt.Print(usage)
		return

	case "init":
		cfg := ""
		if len(args) > 1 {
			cfg = args[1]
		}
		if err := autopull.CmdInit(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "init: %v\n", err)
			os.Exit(1)
		}
		return

	case "daemon":
		cfg := ""
		if len(args) > 1 {
			cfg = args[1]
		}
		if err := autopull.CmdDaemon(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "daemon: %v\n", err)
			os.Exit(1)
		}
		return

	case "start":
		cfg := ""
		if len(args) > 1 {
			cfg = args[1]
		}
		if err := autopull.CmdDaemon(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "start: %v\n", err)
			os.Exit(1)
		}
		return

	case "status":
		cfg := ""
		if len(args) > 1 {
			cfg = args[1]
		}
		if err := autopull.CmdStatus(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "status: %v\n", err)
			os.Exit(1)
		}
		return

	case "stop":
		cfg := ""
		if len(args) > 1 {
			cfg = args[1]
		}
		if err := autopull.CmdStop(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "stop: %v\n", err)
			os.Exit(1)
		}
		return

	case "logs":
		cfg := ""
		lines := 50
		for _, a := range args[1:] {
			if n, err := strconv.Atoi(a); err == nil {
				lines = n
			} else {
				cfg = a
			}
		}
		if err := autopull.CmdLogs(cfg, lines); err != nil {
			fmt.Fprintf(os.Stderr, "logs: %v\n", err)
			os.Exit(1)
		}
		return

	case "dry-run":
		cfg := ""
		if len(args) > 1 {
			cfg = args[1]
		}
		if err := autopull.CmdDryRun(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "dry-run: %v\n", err)
			os.Exit(1)
		}
		return

	case "service":
		if err := autopull.CmdService(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "service: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// backward compatible: treat argument as config path if it looks like one
	candidate := args[len(args)-1]
	if strings.HasSuffix(candidate, ".json") {
		autopull.RunWatcher(candidate)
		return
	}

	fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
	fmt.Print(usage)
	os.Exit(1)
}
