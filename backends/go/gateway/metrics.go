package gateway

import (
	"time"

	"github.com/vlumi/wrinkle/backends/go/internal/metrics"
	pb "github.com/vlumi/wrinkle/proto/go/wrinkle/v1"
)

var requestBody = (&pb.Request{}).ProtoReflect().Descriptor().Oneofs().ByName("body")

func observeRequest(req *pb.Request, resp *pb.Response, start time.Time) {
	kind := requestType(req)
	code := "ok"
	if e := resp.GetError(); e != nil {
		code = e.GetCode().String()
	}
	metrics.Requests.WithLabelValues(kind, code).Inc()
	metrics.RequestDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
}

// requestType is the metrics label for a request: the name of its body
// field in the contract, such as send_message.
func requestType(req *pb.Request) string {
	field := req.ProtoReflect().WhichOneof(requestBody)
	if field == nil {
		return "unknown"
	}
	return string(field.Name())
}
