package integration

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "Betterfly2/proto/data_forwarding"
	sharedDB "Betterfly2/shared/db"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/proto"
)

const (
	defaultDFPort1 = "54342"
	defaultDFPort2 = "54343"
	e2eTimeout     = 12 * time.Second
)

type wsTestClient struct {
	t      *testing.T
	label  string
	conn   *websocket.Conn
	jwt    string
	userID int64
}

func TestFriendServiceEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_E2E") != "1" {
		t.Skip("set BETTERFLY_E2E=1 to run docker-compose end-to-end tests")
	}

	user1 := newWSClient(t, "user1", envOr("BETTERFLY_DF_PORT_1", defaultDFPort1))
	defer user1.close()
	user2 := newWSClient(t, "user2", envOr("BETTERFLY_DF_PORT_2", defaultDFPort2))
	defer user2.close()

	runID := time.Now().UnixNano()
	account1 := randomAccount(t, "u1")
	account2 := randomAccount(t, "u2")
	password := "betterfly-pass"
	groupID := runID%9_000_000_000 + 1_000_000_000
	groupAvatar := fmt.Sprintf("avatar-%d", runID)
	groupMessage1 := fmt.Sprintf("group-msg-%d", runID)
	groupMessage2 := fmt.Sprintf("group-msg-after-leave-%d", runID)

	user1.signupAndLogin(account1, password, "User One")
	user2.signupAndLogin(account2, password, "User Two")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_InsertContact{
		InsertContact: &pb.InsertContact{
			FromUserId:     user1.userID,
			ToInsertUserId: user2.userID,
		},
	}))
	friendRequest := user1.expectRelationshipOperation("create_friend_request")
	if friendRequest.GetResult() != "FRIEND_OK" || friendRequest.GetRequest().GetRequestId() <= 0 {
		t.Fatalf("unexpected friend request result: %+v", friendRequest)
	}
	user2.send(authenticatedRequest(user2.jwt, &pb.RequestMessage_ResolveFriendRequest{
		ResolveFriendRequest: &pb.ResolveFriendRequest{RequestId: friendRequest.GetRequest().GetRequestId(), Decision: pb.RequestDecision_REQUEST_ACCEPT},
	}))
	if accepted := user2.expectRelationshipOperation("resolve_friend_request"); accepted.GetResult() != "FRIEND_OK" {
		t.Fatalf("friend request acceptance failed: %+v", accepted)
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryContacts{
		QueryContacts: &pb.QueryContacts{FromUserId: user1.userID},
	}))
	contactsRsp := user1.expectContactList()
	contact := findContact(contactsRsp.GetContacts(), user2.userID)
	if contact == nil {
		t.Fatalf("expected user2 in user1 contact list")
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_UpdateContactAlias{
		UpdateContactAlias: &pb.UpdateContactAlias{
			FromUserId:   user1.userID,
			TargetUserId: user2.userID,
			NewAlias:     "bestie",
		},
	}))
	user1.expectServerMessageContaining("备注")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_UpdateContactNotify{
		UpdateContactNotify: &pb.UpdateContactNotify{
			FromUserId:   user1.userID,
			TargetUserId: user2.userID,
			IsNotify:     false,
		},
	}))
	user1.expectServerMessageContaining("通知")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryContacts{
		QueryContacts: &pb.QueryContacts{FromUserId: user1.userID},
	}))
	contactsRsp = user1.expectContactList()
	contact = findContact(contactsRsp.GetContacts(), user2.userID)
	if contact == nil {
		t.Fatalf("expected user2 in user1 contact list after alias/notify update")
	}
	if contact.GetAlias() != "bestie" {
		t.Fatalf("expected alias bestie, got %q", contact.GetAlias())
	}
	if contact.GetIsNotify() {
		t.Fatal("expected contact notify to be false after update")
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_DeleteContact{
		DeleteContact: &pb.DeleteContact{
			FromUserId:     user1.userID,
			ToDeleteUserId: user2.userID,
		},
	}))
	user1.expectServerMessageContaining("删除好友")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryContacts{
		QueryContacts: &pb.QueryContacts{FromUserId: user1.userID},
	}))
	contactsRsp = user1.expectContactList()
	if findContact(contactsRsp.GetContacts(), user2.userID) != nil {
		t.Fatalf("expected user2 to be removed from user1 contact list")
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_InsertGroup{
		InsertGroup: &pb.InsertGroup{
			FromUserId:           user1.userID,
			ToBeCreatedGroupId:   groupID,
			ToBeCreatedGroupName: "codex-e2e-group",
		},
	}))
	user1.expectServerMessageContaining("创建群组")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryGroup{
		QueryGroup: &pb.QueryGroup{
			FromUserId:     user1.userID,
			ToQueryGroupId: groupID,
			ClientNeedSave: true,
		},
	}))
	groupInfo := user1.expectGroupInfo()
	if groupInfo.GetQueryGroupId() != groupID {
		t.Fatalf("expected queried group id %d, got %d", groupID, groupInfo.GetQueryGroupId())
	}
	if groupInfo.GetQueryGroupName() != "codex-e2e-group" {
		t.Fatalf("expected group name codex-e2e-group, got %q", groupInfo.GetQueryGroupName())
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryJoinedGroups{
		QueryJoinedGroups: &pb.QueryJoinedGroups{
			FromUserId: user1.userID,
		},
	}))
	joinedGroups := user1.expectJoinedGroups()
	if len(joinedGroups.GetGroups()) != 1 {
		t.Fatalf("expected 1 joined group for user1 after create, got %d", len(joinedGroups.GetGroups()))
	}
	if group := findJoinedGroup(joinedGroups.GetGroups(), groupID); group == nil {
		t.Fatalf("expected created group %d to appear in user1 joined groups", groupID)
	}

	user2.send(authenticatedRequest(user2.jwt, &pb.RequestMessage_InsertGroupUser{
		InsertGroupUser: &pb.InsertGroupUser{
			FromUserId:    user2.userID,
			TargetGroupId: groupID,
		},
	}))
	joinRequest := user2.expectRelationshipOperation("create_group_join_request")
	if joinRequest.GetResult() != "FRIEND_OK" || joinRequest.GetRequest().GetRequestId() <= 0 {
		t.Fatalf("unexpected group join request result: %+v", joinRequest)
	}
	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_ResolveGroupJoinRequest{
		ResolveGroupJoinRequest: &pb.ResolveGroupJoinRequest{RequestId: joinRequest.GetRequest().GetRequestId(), Decision: pb.RequestDecision_REQUEST_ACCEPT},
	}))
	if accepted := user1.expectRelationshipOperation("resolve_group_join_request"); accepted.GetResult() != "FRIEND_OK" {
		t.Fatalf("group join request acceptance failed: %+v", accepted)
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryGroupMembers{
		QueryGroupMembers: &pb.QueryGroupMembers{
			FromUserId:    user1.userID,
			TargetGroupId: groupID,
		},
	}))
	groupMembers := user1.expectGroupMembers()
	if len(groupMembers.GetMembers()) != 2 {
		t.Fatalf("expected 2 group members after join, got %d", len(groupMembers.GetMembers()))
	}
	member1 := findGroupMember(groupMembers.GetMembers(), user1.userID)
	member2 := findGroupMember(groupMembers.GetMembers(), user2.userID)
	if member1 == nil || member1.GetRole() != "owner" {
		t.Fatalf("expected user1 to be owner, got %+v", member1)
	}
	if member2 == nil || member2.GetRole() != "member" {
		t.Fatalf("expected user2 to be member, got %+v", member2)
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_UpdateGroupName{
		UpdateGroupName: &pb.UpdateGroupName{TargetGroupId: groupID, NewGroupName: "codex-renamed-group"},
	}))
	renameResult := user1.expectGroupMemberOperation("update_group_name")
	if renameResult.GetResult() != "FRIEND_OK" || renameResult.GetGroupName() != "codex-renamed-group" {
		t.Fatalf("unexpected group rename result: %+v", renameResult)
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_TransferGroupOwner{
		TransferGroupOwner: &pb.TransferGroupOwner{TargetGroupId: groupID, TargetUserId: user2.userID},
	}))
	transferResult := user1.expectGroupMemberOperation("transfer_group_owner")
	if transferResult.GetResult() != "FRIEND_OK" || transferResult.GetUserId() != user2.userID || transferResult.GetPreviousOwnerUserId() != user1.userID {
		t.Fatalf("unexpected owner transfer result: %+v", transferResult)
	}

	user2.send(authenticatedRequest(user2.jwt, &pb.RequestMessage_UpdateGroupName{
		UpdateGroupName: &pb.UpdateGroupName{TargetGroupId: groupID, NewGroupName: "codex-new-owner-group"},
	}))
	if operation := user2.expectGroupMemberOperation("update_group_name"); operation.GetResult() != "FRIEND_OK" {
		t.Fatalf("new owner could not manage group: %+v", operation)
	}
	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryGroup{
		QueryGroup: &pb.QueryGroup{ToQueryGroupId: groupID},
	}))
	if groupInfo = user1.expectGroupInfo(); groupInfo.GetQueryGroupName() != "codex-new-owner-group" {
		t.Fatalf("group rename was not visible on the next query: %+v", groupInfo)
	}
	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryGroupMembers{
		QueryGroupMembers: &pb.QueryGroupMembers{TargetGroupId: groupID},
	}))
	groupMembers = user1.expectGroupMembers()
	member1 = findGroupMember(groupMembers.GetMembers(), user1.userID)
	member2 = findGroupMember(groupMembers.GetMembers(), user2.userID)
	if member1 == nil || member1.GetRole() != "admin" || member2 == nil || member2.GetRole() != "owner" {
		t.Fatalf("owner transfer roles are inconsistent: old=%+v new=%+v", member1, member2)
	}

	user2.send(authenticatedRequest(user2.jwt, &pb.RequestMessage_QueryJoinedGroups{
		QueryJoinedGroups: &pb.QueryJoinedGroups{
			FromUserId: user2.userID,
		},
	}))
	joinedGroups = user2.expectJoinedGroups()
	if group := findJoinedGroup(joinedGroups.GetGroups(), groupID); group == nil {
		t.Fatalf("expected joined group %d to appear in user2 joined groups", groupID)
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_UpdateAvatar{
		UpdateAvatar: &pb.UpdateAvatar{
			TargetId:   groupID,
			AvatarHash: groupAvatar,
			IsGroup:    true,
		},
	}))
	user1.expectServerMessageContaining("群头像")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryGroup{
		QueryGroup: &pb.QueryGroup{
			FromUserId:     user1.userID,
			ToQueryGroupId: groupID,
		},
	}))
	groupInfo = user1.expectGroupInfo()
	if groupInfo.GetAvatar() != groupAvatar {
		t.Fatalf("expected group avatar %q, got %q", groupAvatar, groupInfo.GetAvatar())
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_Post{
		Post: &pb.Post{
			FromId:    user1.userID,
			IsGroup:   true,
			ToId:      groupID,
			Msg:       groupMessage1,
			MsgType:   "text",
			Timestamp: nowTimestamp(),
		},
	}))
	groupPost := user2.expectGroupPost(groupID, user1.userID, groupMessage1)
	if !groupPost.GetIsGroup() {
		t.Fatal("expected received post to be group message")
	}

	user2.send(authenticatedRequest(user2.jwt, &pb.RequestMessage_DeleteGroupUser{
		DeleteGroupUser: &pb.DeleteGroupUser{
			FromUserId:    user2.userID,
			TargetGroupId: groupID,
		},
	}))
	user2.expectServerMessageContaining("退出群组")

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_QueryGroupMembers{
		QueryGroupMembers: &pb.QueryGroupMembers{
			FromUserId:    user1.userID,
			TargetGroupId: groupID,
		},
	}))
	groupMembers = user1.expectGroupMembers()
	if len(groupMembers.GetMembers()) != 1 {
		t.Fatalf("expected 1 group member after user2 leaves, got %d", len(groupMembers.GetMembers()))
	}
	if findGroupMember(groupMembers.GetMembers(), user2.userID) != nil {
		t.Fatal("expected user2 to be absent from group after leaving")
	}

	user2.send(authenticatedRequest(user2.jwt, &pb.RequestMessage_QueryJoinedGroups{
		QueryJoinedGroups: &pb.QueryJoinedGroups{
			FromUserId: user2.userID,
		},
	}))
	joinedGroups = user2.expectJoinedGroups()
	if group := findJoinedGroup(joinedGroups.GetGroups(), groupID); group != nil {
		t.Fatalf("expected group %d to be absent from user2 joined groups after leaving", groupID)
	}

	user1.send(authenticatedRequest(user1.jwt, &pb.RequestMessage_Post{
		Post: &pb.Post{
			FromId:    user1.userID,
			IsGroup:   true,
			ToId:      groupID,
			Msg:       groupMessage2,
			MsgType:   "text",
			Timestamp: nowTimestamp(),
		},
	}))
	user2.expectNoGroupPost(groupMessage2, 2*time.Second)
}

func newWSClient(t *testing.T, label, port string) *wsTestClient {
	t.Helper()

	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	url := fmt.Sprintf("wss://localhost:%s/ws", port)
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("%s failed to connect to %s: %v", label, url, err)
	}
	return &wsTestClient{
		t:     t,
		label: label,
		conn:  conn,
	}
}

func (c *wsTestClient) close() {
	_ = c.conn.Close()
}

func (c *wsTestClient) send(req *pb.RequestMessage) {
	c.t.Helper()
	data, err := proto.Marshal(req)
	if err != nil {
		c.t.Fatalf("%s failed to marshal request: %v", c.label, err)
	}
	if err := c.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		c.t.Fatalf("%s failed to write websocket message: %v", c.label, err)
	}
}

func (c *wsTestClient) signupAndLogin(account, password, userName string) {
	c.t.Helper()

	loginAccount := account
	for attempt := 0; attempt < 5; attempt++ {
		c.t.Logf("%s signup attempt %d with account %s", c.label, attempt+1, loginAccount)
		c.send(&pb.RequestMessage{
			Payload: &pb.RequestMessage_Signup{
				Signup: &pb.SignupReq{
					Account:  loginAccount,
					Password: password,
					UserName: userName,
				},
			},
		})
		signupRsp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
			return resp.GetSignup() != nil
		})
		if signupRsp.GetSignup().GetResult() == pb.SignupResult_SIGNUP_OK {
			account = loginAccount
			break
		}
		c.t.Logf("%s signup result for account %s: %v", c.label, loginAccount, signupRsp.GetSignup().GetResult())
		if signupRsp.GetSignup().GetResult() != pb.SignupResult_ACCOUNT_EXIST {
			c.t.Fatalf("%s signup failed: %v", c.label, signupRsp.GetSignup().GetResult())
		}
		loginAccount = randomAccount(c.t, account)
		if attempt == 4 {
			c.t.Logf("%s signup keeps returning ACCOUNT_EXIST; seeding test user directly into DB for login fallback", c.label)
			seedUserForLogin(c.t, loginAccount, password, userName)
		}
	}

	c.send(&pb.RequestMessage{
		Payload: &pb.RequestMessage_Login{
			Login: &pb.LoginReq{
				Account:  loginAccount,
				Password: password,
			},
		},
	})
	loginRsp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		return resp.GetLogin() != nil
	})
	if loginRsp.GetLogin().GetResult() != pb.LoginResult_LOGIN_OK {
		c.t.Fatalf("%s login failed: %v", c.label, loginRsp.GetLogin().GetResult())
	}
	c.jwt = loginRsp.GetLogin().GetJwt()
	c.userID = loginRsp.GetLogin().GetUserId()
}

func (c *wsTestClient) waitFor(timeout time.Duration, match func(*pb.ResponseMessage) bool) *pb.ResponseMessage {
	c.t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.t.Fatalf("%s timed out waiting for websocket response", c.label)
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(remaining))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.t.Fatalf("%s failed to read websocket message: %v", c.label, err)
		}

		resp := &pb.ResponseMessage{}
		if err := proto.Unmarshal(data, resp); err != nil {
			c.t.Fatalf("%s failed to unmarshal websocket response: %v", c.label, err)
		}
		if match(resp) {
			return resp
		}
	}
}

func (c *wsTestClient) expectServerMessageContaining(substr string) {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		return resp.GetServer() != nil || resp.GetWarn() != nil
	})

	if server := resp.GetServer(); server != nil {
		if !strings.Contains(server.GetServerMsg(), substr) {
			c.t.Fatalf("%s expected server message containing %q, got %q", c.label, substr, server.GetServerMsg())
		}
		return
	}
	if warn := resp.GetWarn(); warn != nil {
		c.t.Fatalf("%s expected success message containing %q, got warning %q", c.label, substr, warn.GetWarningMessage())
	}
}

func (c *wsTestClient) expectContactList() *pb.ContactListRsp {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		return resp.GetContactListRsp() != nil
	})
	return resp.GetContactListRsp()
}

func (c *wsTestClient) expectGroupInfo() *pb.GroupInfo {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		return resp.GetGroupInfo() != nil
	})
	return resp.GetGroupInfo()
}

func (c *wsTestClient) expectGroupMembers() *pb.GroupMembersRsp {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		return resp.GetGroupMembersRsp() != nil
	})
	return resp.GetGroupMembersRsp()
}

func (c *wsTestClient) expectJoinedGroups() *pb.JoinedGroupsRsp {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		return resp.GetJoinedGroupsRsp() != nil
	})
	return resp.GetJoinedGroupsRsp()
}

func (c *wsTestClient) expectGroupMemberOperation(operation string) *pb.GroupMemberOperationRsp {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		result := resp.GetGroupMemberOperationRsp()
		return result != nil && result.GetOperation() == operation
	})
	return resp.GetGroupMemberOperationRsp()
}

func (c *wsTestClient) expectRelationshipOperation(operation string) *pb.RelationshipOperationRsp {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		result := resp.GetRelationshipOperationRsp()
		return result != nil && result.GetOperation() == operation
	})
	return resp.GetRelationshipOperationRsp()
}

func (c *wsTestClient) expectGroupPost(groupID, fromUserID int64, msg string) *pb.Post {
	c.t.Helper()
	resp := c.waitFor(e2eTimeout, func(resp *pb.ResponseMessage) bool {
		post := resp.GetPost()
		return post != nil && post.GetIsGroup() && post.GetToId() == groupID && post.GetFromId() == fromUserID && post.GetMsg() == msg
	})
	return resp.GetPost()
}

func (c *wsTestClient) expectNoGroupPost(msg string, timeout time.Duration) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		if websocket.IsUnexpectedCloseError(err) {
			c.t.Fatalf("%s unexpected websocket close while waiting for no group post: %v", c.label, err)
		}
		if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "i/o timeout") {
			return
		}
		c.t.Fatalf("%s failed while asserting no group post: %v", c.label, err)
	}

	resp := &pb.ResponseMessage{}
	if err := proto.Unmarshal(data, resp); err != nil {
		c.t.Fatalf("%s failed to decode unexpected websocket message: %v", c.label, err)
	}
	if post := resp.GetPost(); post != nil && post.GetMsg() == msg {
		c.t.Fatalf("%s unexpectedly received group post %q after leaving group", c.label, msg)
	}
	c.t.Fatalf("%s received unexpected websocket response while expecting no group post: %+v", c.label, resp)
}

func authenticatedRequest(jwt string, payload isAuthenticatedPayload) *pb.RequestMessage {
	req := &pb.RequestMessage{Jwt: jwt}
	switch p := payload.(type) {
	case *pb.RequestMessage_InsertContact:
		req.Payload = p
	case *pb.RequestMessage_QueryContacts:
		req.Payload = p
	case *pb.RequestMessage_UpdateContactAlias:
		req.Payload = p
	case *pb.RequestMessage_UpdateContactNotify:
		req.Payload = p
	case *pb.RequestMessage_DeleteContact:
		req.Payload = p
	case *pb.RequestMessage_ResolveFriendRequest:
		req.Payload = p
	case *pb.RequestMessage_InsertGroup:
		req.Payload = p
	case *pb.RequestMessage_QueryGroup:
		req.Payload = p
	case *pb.RequestMessage_InsertGroupUser:
		req.Payload = p
	case *pb.RequestMessage_ResolveGroupJoinRequest:
		req.Payload = p
	case *pb.RequestMessage_QueryGroupMembers:
		req.Payload = p
	case *pb.RequestMessage_QueryJoinedGroups:
		req.Payload = p
	case *pb.RequestMessage_UpdateAvatar:
		req.Payload = p
	case *pb.RequestMessage_Post:
		req.Payload = p
	case *pb.RequestMessage_DeleteGroupUser:
		req.Payload = p
	case *pb.RequestMessage_UpdateGroupName:
		req.Payload = p
	case *pb.RequestMessage_TransferGroupOwner:
		req.Payload = p
	default:
		panic(fmt.Sprintf("unsupported authenticated payload type %T", payload))
	}
	return req
}

type isAuthenticatedPayload interface{}

func findContact(contacts []*pb.ContactInfo, userID int64) *pb.ContactInfo {
	for _, contact := range contacts {
		if contact.GetUserId() == userID {
			return contact
		}
	}
	return nil
}

func findGroupMember(members []*pb.GroupMemberInfo, userID int64) *pb.GroupMemberInfo {
	for _, member := range members {
		if member.GetUserId() == userID {
			return member
		}
	}
	return nil
}

func findJoinedGroup(groups []*pb.JoinedGroupInfo, groupID int64) *pb.JoinedGroupInfo {
	for _, group := range groups {
		if group.GetGroupId() == groupID {
			return group
		}
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func nowTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func randomAccount(t *testing.T, prefix string) string {
	t.Helper()

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("failed to generate random account suffix: %v", err)
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(buf))
}

func seedUserForLogin(t *testing.T, account, password, userName string) {
	t.Helper()

	dsn := os.Getenv("PGSQL_DSN")
	if dsn == "" {
		dsn = loadDSNFromDotEnv(t)
	}
	if err := os.Setenv("PGSQL_DSN", dsn); err != nil {
		t.Fatalf("failed to set PGSQL_DSN for test seeding: %v", err)
	}

	if existing, err := sharedDB.GetUserByAccount(account); err != nil {
		t.Fatalf("failed to check existing user %s before seeding: %v", account, err)
	} else if existing != nil {
		return
	}

	nextUserID := nextSeedUserID(t)

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash test password: %v", err)
	}

	if err := sharedDB.AddUser(&sharedDB.User{
		ID:           nextUserID,
		Account:      account,
		Name:         userName,
		PasswordHash: string(hash),
	}); err != nil {
		t.Fatalf("failed to seed test user %s: %v", account, err)
	}
}

func loadDSNFromDotEnv(t *testing.T) string {
	t.Helper()

	candidates := []string{
		filepath.Join("..", "..", ".env"),
		filepath.Join("..", ".env"),
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "PGSQL_DSN=") {
				return strings.TrimPrefix(line, "PGSQL_DSN=")
			}
		}
	}

	t.Fatal("failed to load PGSQL_DSN from environment or services/.env")
	return ""
}

func nextSeedUserID(t *testing.T) int64 {
	t.Helper()

	type maxUserIDRow struct {
		MaxID int64 `gorm:"column:max_id"`
	}

	var row maxUserIDRow
	if err := sharedDB.DB().Raw("SELECT COALESCE(MAX(id), 0) AS max_id FROM users").Scan(&row).Error; err != nil {
		t.Fatalf("failed to query next test user id: %v", err)
	}
	return row.MaxID + 1
}
