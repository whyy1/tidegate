package grpcproto

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"

	"tidegate/internal/testserver"
)

func TestParseProtoTextAndCodec(t *testing.T) {
	files := map[string][]byte{
		"common.proto": []byte(`
syntax = "proto3";
package demo;
enum Status {
  UNKNOWN = 0;
  ACTIVE = 1;
}
message Meta {
  string trace_id = 1;
  Status status = 2;
}
`),
		"user.proto": []byte(`
syntax = "proto3";
package demo;
import "common.proto";
import "google/protobuf/timestamp.proto";

message GetUserRequest {
  int64 user_id = 1;
  repeated string tags = 2;
  map<string, string> attrs = 3;
}
message GetUserReply {
  message Profile {
    string nickname = 1;
  }
  int64 user_id = 1;
  Profile profile = 2;
  Meta meta = 3;
  google.protobuf.Timestamp created_at = 4;
}
service UserService {
  rpc GetUser (GetUserRequest) returns (GetUserReply);
}
`),
	}

	reg, err := ParseProtoFiles("user.proto", files)
	if err != nil {
		t.Fatal(err)
	}
	cat := BuildCatalog(reg)
	if len(cat.Services) != 1 || cat.Services[0].Name != "demo.UserService" || len(cat.Services[0].Methods) != 1 {
		t.Fatalf("unexpected catalog: %+v", cat)
	}
	m := cat.Services[0].Methods[0]
	if m.FullMethod != "/demo.UserService/GetUser" || m.SampleJSON == "" {
		t.Fatalf("unexpected method info: %+v", m)
	}

	codec, err := NewMethodCodec(reg, m.FullMethod)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := codec.EncodeJSON([]byte(`{"user_id": 42, "tags": ["vip"], "attrs": {"env": "prod"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) == 0 {
		t.Fatal("expected non-empty protobuf wire bytes")
	}
	// Verify round-trip via out codec with simple self-test
	reqCodec, err := NewMethodCodec(reg, m.FullMethod)
	if err != nil {
		t.Fatal(err)
	}
	reqCodec.OutDesc = reqCodec.InDesc
	rtJSON, err := reqCodec.DecodeToJSON(wire)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(rtJSON, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["user_id"] != "42" { // protojson encodes int64 as string
		t.Fatalf("expected user_id='42', got %#v (%s)", parsed["user_id"], rtJSON)
	}

	// Test binary FileDescriptorSet loading
	fd, err := reg.FindFileByPath("user.proto")
	if err != nil {
		t.Fatal(err)
	}
	commonFD, err := reg.FindFileByPath("common.proto")
	if err != nil {
		t.Fatal(err)
	}
	tsFD, _ := reg.FindFileByPath("google/protobuf/timestamp.proto")
	fdsBytes, err := proto.Marshal(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(tsFD),
			protodesc.ToFileDescriptorProto(commonFD),
			protodesc.ToFileDescriptorProto(fd),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	binReg, err := ParseProtoFiles("bundle.desc", map[string][]byte{"bundle.desc": fdsBytes})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMethodCodec(binReg, "/demo.UserService/GetUser"); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverViaReflectionAgainstTestServer(t *testing.T) {
	gs, err := testserver.ListenGRPC("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	gs.Start()
	defer gs.Close()

	dialChecked := false
	cat, files, err := DiscoverViaReflection(context.Background(), ReflectOptions{
		Target: gs.Addr(),
		DialCheck: func(host string, ip net.IP) error {
			dialChecked = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dialChecked {
		t.Fatal("expected DialCheck callback to run for SSRF enforcement")
	}
	found := false
	for _, s := range cat.Services {
		if s.Name == "tidegate.test.EchoService" && len(s.Methods) == 1 && s.Methods[0].Name == "UnaryEcho" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected tidegate.test.EchoService.UnaryEcho, got %+v", cat.Services)
	}
	codec, err := NewMethodCodec(files, "/tidegate.test.EchoService/UnaryEcho")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := codec.EncodeJSON([]byte(`{"message":"hello","count":7}`))
	if err != nil || len(wire) == 0 {
		t.Fatalf("EncodeJSON err=%v len=%d", err, len(wire))
	}
}
