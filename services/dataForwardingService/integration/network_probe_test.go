package integration

import (
	pb "Betterfly2/proto/data_forwarding"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

type receivedResponse struct {
	message *pb.ResponseMessage
	at      time.Time
}

// Always read so idle peers respond to server pings, including during load setup.
type networkProbe struct {
	conn    *websocket.Conn
	events  chan receivedResponse
	quit    chan struct{}
	done    chan struct{}
	writeMu sync.Mutex
	mu      sync.Mutex
	err     error
	posts   map[int64]int
	once    sync.Once
	account string
	jwt     string
	userID  int64
}

const probePassword = "bf2-acceptance-test-password"

func openNetworkProbe(port string) (*networkProbe, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // Local self-signed test endpoint only.
	}}
	conn, _, err := dialer.Dial("wss://127.0.0.1:"+port+"/ws", nil)
	if err != nil {
		return nil, err
	}
	p := &networkProbe{conn: conn, events: make(chan receivedResponse, 4096), quit: make(chan struct{}), done: make(chan struct{}), posts: make(map[int64]int)}
	go func() {
		defer close(p.done)
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				p.mu.Lock()
				p.err = err
				p.mu.Unlock()
				return
			}
			message := &pb.ResponseMessage{}
			if err := proto.Unmarshal(payload, message); err != nil {
				p.mu.Lock()
				p.err = err
				p.mu.Unlock()
				return
			}
			if post := message.GetPost(); post != nil {
				p.mu.Lock()
				p.posts[post.GetMessageId()]++
				p.mu.Unlock()
			}
			select {
			case p.events <- receivedResponse{message: message, at: time.Now()}:
			case <-p.quit:
				return
			}
		}
	}()
	return p, nil
}

func (p *networkProbe) close() {
	p.once.Do(func() { close(p.quit); _ = p.conn.Close(); <-p.done })
}

func (p *networkProbe) send(request *pb.RequestMessage) error {
	request.Jwt = p.jwt
	payload, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return p.conn.WriteMessage(websocket.BinaryMessage, payload)
}

func (p *networkProbe) wait(timeout time.Duration, match func(*pb.ResponseMessage) bool) (receivedResponse, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case response := <-p.events:
			if match(response.message) {
				return response, nil
			}
			if response.message.GetWarn() != nil || response.message.GetRefused() != nil {
				return receivedResponse{}, fmt.Errorf("unexpected rejection for test user %d", p.userID)
			}
		case <-p.done:
			p.mu.Lock()
			err := p.err
			p.mu.Unlock()
			return receivedResponse{}, fmt.Errorf("test connection closed: %w", err)
		case <-timer.C:
			return receivedResponse{}, fmt.Errorf("test user %d response timeout after %s", p.userID, timeout)
		}
	}
}

func (p *networkProbe) request(request *pb.RequestMessage, match func(*pb.ResponseMessage) bool) (receivedResponse, error) {
	if err := p.send(request); err != nil {
		return receivedResponse{}, err
	}
	return p.wait(15*time.Second, match)
}

func (p *networkProbe) signup(account string) error {
	response, err := p.request(&pb.RequestMessage{Payload: &pb.RequestMessage_Signup{Signup: &pb.SignupReq{Account: account, Password: probePassword, UserName: "BF2 acceptance"}}}, func(r *pb.ResponseMessage) bool { return r.GetSignup() != nil })
	if err != nil {
		return err
	}
	if response.message.GetSignup().GetResult() != pb.SignupResult_SIGNUP_OK {
		return fmt.Errorf("signup rejected: %s", response.message.GetSignup().GetResult())
	}
	return p.login(account)
}

func (p *networkProbe) login(account string) error {
	response, err := p.request(&pb.RequestMessage{Payload: &pb.RequestMessage_Login{Login: &pb.LoginReq{Account: account, Password: probePassword}}}, func(r *pb.ResponseMessage) bool { return r.GetLogin() != nil })
	if err != nil {
		return err
	}
	login := response.message.GetLogin()
	if login.GetResult() != pb.LoginResult_LOGIN_OK {
		return fmt.Errorf("login rejected: %s", login.GetResult())
	}
	p.account, p.jwt, p.userID = account, login.GetJwt(), login.GetUserId()
	return nil
}

func (p *networkProbe) postCount(messageID int64) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.posts[messageID]
}
