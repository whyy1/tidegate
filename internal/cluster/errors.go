package cluster

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	// ErrMessageTooLarge is wrapped by send errors for messages above the
	// configured maximum message size.
	ErrMessageTooLarge = errors.New("消息超过大小上限")

	// ErrRejected matches every *RejectedError.
	ErrRejected = errors.New("控制器拒绝了节点接入")
	// ErrBadToken matches a *RejectedError caused by an invalid token.
	ErrBadToken = errors.New("节点令牌无效")
	// ErrVersionMismatch matches a *RejectedError caused by a version
	// mismatch between node and controller.
	ErrVersionMismatch = errors.New("节点与控制器版本不一致")

	// ErrServerClosed is the NodeLeft reason when the server stops.
	ErrServerClosed = errors.New("控制器已关闭")
	// ErrNodeGone is returned when sending to a node whose connection has
	// ended.
	ErrNodeGone = errors.New("节点已断开，消息未发送")
	// ErrSendQueueFull is returned when a node's outbound queue is full. The
	// message is not sent and the node stays connected.
	ErrSendQueueFull = errors.New("节点发送队列已满，消息未发送")
	// ErrNodeDisconnected is the NodeLeft reason when the stream ends; it may
	// be wrapped with transport details.
	ErrNodeDisconnected = errors.New("节点连接已断开")
	// ErrNodeTimeout is the NodeLeft reason when nothing arrived from the node
	// within the ping timeout.
	ErrNodeTimeout = errors.New("节点心跳超时")
	// ErrNodeReplaced is the NodeLeft reason when the same node ID connected
	// again on a new stream.
	ErrNodeReplaced = errors.New("节点已在新连接上重新接入，旧连接已关闭")
	// ErrNodeClosed is the NodeLeft reason after Node.Close.
	ErrNodeClosed = errors.New("控制器主动断开了节点")

	// ErrNotConnected is returned by Client.SendSysStats while there is no
	// connection; the sample is dropped.
	ErrNotConnected = errors.New("未连接到控制器，系统监控数据已丢弃")
	// ErrAlreadyRunning is returned when Client.Run is called while another
	// Run on the same client is active.
	ErrAlreadyRunning = errors.New("客户端已在运行")
	// ErrDisconnected wraps the reason a connection to the controller ended.
	ErrDisconnected = errors.New("与控制器的连接已断开")
	// ErrHandshakeTimeout is reported when no Welcome arrived in time.
	ErrHandshakeTimeout = errors.New("等待控制器响应超时")
	// ErrControllerTimeout is reported when nothing arrived from the
	// controller within ClientOptions.PingTimeout.
	ErrControllerTimeout = errors.New("控制器心跳超时")
)

// RejectedError is returned by Client.Run when the controller refused the
// node. It is terminal: Run does not retry.
type RejectedError struct {
	Reason RejectReason
	// Message is the controller's explanation in Simplified Chinese.
	Message string
	// ServerVersion is set for RejectVersionMismatch.
	ServerVersion string
}

func (e *RejectedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s（%s）", ErrRejected.Error(), e.Reason)
}

// Is makes errors.Is match ErrRejected, and ErrBadToken or
// ErrVersionMismatch according to the reason.
func (e *RejectedError) Is(target error) bool {
	switch target {
	case ErrRejected:
		return true
	case ErrBadToken:
		return e.Reason == RejectBadToken
	case ErrVersionMismatch:
		return e.Reason == RejectVersionMismatch
	}
	return false
}

// TokenEqual compares two tokens in constant time. Hashing first keeps the
// comparison time independent of the token lengths too.
func TokenEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func tooLarge(n, limit int) error {
	return fmt.Errorf("%w：%d 字节，上限 %d 字节", ErrMessageTooLarge, n, limit)
}

// errText returns the message of a gRPC status error, or err.Error().
func errText(err error) string {
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}

func nodeDisconnected(err error) error {
	if err == nil || errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
		return ErrNodeDisconnected
	}
	return fmt.Errorf("%w：%s", ErrNodeDisconnected, errText(err))
}

func controllerDisconnected(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return ErrDisconnected
	}
	return fmt.Errorf("%w：%s", ErrDisconnected, errText(err))
}
