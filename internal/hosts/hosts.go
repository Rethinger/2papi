package hosts

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Path returns the OS-specific hosts file path.
func Path() string {
	if runtime.GOOS == "windows" {
		if p := os.Getenv("SystemRoot"); p != "" {
			return p + "\\System32\\drivers\\etc\\hosts"
		}
		return "C:\\Windows\\System32\\drivers\\etc\\hosts"
	}
	return "/etc/hosts"
}

// HasEntry reports whether hostname is already mapped to 127.0.0.1.
func HasEntry(hostname string) bool {
	f, err := os.Open(Path())
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "127.0.0.1" && fields[0] != "::1" {
			continue
		}
		for _, h := range fields[1:] {
			if h == hostname {
				return true
			}
		}
	}
	return false
}

// AddEntry appends "127.0.0.1 <hostname>  # 2papi" to the hosts file.
// It is idempotent. Requires admin/root. Returns a user-friendly error
// suggesting manual edit if permission is denied.
func AddEntry(hostname string) error {
	if HasEntry(hostname) {
		return nil
	}
	p := Path()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied for %s — run: sudo sh -c 'echo \"127.0.0.1 %s  # 2papi\" >> %s' (or add manually)", p, hostname, p)
		}
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n127.0.0.1 %s  # 2papi\n", hostname)
	return err
}

// RemoveEntry removes the 2papi hostname line.
func RemoveEntry(hostname string) error {
	p := Path()
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "# 2papi") && strings.Contains(line, hostname) {
			continue
		}
		if trimmed == "127.0.0.1 "+hostname || trimmed == "::1 "+hostname {
			continue
		}
		out = append(out, line)
	}
	// Only write if changed
	if len(out) == len(lines) {
		return nil
	}
	newData := strings.Join(out, "\n")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(newData), 0644); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied for %s — remove line \"127.0.0.1 %s\" manually", p, hostname)
		}
		return err
	}
	return os.Rename(tmp, p)
}
