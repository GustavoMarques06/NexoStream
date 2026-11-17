// Package config reads settings from environment variables. Secrets can be
// given as files through the <NAME>_FILE convention so they never appear in
// the environment of the process listing or in the repository.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Prefix is prepended to every variable name.
const Prefix = "NEXOSTREAM_"

// Env is a typed reader that accumulates errors.
type Env struct {
	lookup func(string) (string, bool)
	errs   []string
}

// FromOS reads the real environment.
func FromOS() *Env { return &Env{lookup: os.LookupEnv} }

// FromMap reads from a map (tests).
func FromMap(m map[string]string) *Env {
	return &Env{lookup: func(k string) (string, bool) { v, ok := m[k]; return v, ok }}
}

func (e *Env) get(name string) (string, bool) {
	v, ok := e.lookup(Prefix + name)
	return strings.TrimSpace(v), ok && strings.TrimSpace(v) != ""
}

// String returns the variable or def.
func (e *Env) String(name, def string) string {
	if v, ok := e.get(name); ok {
		return v
	}
	return def
}

// Secret returns NAME, or the content of the file named by NAME_FILE.
func (e *Env) Secret(name string) string {
	if v, ok := e.get(name); ok {
		return v
	}
	if p, ok := e.get(name + "_FILE"); ok {
		b, err := os.ReadFile(p)
		if err != nil {
			e.errs = append(e.errs, fmt.Sprintf("%s%s_FILE: %v", Prefix, name, err))
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	return ""
}

// Int returns the variable as an int.
func (e *Env) Int(name string, def int) int {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Sprintf("%s%s: %v", Prefix, name, err))
		return def
	}
	return n
}

// Float returns the variable as a float64.
func (e *Env) Float(name string, def float64) float64 {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		e.errs = append(e.errs, fmt.Sprintf("%s%s: %v", Prefix, name, err))
		return def
	}
	return n
}

// Bool returns the variable as a bool.
func (e *Env) Bool(name string, def bool) bool {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Sprintf("%s%s: %v", Prefix, name, err))
		return def
	}
	return b
}

// Duration returns the variable as a time.Duration ("5s", "1m").
func (e *Env) Duration(name string, def time.Duration) time.Duration {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Sprintf("%s%s: %v", Prefix, name, err))
		return def
	}
	return d
}

// List returns a comma-separated list.
func (e *Env) List(name string, def []string) []string {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Require records an error when value is empty.
func (e *Env) Require(name, value string) {
	if value == "" {
		e.errs = append(e.errs, Prefix+name+" is required")
	}
}

// Err returns all accumulated errors.
func (e *Env) Err() error {
	if len(e.errs) == 0 {
		return nil
	}
	return fmt.Errorf("configuration: %s", strings.Join(e.errs, "; "))
}
