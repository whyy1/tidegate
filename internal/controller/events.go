// Package controller orchestrates Tidegate projects, interactive API and
// scenario debugging, cluster load test execution, metric aggregation, and
// load-generator bottleneck alerts.
package controller

import (
	"encoding/json"
	"sync"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/store"
	"tidegate/internal/sysmon"
)

// Event types pushed over the controller pub/sub hub and WebSocket API.
const (
	EventSnapshot  = "snapshot"
	EventSys       = "sys"
	EventRunStatus = "run_status"
	EventAlert     = "alert"
	EventAgents    = "agents"
)

// AgentInterface describes a network interface on a connected node.
type AgentInterface struct {
	Name         string `json:"name"`
	LinkSpeedBps uint64 `json:"link_speed_bps"`
}

// AgentView is the API and WebSocket view of a connected load generator node.
type AgentView struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Version       string           `json:"version"`
	OS            string           `json:"os"`
	Arch          string           `json:"arch"`
	CPUCores      int              `json:"cpu_cores"`
	MemoryBytes   uint64           `json:"memory_bytes"`
	Interfaces    []AgentInterface `json:"interfaces,omitempty"`
	RemoteAddr    string           `json:"remote_addr,omitempty"`
	ConnectedAt   time.Time        `json:"connected_at"`
	LastSeen      time.Time        `json:"last_seen"`
	RTTMs         float64          `json:"rtt_ms"`
	ClockOffsetMs float64          `json:"clock_offset_ms"`
	BandwidthBps  int64            `json:"bandwidth_bps"`
	Stats         *sysmon.Stats    `json:"stats,omitempty"`
}

// Event is one real-time notification emitted by the controller.
type Event struct {
	Type     string                `json:"type"`
	RunID    string                `json:"run_id,omitempty"`
	AgentID  string                `json:"agent_id,omitempty"`
	Status   store.RunStatus       `json:"status,omitempty"`
	Error    string                `json:"error,omitempty"`
	Snapshot *metrics.SnapshotView `json:"snapshot,omitempty"`
	Sys      json.RawMessage       `json:"sys,omitempty"`
	Alert    *store.Alert          `json:"alert,omitempty"`
	Agents   []AgentView           `json:"agents,omitempty"`
}

const subscriberQueueSize = 256

// Subscriber receives controller events until Close is called.
type Subscriber struct {
	hub    *eventHub
	ch     chan Event
	mu     sync.Mutex
	closed bool
	runs   map[string]bool
}

// C returns the event channel. It closes when the subscriber or hub closes.
func (s *Subscriber) C() <-chan Event { return s.ch }

// SubscribeRun subscribes to events of runID (or "*" for all runs).
func (s *Subscriber) SubscribeRun(runID string) {
	if runID == "" {
		return
	}
	s.mu.Lock()
	s.runs[runID] = true
	s.mu.Unlock()
}

// UnsubscribeRun removes runID from the subscriber's run filter.
func (s *Subscriber) UnsubscribeRun(runID string) {
	s.mu.Lock()
	delete(s.runs, runID)
	s.mu.Unlock()
}

func (s *Subscriber) wants(runID string) bool {
	if runID == "" {
		return true
	}
	s.mu.Lock()
	ok := s.runs["*"] || s.runs[runID]
	s.mu.Unlock()
	return ok
}

// Close removes the subscriber from its hub and closes C.
func (s *Subscriber) Close() {
	s.hub.remove(s)
}

type eventHub struct {
	mu     sync.Mutex
	closed bool
	subs   map[*Subscriber]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subs: make(map[*Subscriber]struct{})}
}

func (h *eventHub) subscribe() *Subscriber {
	sub := &Subscriber{
		hub:  h,
		ch:   make(chan Event, subscriberQueueSize),
		runs: make(map[string]bool),
	}
	h.mu.Lock()
	if h.closed {
		sub.closed = true
		close(sub.ch)
	} else {
		h.subs[sub] = struct{}{}
	}
	h.mu.Unlock()
	return sub
}

func (h *eventHub) remove(sub *Subscriber) {
	h.mu.Lock()
	delete(h.subs, sub)
	h.mu.Unlock()

	sub.mu.Lock()
	if !sub.closed {
		sub.closed = true
		close(sub.ch)
	}
	sub.mu.Unlock()
}

func (h *eventHub) closeAll() {
	h.mu.Lock()
	h.closed = true
	subs := make([]*Subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	clear(h.subs)
	h.mu.Unlock()

	for _, s := range subs {
		s.mu.Lock()
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
		s.mu.Unlock()
	}
}

func (h *eventHub) publish(ev Event) {
	h.mu.Lock()
	if h.closed || len(h.subs) == 0 {
		h.mu.Unlock()
		return
	}
	subs := make([]*Subscriber, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	important := ev.Type == EventRunStatus || ev.Type == EventAlert
	for _, s := range subs {
		if !s.wants(ev.RunID) {
			continue
		}
		s.mu.Lock()
		if !s.closed {
			select {
			case s.ch <- ev:
			default:
				if important {
					// Evict one older queued item so critical status/alert updates fit.
					select {
					case <-s.ch:
					default:
					}
					select {
					case s.ch <- ev:
					default:
					}
				}
			}
		}
		s.mu.Unlock()
	}
}
