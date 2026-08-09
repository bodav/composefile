// Command testhelper/fakessh is a fake OpenSSH client used by remote-package
// tests. It reads behavior rules from FAKE_SSH_PLAN. Each plan line is:
//
//	match<TAB>base64-output<TAB>exit-code
//
// Rules are matched by content against the script argument and consumed after
// use; an empty "match" is a catch-all. If "match" is contained in the script,
// the encoded output is printed and the exit code returned. The special match
// "__CAT__" captures stdin to FAKE_SSH_CAT_OUT. Every invocation is appended to
// FAKE_SSH_LOG as "target|script".
package main

import (
	"encoding/base64"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	args := os.Args[1:]
	script := strings.Join(args, " ")

	if log := os.Getenv("FAKE_SSH_LOG"); log != "" {
		if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.WriteString(strings.Join(args, "|") + "\n")
			f.Close()
		}
	}

	plan := os.Getenv("FAKE_SSH_PLAN")
	if plan == "" {
		os.Exit(0)
	}

	lines := readLines(plan)
	idx := -1
	for i, l := range lines {
		parts := strings.SplitN(l, "\t", 3)
		m := parts[0]
		if m == "__CAT__" {
			if strings.Contains(script, "cat > ") {
				idx = i
				break
			}
			continue
		}
		if m == "" || matchesAll(script, m) {
			idx = i
			break
		}
	}
	if idx < 0 {
		os.Exit(0)
	}

	line := lines[idx]
	writeLines(plan, append(append([]string{}, lines[:idx]...), lines[idx+1:]...))

	parts := strings.SplitN(line, "\t", 3)
	if parts[0] == "__CAT__" {
		if catOut := os.Getenv("FAKE_SSH_CAT_OUT"); catOut != "" {
			if data, err := io.ReadAll(os.Stdin); err == nil {
				os.WriteFile(catOut, data, 0o644)
			}
		}
		os.Exit(0)
	}

	code := 0
	out := ""
	if len(parts) >= 2 && parts[1] != "" {
		if b, err := base64.StdEncoding.DecodeString(parts[1]); err == nil {
			out = string(b)
		}
	}
	if len(parts) >= 3 {
		if c, err := strconv.Atoi(parts[2]); err == nil {
			code = c
		}
	}
	os.Stdout.WriteString(out)
	os.Exit(code)
}

// matchesAll reports whether script contains every ";;;"-separated substring.
func matchesAll(script, spec string) bool {
	for _, part := range strings.Split(spec, ";;;") {
		if !strings.Contains(script, part) {
			return false
		}
	}
	return true
}

func readLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	raw := strings.Split(string(data), "\n")
	out := raw[:0]
	for _, l := range raw {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func writeLines(path string, lines []string) {
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
