//go:build windows

package procutil

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the children are placed in a Job Object with
// KILL_ON_JOB_CLOSE: when the agent process ends (even when killed), the
// handle closes and the system terminates every pipeline, the equivalent of
// PDEATHSIG on Linux. The job handle is intentionally never closed.
var (
	jobOnce sync.Once
	job     windows.Handle
)

func killOnCloseJob() windows.Handle {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // G103: required by the Win32 API

			_ = windows.CloseHandle(h)
			return
		}
		job = h
	})
	return job
}

func SetAttr(*exec.Cmd) {}

// AfterStart attaches the started process to the kill-on-close job.
func AfterStart(cmd *exec.Cmd) {
	j := killOnCloseJob()
	if j == 0 || cmd.Process == nil {
		return
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid)) //nolint:gosec // PIDs fit in uint32
	if err != nil {
		return
	}
	defer windows.CloseHandle(p) //nolint:errcheck
	_ = windows.AssignProcessToJobObject(j, p)
}
