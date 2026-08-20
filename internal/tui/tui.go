package tui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// Item is one menu row.
type Item struct {
	Label string
	// Kind drives the terminal action: "menu" items run Run, "info"/"header"/"sep"
	// are non-selectable.
	Kind string
	Run  func() error
}

const (
	clearScreen = "\033[2J\033[H"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"
	arrowUp     = "\x1b[A"
	arrowDown   = "\x1b[B"
	enter       = "\r"
)

// Menu renders a lightweight keyboard-driven menu (like 9router cli/) using
// raw terminal mode. No external framework — just term.RawMode + ANSI.
func Menu(title string, items []Item) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Fallback: line-based menu when not a TTY (CI, pipe).
		return lineMenu(title, items)
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return lineMenu(title, items)
	}
	defer term.Restore(fd, state)
	defer fmt.Print(showCursor)

	sel := 0
	selectable := 0
	for _, it := range items {
		if it.Kind == "menu" {
			selectable++
		}
	}
	if selectable == 0 {
		return nil
	}

	var buf = make([]byte, 1)
	for {
		fmt.Print(clearScreen + hideCursor)
		fmt.Printf("\033[1m%s\033[0m\n\n", title)
		idx := 0
		for _, it := range items {
			switch it.Kind {
			case "menu":
				marker := " "
				if idx == sel {
					marker = ">"
				}
				fmt.Printf("  %s %s\n", marker, it.Label)
				idx++
			case "info":
				fmt.Printf("  \033[2m%s\033[0m\n", it.Label)
			case "header":
				fmt.Printf("\n  \033[3m%s\033[0m\n", it.Label)
			case "sep":
				fmt.Printf("  \033[2m——\033[0m\n")
			}
		}
		fmt.Printf("\n  \033[2m↑/↓ select · Enter run · q quit\033[0m\n")

		n, _ := os.Stdin.Read(buf)
		if n == 0 {
			continue
		}
		switch string(buf[:n]) {
		case "q", "\x03": // q or Ctrl+C
			fmt.Print("\r\n")
			return nil
		case arrowUp:
			if sel > 0 {
				sel--
			}
		case arrowDown:
			if sel < selectable-1 {
				sel++
			}
		case enter:
			// find selected menu item
			idx := 0
			for _, it := range items {
				if it.Kind != "menu" {
					continue
				}
				if idx == sel && it.Run != nil {
					term.Restore(fd, state)
					fmt.Print(showCursor + "\r\n")
					if err := it.Run(); err != nil {
						fmt.Printf("\033[31m! %v\033[0m\n", err)
					}
					fmt.Print("\r\n\033[2mPress Enter to return to menu...\033[0m")
					var b [1]byte
					for {
						nn, _ := os.Stdin.Read(b[:])
						if nn > 0 && (b[0] == '\r' || b[0] == '\n') {
							break
						}
					}
					state, _ = term.MakeRaw(fd)
					break
				}
				idx++
			}
		}
	}
}

// lineMenu is a TTY fallback for non-interactive output.
func lineMenu(title string, items []Item) error {
	fmt.Printf("%s\n", title)
	n := 0
	for i, it := range items {
		switch it.Kind {
		case "menu":
			n++
			fmt.Printf("  %d. %s\n", n, it.Label)
		case "header":
			if i > 0 {
				fmt.Println()
			}
			fmt.Println(it.Label)
		case "info":
			fmt.Printf("  %s\n", it.Label)
		}
	}
	fmt.Printf("Selection (1-%d, 0=quit): ", n)
	var sel int
	if _, err := fmt.Scan(&sel); err != nil {
		return err
	}
	if sel <= 0 {
		return nil
	}
	idx := 0
	for _, it := range items {
		if it.Kind != "menu" {
			continue
		}
		idx++
		if idx == sel && it.Run != nil {
			return it.Run()
		}
	}
	return nil
}

func countSelectable(items []Item) int {
	c := 0
	for _, it := range items {
		if it.Kind == "menu" {
			c++
		}
	}
	return c
}

// Banner prints the styled startup banner.
func Banner() {
	b := []string{
		"  ____ _____ ____  ____ ___",
		" |  _ \\_   _|  _ \\|  _ \\_ _|",
		" | |_) || | | |_) | |_) | |",
		" |  __/ | | |  __/|  __/| |",
		" |_|    |_| |_|   |_|  |___|",
	}
	for _, line := range b {
		fmt.Printf("\033[36m%s\033[0m\n", line)
	}
	fmt.Printf("\033[2m  lightweight multi-account AI gateway — fast like nobody\n\033[0m")
	_ = strings.TrimSpace
}
