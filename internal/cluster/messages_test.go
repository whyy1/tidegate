package cluster

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestNodeAndControllerMessagesRoundTrip(t *testing.T) {
	ts := time.Unix(1730000000, 123456789)
	nodeMsgs := []NodeBody{
		&Hello{
			Token:       "tok-123",
			NodeID:      "node-abc",
			Name:        "worker-1",
			Version:     "0.1.0",
			OS:          "linux",
			Arch:        "amd64",
			CPUCores:    16,
			MemoryBytes: 64 << 30,
			Interfaces: []NetInterface{
				{Name: "eth0", LinkSpeedBps: 10_000_000_000},
				{Name: "lo", LinkSpeedBps: 0},
			},
			Labels: map[string]string{"zone": "ap-east", "tier": "high"},
		},
		&Hello{}, // empty oneof member must still decode as *Hello
		&SysStats{Time: ts, JSON: []byte(`{"cpu":0.25}`)},
		&RunSnapshot{RunID: "run-1", Seq: 42, Snapshot: []byte{1, 2, 3, 4}},
		&RunFinished{RunID: "run-1", Summary: []byte{9, 8}, Error: "部分失败"},
		&Pong{Nonce: 99, PingTime: ts, NodeTime: ts.Add(5 * time.Millisecond)},
	}
	for i, body := range nodeMsgs {
		want := NodeMessage{Body: body}
		raw, err := want.Marshal()
		if err != nil {
			t.Fatalf("node case %d marshal: %v", i, err)
		}
		var got NodeMessage
		if err := got.Unmarshal(raw); err != nil {
			t.Fatalf("node case %d unmarshal: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("node case %d mismatch:\ngot:  %#v\nwant: %#v", i, got, want)
		}
	}

	ctrlMsgs := []ControllerBody{
		&Welcome{
			NodeID:        "node-1",
			ServerTime:    ts,
			ServerVersion: "0.1.0",
			PingInterval:  5 * time.Second,
		},
		&Reject{
			Reason:        RejectVersionMismatch,
			Message:       "版本不一致",
			ServerVersion: "0.2.0",
		},
		&StartRun{
			RunID:      "run-9",
			Scenario:   []byte(`{"version":1}`),
			DataFiles:  []DataFile{{Name: "users.csv", Content: []byte("a,b\n1,2\n")}},
			LoadShare:  0.5,
			SliceIndex: 1,
			SliceCount: 2,
			StartTime:  ts.Add(time.Second),
		},
		&StopRun{RunID: "run-9", Mode: StopAbort},
		&SetTarget{RunID: "run-9", Value: 250.5, LoadShare: 0.75},
		&Ping{Nonce: 7, Time: ts},
		&Ping{},
	}
	for i, body := range ctrlMsgs {
		want := ControllerMessage{Body: body}
		raw, err := want.Marshal()
		if err != nil {
			t.Fatalf("ctrl case %d marshal: %v", i, err)
		}
		var got ControllerMessage
		if err := got.Unmarshal(raw); err != nil {
			t.Fatalf("ctrl case %d unmarshal: %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ctrl case %d mismatch:\ngot:  %#v\nwant: %#v", i, got, want)
		}
	}
}

func TestWireSkipsUnknownFieldsAndRejectsTruncated(t *testing.T) {
	// Append an unknown varint field #99 and unknown bytes field #100 onto a Ping.
	m := NodeMessage{Body: &Pong{Nonce: 12, PingTime: time.Unix(1700000000, 0)}}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	extra := protowire.AppendTag(nil, 99, protowire.VarintType)
	extra = protowire.AppendVarint(extra, 123456)
	extra = protowire.AppendTag(extra, 100, protowire.BytesType)
	extra = protowire.AppendString(extra, "future-field")
	withUnknown := append(bytes.Clone(raw), extra...)

	var got NodeMessage
	if err := got.Unmarshal(withUnknown); err != nil {
		t.Fatalf("unmarshal with unknown fields: %v", err)
	}
	p, ok := got.Body.(*Pong)
	if !ok || p.Nonce != 12 {
		t.Fatalf("got = %#v", got.Body)
	}

	// Unknown top-level envelope field should decode cleanly with nil Body.
	var empty NodeMessage
	if err := empty.Unmarshal(extra); err != nil || empty.Body != nil {
		t.Fatalf("unknown envelope: got=(%#v, %v)", empty.Body, err)
	}

	// Truncated message must error.
	if err := got.Unmarshal(raw[:len(raw)-2]); err == nil {
		t.Fatal("expected error for truncated wire bytes")
	}

	// Non-UTF8 string is sanitized rather than failing marshal.
	bad := &Reject{Reason: RejectBadToken, Message: string([]byte{0xff, 0xfe, 'a'})}
	b, err := bad.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var dec Reject
	if err := dec.Unmarshal(b); err != nil {
		t.Fatal(err)
	}
	if len(dec.Message) == 0 {
		t.Fatal("sanitized message became empty")
	}
}

func TestMessageSizeLimitAndTokenEqual(t *testing.T) {
	if _, err := encodeNode(&SysStats{JSON: make([]byte, 1024)}, 512); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("encodeNode oversized: got %v, want ErrMessageTooLarge", err)
	}
	if _, err := encodeController(&StartRun{RunID: "r", Scenario: make([]byte, 1024)}, 512); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("encodeController oversized: got %v, want ErrMessageTooLarge", err)
	}
	if !TokenEqual("secret-token", "secret-token") {
		t.Fatal("TokenEqual false for equal tokens")
	}
	if TokenEqual("secret-token", "secret-token2") || TokenEqual("secret-token", "") {
		t.Fatal("TokenEqual true for unequal tokens")
	}

	erWant := EnrollRequest{Token: "tgj_abc", CSRDER: []byte{1, 2, 3}, Name: "edge-1"}
	erRaw, err := erWant.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var erGot EnrollRequest
	if err := erGot.Unmarshal(erRaw); err != nil || !reflect.DeepEqual(erGot, erWant) {
		t.Fatalf("EnrollRequest round-trip got=(%+v, %v)", erGot, err)
	}

	respWant := EnrollResponse{ClientCertPEM: []byte("CERT"), CACertPEM: []byte("CA"), RejectMessage: "denied"}
	respRaw, err := respWant.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var respGot EnrollResponse
	if err := respGot.Unmarshal(respRaw); err != nil || !reflect.DeepEqual(respGot, respWant) {
		t.Fatalf("EnrollResponse round-trip got=(%+v, %v)", respGot, err)
	}
}
