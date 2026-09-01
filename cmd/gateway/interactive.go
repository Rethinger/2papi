package main

// Interactive subcommands referenced by main() but never defined upstream:
//   - RunTUI  — `2papi tui` keyboard menu (README: Start / Providers / Quota /
//               Plugins / 2papi.local), backed by internal/tui;
//   - RunInit — `2papi init` enables 2papi.local via mDNS or hosts.
// They were advertised in the README and called from main.go, yet had no
// implementation, which left the whole cmd/gateway package uncompilable.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/Rethinger/2papi/internal/hosts"
	"github.com/Rethinger/2papi/internal/tui"
)

// args0 returns the current executable path (os.Args[0] is fine for
// re-invocation from the installed binary or `go run`).
func args0() string {
	if p, err := filepath.Abs(os.Args[0]); err == nil {
		return p
	}
	return os.Args[0]
}

// RunTUI renders the interactive console menu (like 9router cli/).
func RunTUI() error {
	config := ""
	if _, err := os.Stat("config/example.yaml"); err == nil {
		config = "config/example.yaml"
	} else {
		config = "~/.2papi/config.yaml"
	}
	return tui.Menu("2papi — interactive console", []tui.Item{
		{Label: "Start gateway", Kind: "menu", Run: func() error {
			fmt.Printf("\nStarting gateway in this terminal (%s). Ctrl+C stops it and returns to the menu.\n\n", config)
			cmd := exec.Command(args0(), "-config", config)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			err := cmd.Run()
			if _, ok := err.(*exec.ExitError); ok {
				// daemon terminated by Ctrl+C — that's a normal return
				return nil
			}
			return err
		}},
		{Label: "Providers", Kind: "menu", Run: func() error {
			fmt.Printf("\nDashboard: http://localhost:8080/dashboard/  — manage accounts/providers there.\n")
			return nil
		}},
		{Label: "Quota", Kind: "menu", Run: func() error {
			fmt.Printf("\nProvider quota (X-Provider-Quota-*) is exposed via GET /api/quota — open the dashboard Overview.\n")
			return nil
		}},
		{Label: "Plugins", Kind: "menu", Run: func() error {
			fmt.Printf("\nConfig-declared HTTP sidecar plugins run automatically; manage them in the dashboard Settings.\n")
			return nil
		}},
		{Label: "2papi.local", Kind: "menu", Run: func() error {
			return RunInit(defaultHostname, 8080)
		}},
	})
}

// RunInit interactively enables 2papi.local: mDNS (no admin, LAN-wide) or a
// hosts entry (this machine, needs admin) — see README Quick Install.
func RunInit(hostname string, port int) error {
	choice := "2"
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Println("Resolve 2papi.local on this network:")
		fmt.Println("  1) mDNS/Bonjour — LAN-wide, no admin (keep alive with `2papi advert` or --mdns)")
		fmt.Println("  2) hosts entry — this machine only, needs admin")
		fmt.Print("Choice [2]: ")
		if _, err := fmt.Scanln(&choice); err != nil && choice == "" {
			choice = "2"
		}
	}
	switch strings.TrimSpace(choice) {
	case "1":
		fmt.Printf("mDNS selected — start advertising with: `2papi advert --hostname %s --port %d`,\nor launch the gateway with `--mdns --hostname %s`.\n", hostname, port, hostname)
	case "2":
		if err := hosts.AddEntry(hostname); err != nil {
			log.Printf("hosts update failed: %v", err)
			return err
		}
		fmt.Printf("done: %s now resolves on this machine (%s)\n", hostname, hosts.Path())
	default:
		return fmt.Errorf("unknown choice %q", strings.TrimSpace(choice))
	}
	return nil
}
