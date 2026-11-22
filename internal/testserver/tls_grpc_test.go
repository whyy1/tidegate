package testserver

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
)

func TestListenTLSAndH2(t *testing.T) {
	tlsSrv, err := ListenTLS("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsSrv.Start()
	defer tlsSrv.Close()

	cl := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := cl.Get(tlsSrv.URL() + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("TLS /ping status = %d", resp.StatusCode)
	}

	h2Srv, err := ListenH2("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h2Srv.Start()
	defer h2Srv.Close()

	h2cl := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}},
			ForceAttemptHTTP2: true,
		},
	}
	resp2, err := h2cl.Get(h2Srv.URL() + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 || resp2.ProtoMajor != 2 {
		t.Fatalf("H2 /ping status=%d proto=%s", resp2.StatusCode, resp2.Proto)
	}
}

func TestListenGRPCWithReflection(t *testing.T) {
	gs, err := ListenGRPC("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	gs.Start()
	defer gs.Close()

	cc, err := grpc.NewClient(gs.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := grpc_reflection_v1.NewServerReflectionClient(cc).ServerReflectionInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&grpc_reflection_v1.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: "*"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	svcs := res.GetListServicesResponse().GetService()
	found := false
	for _, s := range svcs {
		if s.GetName() == "tidegate.test.EchoService" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected tidegate.test.EchoService in reflection services, got %+v", svcs)
	}
}
