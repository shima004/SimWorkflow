package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// ReadEnvFile reads literal KEY=value assignments. It supports export prefixes,
// single/double quotes, and comments. It never evaluates shell expressions,
// expands variables, or changes the process environment.
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "export ") || strings.HasPrefix(s, "export\t") {
			s = strings.TrimSpace(s[len("export"):])
		}
		key, value, ok := strings.Cut(s, "=")
		key = strings.TrimSpace(key)
		if !ok || !validKey(key) {
			return nil, fmt.Errorf("invalid .env assignment at line %d", line)
		}
		value = strings.TrimSpace(value)
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("unterminated .env quote at line %d", line)
			}
			end++
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, fmt.Errorf("invalid .env quoted value at line %d", line)
			}
			value = value[1:end]
		} else {
			for i, r := range value {
				if r == '#' && (i == 0 || unicode.IsSpace(rune(value[i-1]))) {
					value = strings.TrimSpace(value[:i])
					break
				}
			}
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read .env: %w", err)
	}
	return values, nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c != '_' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// WithFallback preserves explicitly set (even empty) process values.
func WithFallback(primary func(string) (string, bool), fallback map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if value, ok := primary(key); ok {
			return value, true
		}
		value, ok := fallback[key]
		return value, ok
	}
}
