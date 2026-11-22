package cluster

import (
	"maps"
	"sort"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The types in this file mirror cluster.proto field for field. Marshal and
// Unmarshal produce and accept the protobuf wire format of the corresponding
// proto message. Unmarshal resets the receiver first, skips unknown fields,
// and may keep references into its input for []byte fields, so the input
// must not be modified afterwards.

// NodeMessage is the envelope for everything a node sends.
type NodeMessage struct {
	// Body is one of *Hello, *SysStats, *RunSnapshot, *RunFinished or *Pong.
	// It is nil after decoding a body type this version does not know.
	Body NodeBody
}

// NodeBody is implemented by the messages a node can send.
type NodeBody interface {
	message
	nodeField() protowire.Number
}

// ControllerMessage is the envelope for everything the controller sends.
type ControllerMessage struct {
	// Body is one of *Welcome, *Reject, *StartRun, *StopRun, *SetTarget or
	// *Ping. It is nil after decoding a body type this version does not know.
	Body ControllerBody
}

// ControllerBody is implemented by the messages the controller can send.
type ControllerBody interface {
	message
	controllerField() protowire.Number
}

func (*Hello) nodeField() protowire.Number       { return 1 }
func (*SysStats) nodeField() protowire.Number    { return 2 }
func (*RunSnapshot) nodeField() protowire.Number { return 3 }
func (*RunFinished) nodeField() protowire.Number { return 4 }
func (*Pong) nodeField() protowire.Number        { return 5 }

func (*Welcome) controllerField() protowire.Number   { return 1 }
func (*Reject) controllerField() protowire.Number    { return 2 }
func (*StartRun) controllerField() protowire.Number  { return 3 }
func (*StopRun) controllerField() protowire.Number   { return 4 }
func (*SetTarget) controllerField() protowire.Number { return 5 }
func (*Ping) controllerField() protowire.Number      { return 6 }

func (m *NodeMessage) size() int {
	if m.Body == nil {
		return 0
	}
	return sizeMessage(m.Body.nodeField(), m.Body)
}

func (m *NodeMessage) appendTo(b []byte) []byte {
	if m.Body == nil {
		return b
	}
	return appendMessage(b, m.Body.nodeField(), m.Body)
}

func (m *NodeMessage) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			v, _ := m.Body.(*Hello)
			if v == nil {
				v = new(Hello)
			}
			d.message(v)
			m.Body = v
		case 2<<3 | wBytes:
			v, _ := m.Body.(*SysStats)
			if v == nil {
				v = new(SysStats)
			}
			d.message(v)
			m.Body = v
		case 3<<3 | wBytes:
			v, _ := m.Body.(*RunSnapshot)
			if v == nil {
				v = new(RunSnapshot)
			}
			d.message(v)
			m.Body = v
		case 4<<3 | wBytes:
			v, _ := m.Body.(*RunFinished)
			if v == nil {
				v = new(RunFinished)
			}
			d.message(v)
			m.Body = v
		case 5<<3 | wBytes:
			v, _ := m.Body.(*Pong)
			if v == nil {
				v = new(Pong)
			}
			d.message(v)
			m.Body = v
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *NodeMessage) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded NodeMessage into m.
func (m *NodeMessage) Unmarshal(b []byte) error {
	*m = NodeMessage{}
	return m.unmarshal(b)
}

func (m *ControllerMessage) size() int {
	if m.Body == nil {
		return 0
	}
	return sizeMessage(m.Body.controllerField(), m.Body)
}

func (m *ControllerMessage) appendTo(b []byte) []byte {
	if m.Body == nil {
		return b
	}
	return appendMessage(b, m.Body.controllerField(), m.Body)
}

func (m *ControllerMessage) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			v, _ := m.Body.(*Welcome)
			if v == nil {
				v = new(Welcome)
			}
			d.message(v)
			m.Body = v
		case 2<<3 | wBytes:
			v, _ := m.Body.(*Reject)
			if v == nil {
				v = new(Reject)
			}
			d.message(v)
			m.Body = v
		case 3<<3 | wBytes:
			v, _ := m.Body.(*StartRun)
			if v == nil {
				v = new(StartRun)
			}
			d.message(v)
			m.Body = v
		case 4<<3 | wBytes:
			v, _ := m.Body.(*StopRun)
			if v == nil {
				v = new(StopRun)
			}
			d.message(v)
			m.Body = v
		case 5<<3 | wBytes:
			v, _ := m.Body.(*SetTarget)
			if v == nil {
				v = new(SetTarget)
			}
			d.message(v)
			m.Body = v
		case 6<<3 | wBytes:
			v, _ := m.Body.(*Ping)
			if v == nil {
				v = new(Ping)
			}
			d.message(v)
			m.Body = v
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *ControllerMessage) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded ControllerMessage into m.
func (m *ControllerMessage) Unmarshal(b []byte) error {
	*m = ControllerMessage{}
	return m.unmarshal(b)
}

// Hello is the first message of every connection.
type Hello struct {
	Token string
	// NodeID is the ID from an earlier Welcome, sent again when reconnecting
	// so the controller can keep it. Empty on the first connection.
	NodeID      string
	Name        string
	Version     string
	OS          string
	Arch        string
	CPUCores    int
	MemoryBytes uint64
	Interfaces  []NetInterface
	Labels      map[string]string
}

type labelEntry struct {
	key   string
	value string
}

func (m *labelEntry) size() int {
	return sizeString(1, m.key) + sizeString(2, m.value)
}

func (m *labelEntry) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.key)
	return appendString(b, 2, m.value)
}

func (m *labelEntry) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.key = d.string()
		case 2<<3 | wBytes:
			m.value = d.string()
		default:
			d.skip()
		}
	}
	return d.err
}

func sortedHelloLabels(labels map[string]string) []labelEntry {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]labelEntry, 0, len(keys))
	for _, k := range keys {
		out = append(out, labelEntry{key: k, value: labels[k]})
	}
	return out
}

// NetInterface describes one network interface of a node.
type NetInterface struct {
	Name string
	// LinkSpeedBps is the negotiated link speed in bits per second, 0 when
	// unknown.
	LinkSpeedBps uint64
}

func (m *Hello) size() int {
	n := sizeString(1, m.Token) + sizeString(2, m.NodeID) + sizeString(3, m.Name) +
		sizeString(4, m.Version) + sizeString(5, m.OS) + sizeString(6, m.Arch) +
		sizeInt32(7, int32(m.CPUCores)) + sizeUint(8, m.MemoryBytes)
	for i := range m.Interfaces {
		n += sizeMessage(9, &m.Interfaces[i])
	}
	for _, lb := range sortedHelloLabels(m.Labels) {
		n += sizeMessage(10, &lb)
	}
	return n
}

func (m *Hello) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.Token)
	b = appendString(b, 2, m.NodeID)
	b = appendString(b, 3, m.Name)
	b = appendString(b, 4, m.Version)
	b = appendString(b, 5, m.OS)
	b = appendString(b, 6, m.Arch)
	b = appendInt32(b, 7, int32(m.CPUCores))
	b = appendUint(b, 8, m.MemoryBytes)
	for i := range m.Interfaces {
		b = appendMessage(b, 9, &m.Interfaces[i])
	}
	for _, lb := range sortedHelloLabels(m.Labels) {
		b = appendMessage(b, 10, &lb)
	}
	return b
}

func (m *Hello) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.Token = d.string()
		case 2<<3 | wBytes:
			m.NodeID = d.string()
		case 3<<3 | wBytes:
			m.Name = d.string()
		case 4<<3 | wBytes:
			m.Version = d.string()
		case 5<<3 | wBytes:
			m.OS = d.string()
		case 6<<3 | wBytes:
			m.Arch = d.string()
		case 7<<3 | wVarint:
			m.CPUCores = int(int32(d.varint()))
		case 8<<3 | wVarint:
			m.MemoryBytes = d.varint()
		case 9<<3 | wBytes:
			var v NetInterface
			d.message(&v)
			m.Interfaces = append(m.Interfaces, v)
		case 10<<3 | wBytes:
			var lb labelEntry
			d.message(&lb)
			if m.Labels == nil {
				m.Labels = make(map[string]string)
			}
			m.Labels[lb.key] = lb.value
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *Hello) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded Hello into m.
func (m *Hello) Unmarshal(b []byte) error {
	*m = Hello{}
	return m.unmarshal(b)
}

func (m *NetInterface) size() int {
	return sizeString(1, m.Name) + sizeUint(2, m.LinkSpeedBps)
}

func (m *NetInterface) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.Name)
	return appendUint(b, 2, m.LinkSpeedBps)
}

func (m *NetInterface) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.Name = d.string()
		case 2<<3 | wVarint:
			m.LinkSpeedBps = d.varint()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *NetInterface) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded NetInterface into m.
func (m *NetInterface) Unmarshal(b []byte) error {
	*m = NetInterface{}
	return m.unmarshal(b)
}

// SysStats carries one sample of machine statistics as an opaque JSON
// document produced by the node.
type SysStats struct {
	Time time.Time
	JSON []byte
}

func (m *SysStats) size() int {
	return sizeInt64(1, unixNano(m.Time)) + sizeBytes(2, m.JSON)
}

func (m *SysStats) appendTo(b []byte) []byte {
	b = appendInt64(b, 1, unixNano(m.Time))
	return appendBytes(b, 2, m.JSON)
}

func (m *SysStats) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wVarint:
			m.Time = fromUnixNano(int64(d.varint()))
		case 2<<3 | wBytes:
			m.JSON = d.bytes()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *SysStats) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded SysStats into m.
func (m *SysStats) Unmarshal(b []byte) error {
	*m = SysStats{}
	return m.unmarshal(b)
}

// RunSnapshot is one per-second metrics snapshot of a run. Snapshot holds
// the metrics binary encoding (metrics.Snapshot.MarshalBinary).
type RunSnapshot struct {
	RunID    string
	Seq      int64
	Snapshot []byte
}

func (m *RunSnapshot) size() int {
	return sizeString(1, m.RunID) + sizeInt64(2, m.Seq) + sizeBytes(3, m.Snapshot)
}

func (m *RunSnapshot) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.RunID)
	b = appendInt64(b, 2, m.Seq)
	return appendBytes(b, 3, m.Snapshot)
}

func (m *RunSnapshot) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.RunID = d.string()
		case 2<<3 | wVarint:
			m.Seq = int64(d.varint())
		case 3<<3 | wBytes:
			m.Snapshot = d.bytes()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *RunSnapshot) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded RunSnapshot into m.
func (m *RunSnapshot) Unmarshal(b []byte) error {
	*m = RunSnapshot{}
	return m.unmarshal(b)
}

// RunFinished reports the end of a run on a node. Summary holds the
// whole-run snapshot in the metrics binary encoding; Error is empty when the
// run succeeded.
type RunFinished struct {
	RunID   string
	Summary []byte
	Error   string
}

func (m *RunFinished) size() int {
	return sizeString(1, m.RunID) + sizeBytes(2, m.Summary) + sizeString(3, m.Error)
}

func (m *RunFinished) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.RunID)
	b = appendBytes(b, 2, m.Summary)
	return appendString(b, 3, m.Error)
}

func (m *RunFinished) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.RunID = d.string()
		case 2<<3 | wBytes:
			m.Summary = d.bytes()
		case 3<<3 | wBytes:
			m.Error = d.string()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *RunFinished) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded RunFinished into m.
func (m *RunFinished) Unmarshal(b []byte) error {
	*m = RunFinished{}
	return m.unmarshal(b)
}

// Pong answers a Ping. PingTime echoes Ping.Time; NodeTime is the node
// clock when the Pong was created.
type Pong struct {
	Nonce    uint64
	PingTime time.Time
	NodeTime time.Time
}

func (m *Pong) size() int {
	return sizeUint(1, m.Nonce) + sizeInt64(2, unixNano(m.PingTime)) + sizeInt64(3, unixNano(m.NodeTime))
}

func (m *Pong) appendTo(b []byte) []byte {
	b = appendUint(b, 1, m.Nonce)
	b = appendInt64(b, 2, unixNano(m.PingTime))
	return appendInt64(b, 3, unixNano(m.NodeTime))
}

func (m *Pong) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wVarint:
			m.Nonce = d.varint()
		case 2<<3 | wVarint:
			m.PingTime = fromUnixNano(int64(d.varint()))
		case 3<<3 | wVarint:
			m.NodeTime = fromUnixNano(int64(d.varint()))
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *Pong) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded Pong into m.
func (m *Pong) Unmarshal(b []byte) error {
	*m = Pong{}
	return m.unmarshal(b)
}

// Welcome accepts a node.
type Welcome struct {
	NodeID string
	// ServerTime is the controller clock when the Welcome was sent.
	ServerTime    time.Time
	ServerVersion string
	// PingInterval is how often the controller sends Ping. It travels in
	// whole milliseconds.
	PingInterval time.Duration
}

func (m *Welcome) size() int {
	return sizeString(1, m.NodeID) + sizeInt64(2, unixNano(m.ServerTime)) +
		sizeString(3, m.ServerVersion) + sizeInt64(4, m.PingInterval.Milliseconds())
}

func (m *Welcome) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.NodeID)
	b = appendInt64(b, 2, unixNano(m.ServerTime))
	b = appendString(b, 3, m.ServerVersion)
	return appendInt64(b, 4, m.PingInterval.Milliseconds())
}

func (m *Welcome) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.NodeID = d.string()
		case 2<<3 | wVarint:
			m.ServerTime = fromUnixNano(int64(d.varint()))
		case 3<<3 | wBytes:
			m.ServerVersion = d.string()
		case 4<<3 | wVarint:
			m.PingInterval = time.Duration(int64(d.varint())) * time.Millisecond
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *Welcome) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded Welcome into m.
func (m *Welcome) Unmarshal(b []byte) error {
	*m = Welcome{}
	return m.unmarshal(b)
}

// RejectReason tells a node why it was refused.
type RejectReason int32

const (
	RejectUnspecified     RejectReason = 0
	RejectBadToken        RejectReason = 1
	RejectVersionMismatch RejectReason = 2
	RejectProtocol        RejectReason = 3
)

func (r RejectReason) String() string {
	switch r {
	case RejectUnspecified:
		return "REJECT_REASON_UNSPECIFIED"
	case RejectBadToken:
		return "REJECT_REASON_BAD_TOKEN"
	case RejectVersionMismatch:
		return "REJECT_REASON_VERSION_MISMATCH"
	case RejectProtocol:
		return "REJECT_REASON_PROTOCOL"
	}
	return "RejectReason(" + strconv.Itoa(int(r)) + ")"
}

// Reject refuses a connection. The controller closes the stream after it.
type Reject struct {
	Reason RejectReason
	// Message explains the refusal in Simplified Chinese.
	Message string
	// ServerVersion is only set for RejectVersionMismatch.
	ServerVersion string
}

func (m *Reject) size() int {
	return sizeInt32(1, int32(m.Reason)) + sizeString(2, m.Message) + sizeString(3, m.ServerVersion)
}

func (m *Reject) appendTo(b []byte) []byte {
	b = appendInt32(b, 1, int32(m.Reason))
	b = appendString(b, 2, m.Message)
	return appendString(b, 3, m.ServerVersion)
}

func (m *Reject) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wVarint:
			m.Reason = RejectReason(int32(d.varint()))
		case 2<<3 | wBytes:
			m.Message = d.string()
		case 3<<3 | wBytes:
			m.ServerVersion = d.string()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *Reject) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded Reject into m.
func (m *Reject) Unmarshal(b []byte) error {
	*m = Reject{}
	return m.unmarshal(b)
}

// DataFile is one data file of a run, sent in full.
type DataFile struct {
	Name    string
	Content []byte
}

func (m *DataFile) size() int {
	return sizeString(1, m.Name) + sizeBytes(2, m.Content)
}

func (m *DataFile) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.Name)
	return appendBytes(b, 2, m.Content)
}

func (m *DataFile) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.Name = d.string()
		case 2<<3 | wBytes:
			m.Content = d.bytes()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *DataFile) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded DataFile into m.
func (m *DataFile) Unmarshal(b []byte) error {
	*m = DataFile{}
	return m.unmarshal(b)
}

// StartRun asks a node to start its part of a run.
type StartRun struct {
	RunID string
	// Scenario is the scenario definition in JSON.
	Scenario  []byte
	DataFiles []DataFile
	// LoadShare is the fraction of the plan's load this node runs, in
	// (0, 1]. It maps to engine.Options.LoadShare.
	LoadShare float64
	// The node uses data row i when i % SliceCount == SliceIndex. They map to
	// engine.Options.DataSlice.
	SliceIndex int
	SliceCount int
	// StartTime is the common start time on the controller clock. Nodes
	// convert it with their clock offset (see Client.ControllerToLocal).
	StartTime time.Time
}

func (m *StartRun) size() int {
	n := sizeString(1, m.RunID) + sizeBytes(2, m.Scenario)
	for i := range m.DataFiles {
		n += sizeMessage(3, &m.DataFiles[i])
	}
	return n + sizeDouble(4, m.LoadShare) + sizeInt32(5, int32(m.SliceIndex)) +
		sizeInt32(6, int32(m.SliceCount)) + sizeInt64(7, unixNano(m.StartTime))
}

func (m *StartRun) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.RunID)
	b = appendBytes(b, 2, m.Scenario)
	for i := range m.DataFiles {
		b = appendMessage(b, 3, &m.DataFiles[i])
	}
	b = appendDouble(b, 4, m.LoadShare)
	b = appendInt32(b, 5, int32(m.SliceIndex))
	b = appendInt32(b, 6, int32(m.SliceCount))
	return appendInt64(b, 7, unixNano(m.StartTime))
}

func (m *StartRun) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.RunID = d.string()
		case 2<<3 | wBytes:
			m.Scenario = d.bytes()
		case 3<<3 | wBytes:
			var v DataFile
			d.message(&v)
			m.DataFiles = append(m.DataFiles, v)
		case 4<<3 | wFixed64:
			m.LoadShare = d.double()
		case 5<<3 | wVarint:
			m.SliceIndex = int(int32(d.varint()))
		case 6<<3 | wVarint:
			m.SliceCount = int(int32(d.varint()))
		case 7<<3 | wVarint:
			m.StartTime = fromUnixNano(int64(d.varint()))
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *StartRun) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded StartRun into m.
func (m *StartRun) Unmarshal(b []byte) error {
	*m = StartRun{}
	return m.unmarshal(b)
}

// StopMode selects how a run stops.
type StopMode int32

const (
	// StopGraceful lets in-flight iterations finish.
	StopGraceful StopMode = 0
	// StopAbort cancels in-flight requests immediately.
	StopAbort StopMode = 1
)

func (s StopMode) String() string {
	switch s {
	case StopGraceful:
		return "STOP_MODE_GRACEFUL"
	case StopAbort:
		return "STOP_MODE_ABORT"
	}
	return "StopMode(" + strconv.Itoa(int(s)) + ")"
}

// StopRun asks a node to stop a run.
type StopRun struct {
	RunID string
	Mode  StopMode
}

func (m *StopRun) size() int {
	return sizeString(1, m.RunID) + sizeInt32(2, int32(m.Mode))
}

func (m *StopRun) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.RunID)
	return appendInt32(b, 2, int32(m.Mode))
}

func (m *StopRun) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.RunID = d.string()
		case 2<<3 | wVarint:
			m.Mode = StopMode(int32(d.varint()))
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *StopRun) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded StopRun into m.
func (m *StopRun) Unmarshal(b []byte) error {
	*m = StopRun{}
	return m.unmarshal(b)
}

// SetTarget changes the load target or load share of a running run.
type SetTarget struct {
	RunID     string
	Value     float64
	LoadShare float64
}

func (m *SetTarget) size() int {
	return sizeString(1, m.RunID) + sizeDouble(2, m.Value) + sizeDouble(3, m.LoadShare)
}

func (m *SetTarget) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.RunID)
	b = appendDouble(b, 2, m.Value)
	return appendDouble(b, 3, m.LoadShare)
}

func (m *SetTarget) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.RunID = d.string()
		case 2<<3 | wFixed64:
			m.Value = d.double()
		case 3<<3 | wFixed64:
			m.LoadShare = d.double()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *SetTarget) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded SetTarget into m.
func (m *SetTarget) Unmarshal(b []byte) error {
	*m = SetTarget{}
	return m.unmarshal(b)
}

// EnrollRequest exchanges a cluster join token and PKCS#10 CSR for a signed mTLS certificate.
type EnrollRequest struct {
	Token  string
	CSRDER []byte
	Name   string
}

func (m *EnrollRequest) size() int {
	return sizeString(1, m.Token) + sizeBytes(2, m.CSRDER) + sizeString(3, m.Name)
}

func (m *EnrollRequest) appendTo(b []byte) []byte {
	b = appendString(b, 1, m.Token)
	b = appendBytes(b, 2, m.CSRDER)
	return appendString(b, 3, m.Name)
}

func (m *EnrollRequest) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.Token = d.string()
		case 2<<3 | wBytes:
			m.CSRDER = d.bytes()
		case 3<<3 | wBytes:
			m.Name = d.string()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *EnrollRequest) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded EnrollRequest into m.
func (m *EnrollRequest) Unmarshal(b []byte) error {
	*m = EnrollRequest{}
	return m.unmarshal(b)
}

// EnrollResponse returns the signed client certificate and Cluster CA certificate.
type EnrollResponse struct {
	ClientCertPEM []byte
	CACertPEM     []byte
	RejectMessage string
}

func (m *EnrollResponse) size() int {
	return sizeBytes(1, m.ClientCertPEM) + sizeBytes(2, m.CACertPEM) + sizeString(3, m.RejectMessage)
}

func (m *EnrollResponse) appendTo(b []byte) []byte {
	b = appendBytes(b, 1, m.ClientCertPEM)
	b = appendBytes(b, 2, m.CACertPEM)
	return appendString(b, 3, m.RejectMessage)
}

func (m *EnrollResponse) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wBytes:
			m.ClientCertPEM = d.bytes()
		case 2<<3 | wBytes:
			m.CACertPEM = d.bytes()
		case 3<<3 | wBytes:
			m.RejectMessage = d.string()
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *EnrollResponse) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded EnrollResponse into m.
func (m *EnrollResponse) Unmarshal(b []byte) error {
	*m = EnrollResponse{}
	return m.unmarshal(b)
}

var _ = maps.Clone[map[string]string, string, string]

// Ping is the controller's application level heartbeat.
type Ping struct {
	Nonce uint64
	Time  time.Time
}

func (m *Ping) size() int {
	return sizeUint(1, m.Nonce) + sizeInt64(2, unixNano(m.Time))
}

func (m *Ping) appendTo(b []byte) []byte {
	b = appendUint(b, 1, m.Nonce)
	return appendInt64(b, 2, unixNano(m.Time))
}

func (m *Ping) unmarshal(b []byte) error {
	d := decoder{b: b}
	for d.next() {
		switch d.key {
		case 1<<3 | wVarint:
			m.Nonce = d.varint()
		case 2<<3 | wVarint:
			m.Time = fromUnixNano(int64(d.varint()))
		default:
			d.skip()
		}
	}
	return d.err
}

// Marshal returns the protobuf encoding of m.
func (m *Ping) Marshal() ([]byte, error) { return marshal(m) }

// Unmarshal decodes a protobuf encoded Ping into m.
func (m *Ping) Unmarshal(b []byte) error {
	*m = Ping{}
	return m.unmarshal(b)
}
