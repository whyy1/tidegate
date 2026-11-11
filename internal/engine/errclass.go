package engine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"tidegate/internal/metrics"
)

// phase is where in a request an error happened.
type phase uint8

const (
	phaseDNS phase = iota
	phaseDial
	phaseTLS
	phaseWrite
	phaseRead
)

// hopError tags an error with the request phase it happened in.
type hopError struct {
	phase phase
	err   error
	// noResponse is set for read errors before any response byte arrived.
	noResponse bool
}

func (e *hopError) Error() string { return e.err.Error() }
func (e *hopError) Unwrap() error { return e.err }

// errTLSHandshakeTimeout is returned when the TLS handshake exceeds its timeout.
var errTLSHandshakeTimeout = errors.New("tls handshake timeout")

// classify maps an error to its category. Errors caused by the load
// generator itself (port exhaustion, descriptor limits, buffer shortage)
// become ErrLocal so they are never confused with problems of the target.
func classify(err error) metrics.ErrorKind {
	if err == nil {
		return metrics.ErrNone
	}
	ph := phaseRead
	var he *hopError
	if errors.As(err, &he) {
		ph = he.phase
	}
	var dnsErr *net.DNSError
	if ph == phaseDNS || errors.As(err, &dnsErr) {
		return metrics.ErrDNS
	}
	var opErr *net.OpError
	if ph == phaseRead && errors.As(err, &opErr) && opErr.Op == "dial" {
		ph = phaseDial // dial error surfaced through net/http
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch {
		case isLocalErrno(errno):
			return metrics.ErrLocal
		case isRefusedErrno(errno):
			return metrics.ErrConnectRefused
		case isTimeoutErrno(errno):
			if ph == phaseDial {
				return metrics.ErrConnectTimeout
			}
			return metrics.ErrTimeout
		case isResetErrno(errno):
			if ph == phaseTLS {
				return metrics.ErrTLS
			}
			return metrics.ErrReset
		}
	}
	if isTimeout(err) {
		switch ph {
		case phaseDial:
			return metrics.ErrConnectTimeout
		case phaseTLS:
			return metrics.ErrTLS
		}
		return metrics.ErrTimeout
	}
	if ph == phaseTLS || isTLSError(err) {
		return metrics.ErrTLS
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.EPIPE) ||
		strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "transport connection broken") {
		return metrics.ErrReset
	}
	return metrics.ErrOther
}

func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errTLSHandshakeTimeout) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isTLSError(err error) bool {
	var (
		rh  tls.RecordHeaderError
		al  tls.AlertError
		ver *tls.CertificateVerificationError
		ua  x509.UnknownAuthorityError
		hn  x509.HostnameError
		ci  x509.CertificateInvalidError
	)
	return errors.As(err, &rh) || errors.As(err, &al) || errors.As(err, &ver) ||
		errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci) ||
		strings.HasPrefix(err.Error(), "tls: ")
}
