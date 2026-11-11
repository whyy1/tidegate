// Package metrics holds the measurement model of Tidegate: latency
// histograms, per-second snapshots, merging, binary encoding for node
// reports and the JSON view used by reports and the UI.
package metrics

// ErrorKind classifies a failed request. The set is fixed so that snapshots
// from different nodes and versions can be merged by index.
type ErrorKind uint8

const (
	ErrDNS            ErrorKind = iota // name resolution failed
	ErrConnectRefused                  // target actively refused the connection
	ErrConnectTimeout                  // TCP connect did not finish in time
	ErrTLS                             // TLS handshake or certificate failure
	ErrReset                           // connection reset or closed mid-request
	ErrTimeout                         // first byte or total timeout after connect
	ErrHTTP4xx                         // response status 400-499
	ErrHTTP5xx                         // response status 500-599
	ErrAssertion                       // assertion or extraction failed
	ErrLocal                           // load generator resource problem (ports, fds, buffers)
	ErrOther                           // anything else
	NumErrorKinds

	// ErrNone marks a successful request.
	ErrNone ErrorKind = 255
)

var errorKindNames = [NumErrorKinds]string{
	"dns", "connect_refused", "connect_timeout", "tls", "reset", "timeout",
	"http_4xx", "http_5xx", "assertion", "local", "other",
}

func (k ErrorKind) String() string {
	if k < NumErrorKinds {
		return errorKindNames[k]
	}
	if k == ErrNone {
		return "none"
	}
	return "unknown"
}

// ParseErrorKind maps a name produced by String back to its kind.
func ParseErrorKind(s string) (ErrorKind, bool) {
	for i, n := range errorKindNames {
		if n == s {
			return ErrorKind(i), true
		}
	}
	return ErrNone, false
}

// Phase is one part of a request's timeline.
type Phase uint8

const (
	PhaseDNS       Phase = iota // name resolution (only when a lookup happened)
	PhaseConnect                // TCP connect (only for new connections)
	PhaseTLS                    // TLS handshake (only for new TLS connections)
	PhaseFirstByte              // request write start to first response byte
	PhaseDownload               // first response byte to end of body
	NumPhases
)

var phaseNames = [NumPhases]string{"dns", "connect", "tls", "first_byte", "download"}

func (p Phase) String() string {
	if p < NumPhases {
		return phaseNames[p]
	}
	return "unknown"
}

// PhaseMask records which phases a request measured.
type PhaseMask uint8

// Has reports whether phase p is set.
func (m PhaseMask) Has(p Phase) bool { return m&(1<<p) != 0 }

// SampleKind is the category used to rate-limit samples: every error kind
// plus SampleSlow for successful but slow requests.
type SampleKind uint8

// SampleSlow is the sample category for slow requests.
const SampleSlow = SampleKind(NumErrorKinds)

// NumSampleKinds is the number of sample categories.
const NumSampleKinds = int(NumErrorKinds) + 1

func (k SampleKind) String() string {
	if k == SampleSlow {
		return "slow"
	}
	return ErrorKind(k).String()
}

// ParseSampleKind maps a name produced by String back to its kind.
func ParseSampleKind(s string) (SampleKind, bool) {
	if s == "slow" {
		return SampleSlow, true
	}
	k, ok := ParseErrorKind(s)
	return SampleKind(k), ok
}
