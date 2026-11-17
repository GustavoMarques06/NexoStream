package agent

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"nexostream/internal/model"
)

// sampler measures node resources for heartbeats.
type sampler struct {
	lastAt        time.Time
	lastIn        uint64
	lastOut       uint64
	interfaces    []string // restrict network counters (empty = all)
	nvidiaSMIPath string
}

func newSampler(interfaces []string) *sampler {
	s := &sampler{interfaces: interfaces}
	if p, err := exec.LookPath("nvidia-smi"); err == nil {
		s.nvidiaSMIPath = p
	}
	_, _ = cpu.Percent(0, false) // prime the CPU counter
	return s
}

func (s *sampler) sample(ctx context.Context) model.Resources {
	var r model.Resources
	if p, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(p) > 0 {
		r.CPUPercent = round1(p[0])
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		r.MemoryUsedBytes, r.MemoryTotalBytes = vm.Used, vm.Total
	}
	if counters, err := gnet.IOCountersWithContext(ctx, true); err == nil {
		var in, out uint64
		for _, c := range counters {
			if len(s.interfaces) > 0 && !contains(s.interfaces, c.Name) {
				continue
			}
			in += c.BytesRecv
			out += c.BytesSent
		}
		now := time.Now()
		if !s.lastAt.IsZero() && in >= s.lastIn && out >= s.lastOut {
			el := now.Sub(s.lastAt).Seconds()
			r.NetworkInBps = uint64(float64(in-s.lastIn) * 8 / el)
			r.NetworkOutBps = uint64(float64(out-s.lastOut) * 8 / el)
		}
		s.lastAt, s.lastIn, s.lastOut = now, in, out
	}
	if s.nvidiaSMIPath != "" {
		if g, ok := gpuPercent(ctx, s.nvidiaSMIPath); ok {
			r.GPUPercent = &g
		}
	}
	return r
}

func gpuPercent(ctx context.Context, path string) (float64, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0, false
	}
	var sum float64
	var n int
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(l), 64); err == nil {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return round1(sum / float64(n)), true
}

func diskUsage(path string) (used, total uint64, ok bool) {
	u, err := disk.Usage(path)
	if err != nil {
		return 0, 0, false
	}
	return u.Used, u.Total, true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
