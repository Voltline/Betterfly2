package grpcClient

import (
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestGetAuthClientRetriesAfterFailedDial(t *testing.T) {
	previous := dialTimeout
	dialTimeout = 200 * time.Millisecond
	t.Cleanup(func() { dialTimeout = previous; CloseConn() })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("AUTH_RPC_ADDR", addr)
	if _, err := GetAuthClient(); err == nil {
		t.Fatal("dial to a closed port succeeded")
	}

	listener, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("port %s was taken before the retry: %v", addr, err)
	}
	server := grpc.NewServer()
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	if rpcClient, err := GetAuthClient(); err != nil || rpcClient == nil {
		t.Fatalf("auth client stayed broken after auth came up: client=%v err=%v", rpcClient, err)
	}
}
