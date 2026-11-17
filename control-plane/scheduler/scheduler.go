// Package scheduler chooses the Media Node that runs each channel
// (spec section 9.1). It is a pure function of the cluster state so the
// controller can call it for many channels in one reconciliation pass.
package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"nexostream/internal/model"
)

// ErrNoWorker means no node satisfies the constraints.
var ErrNoWorker = errors.New("no eligible worker")

// Costs estimate the CPU percentage a channel adds to a node. They are
// PROVISIONAL placeholders: the real values must come from the Phase 3 and
// Phase 8 benchmarks (CLAUDE.md: no invented capacity numbers). They only
// drive relative placement, never capacity claims.
type Costs struct {
	Passthrough float64
	// TranscodePerMegapixel is multiplied by width*height/1e6 of the profile.
	TranscodePerMegapixel float64
	// GPUOffload divides the transcode cost on NVENC/QSV nodes.
	GPUOffload float64
}

// Options configure the scheduler.
type Options struct {
	MaxCPUPercent float64 // nodes above this are not eligible (default 80)
	Costs         Costs
}

// DefaultOptions returns provisional defaults.
func DefaultOptions() Options {
	return Options{MaxCPUPercent: 80, Costs: Costs{Passthrough: 0.2, TranscodePerMegapixel: 4, GPUOffload: 4}}
}

// Node is a worker plus the load already planned in this pass.
type Node struct {
	model.Worker
	PlannedCPU      float64
	PlannedChannels int
}

// Cluster is the working set of nodes during a reconciliation pass.
type Cluster struct {
	opt   Options
	nodes []*Node
}

// NewCluster builds the working set from the workers and the current
// number of assigned channels per worker.
func NewCluster(workers []model.Worker, assigned map[string]int, opt Options) *Cluster {
	if opt.MaxCPUPercent <= 0 {
		opt.MaxCPUPercent = 80
	}
	c := &Cluster{opt: opt}
	for _, w := range workers {
		n := &Node{Worker: w, PlannedChannels: assigned[w.ID]}
		// Channels assigned but not yet reflected in the CPU sample would be
		// double counted otherwise; the heartbeat count is the reference.
		if extra := assigned[w.ID] - w.Channels; extra > 0 {
			n.PlannedCPU = float64(extra) * opt.Costs.Passthrough
		}
		c.nodes = append(c.nodes, n)
	}
	return c
}

// Cost estimates the CPU percentage a channel adds on node w.
func (c *Cluster) Cost(ch model.Channel, profiles map[string]model.TranscodeProfile, w model.Worker) float64 {
	k := c.opt.Costs
	if ch.Mode != model.ModeTranscode || len(ch.Profiles) == 0 {
		return k.Passthrough
	}
	// An ABR ladder costs one encode per quality.
	cost := 0.0
	for _, id := range ch.Profiles {
		p, ok := profiles[id]
		if !ok {
			continue
		}
		c1 := k.TranscodePerMegapixel * float64(p.Video.Width*p.Video.Height) / 1e6
		enc := p.Encoder
		if enc == model.EncoderNVENC || enc == model.EncoderQSV ||
			(enc == model.EncoderAuto && (w.Capabilities.HasEncoder(model.EncoderNVENC) || w.Capabilities.HasEncoder(model.EncoderQSV))) {
			if k.GPUOffload > 0 {
				c1 /= k.GPUOffload
			}
		}
		cost += c1
	}
	if cost == 0 {
		return k.Passthrough
	}
	return cost
}

// eligible checks the hard constraints.
func eligible(ch model.Channel, profiles map[string]model.TranscodeProfile, w model.Worker) error {
	if w.State != model.WorkerOnline || w.Draining {
		return fmt.Errorf("%s is %s", w.ID, w.State)
	}
	for _, s := range ch.Sources {
		if !w.Capabilities.HasInterface(s.Interface) {
			return fmt.Errorf("%s has no interface %s", w.ID, s.Interface)
		}
	}
	if len(w.Capabilities.Roles) > 0 {
		for _, r := range ch.RolesNeeded() {
			if !slices.Contains(w.Capabilities.Roles, r) {
				return fmt.Errorf("%s does not have the %s role", w.ID, r)
			}
		}
	}
	if ch.Mode == model.ModeTranscode {
		for _, id := range ch.Profiles {
			p, ok := profiles[id]
			if !ok {
				continue
			}
			if p.Encoder == model.EncoderAuto || p.Encoder == "" {
				if len(w.Capabilities.Encoders) == 0 {
					return fmt.Errorf("%s has no encoder", w.ID)
				}
			} else if !w.Capabilities.HasEncoder(p.Encoder) {
				return fmt.Errorf("%s has no %s encoder", w.ID, p.Encoder)
			}
		}
	}
	return nil
}

// Pick chooses the node with the lowest projected CPU (ties: fewer
// channels, then ID) that satisfies the constraints and stays under the CPU
// limit, and records the planned load. exclude skips a node (migration).
func (c *Cluster) Pick(ch model.Channel, profiles map[string]model.TranscodeProfile, exclude string) (string, error) {
	type cand struct {
		n    *Node
		proj float64
		cost float64
	}
	var cands []cand
	var reasons []string
	for _, n := range c.nodes {
		if n.ID == exclude {
			continue
		}
		if err := eligible(ch, profiles, n.Worker); err != nil {
			reasons = append(reasons, err.Error())
			continue
		}
		cost := c.Cost(ch, profiles, n.Worker)
		proj := n.Resources.CPUPercent + n.PlannedCPU + cost
		if proj > c.opt.MaxCPUPercent {
			reasons = append(reasons, fmt.Sprintf("%s would reach %.1f%% CPU", n.ID, proj))
			continue
		}
		cands = append(cands, cand{n, proj, cost})
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("%w for %s: %v", ErrNoWorker, ch.ID, reasons)
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.proj != b.proj {
			return a.proj < b.proj
		}
		if a.n.PlannedChannels != b.n.PlannedChannels {
			return a.n.PlannedChannels < b.n.PlannedChannels
		}
		return a.n.ID < b.n.ID
	})
	best := cands[0]
	best.n.PlannedCPU += best.cost
	best.n.PlannedChannels++
	return best.n.ID, nil
}

// Release removes a channel from a node's planned load (migration source).
func (c *Cluster) Release(workerID string) {
	for _, n := range c.nodes {
		if n.ID == workerID && n.PlannedChannels > 0 {
			n.PlannedChannels--
		}
	}
}
