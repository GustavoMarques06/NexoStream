//go:build !linux && !windows

package procutil

import "os/exec"

func SetAttr(*exec.Cmd) {}

func AfterStart(*exec.Cmd) {}
