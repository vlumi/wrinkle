package services

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/vlumi/wrinkle/backends/go/internal/ids"
	backendv1 "github.com/vlumi/wrinkle/proto/go/wrinkle/backend/v1"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

// Server exposes a Local backend over the internal RPC, for gateways in
// other processes or languages.
type Server struct {
	backendv1.UnimplementedBackendServiceServer
	local *Local
}

func NewServer(local *Local) *Server { return &Server{local: local} }

func (s *Server) Call(ctx context.Context, req *backendv1.CallRequest) (*backendv1.CallResponse, error) {
	var userID ids.UUID
	if len(req.GetUserId()) > 0 {
		var ok bool
		if userID, ok = ids.Parse(req.GetUserId()); !ok {
			return nil, status.Error(codes.InvalidArgument, "user_id must be empty or 16 bytes")
		}
	}
	resp, err := s.local.Call(ctx, userID, req.GetDeviceId(), req.GetRequest())
	if err != nil {
		return nil, err
	}
	return &backendv1.CallResponse{Response: resp}, nil
}

func (s *Server) Ack(ctx context.Context, req *backendv1.AckRequest) (*backendv1.AckResponse, error) {
	userID, ok := ids.Parse(req.GetUserId())
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "user_id must be 16 bytes")
	}
	if err := s.local.Ack(ctx, userID, req.GetDeviceId(), req.GetCursor()); err != nil {
		return nil, err
	}
	return &backendv1.AckResponse{}, nil
}

// stopGrace is how long a stopping server lets the calls in flight finish
// before dropping them, within the process's shutdown budget.
const stopGrace = 5 * time.Second

// Serve listens until the context ends.
func (s *Server) Serve(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// Every connected device may have a request in flight; the default of
	// 100 concurrent streams per connection made gateways fail under a
	// connect storm.
	srv := grpc.NewServer(grpc.MaxConcurrentStreams(1 << 20))
	backendv1.RegisterBackendServiceServer(srv, s)
	go func() {
		<-ctx.Done()
		stopped := make(chan struct{})
		go func() {
			srv.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(stopGrace):
			srv.Stop()
		}
	}()
	return srv.Serve(lis)
}

// Client forwards to a services process.
type Client struct {
	conn *grpc.ClientConn
	rpc  backendv1.BackendServiceClient
}

// Dial returns a client without connecting: the channel connects on first
// use and reconnects by itself, so a gateway starts while the services are
// not reachable yet, and its requests fail fast with Unavailable until they
// are.
func Dial(addr string) (*Client, error) {
	// The default backoff grows to two minutes, which would leave a gateway
	// started before its services failing long after they came up.
	reconnect := backoff.DefaultConfig
	reconnect.MaxDelay = 5 * time.Second
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: reconnect, MinConnectTimeout: 5 * time.Second}))
	if err != nil {
		return nil, fmt.Errorf("services at %s: %w", addr, err)
	}
	return &Client{conn: conn, rpc: backendv1.NewBackendServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Call(ctx context.Context, userID ids.UUID, deviceID uint64, req *pb.Request) (*pb.Response, error) {
	resp, err := c.rpc.Call(ctx, &backendv1.CallRequest{UserId: userID.Bytes(), DeviceId: deviceID, Request: req})
	if err != nil {
		return nil, err
	}
	return resp.GetResponse(), nil
}

func (c *Client) Ack(ctx context.Context, userID ids.UUID, deviceID, cursor uint64) error {
	_, err := c.rpc.Ack(ctx, &backendv1.AckRequest{UserId: userID.Bytes(), DeviceId: deviceID, Cursor: cursor})
	return err
}
