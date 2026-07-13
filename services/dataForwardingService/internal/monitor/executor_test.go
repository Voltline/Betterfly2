package monitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExecutorRejectsEveryNonAdminWithoutInvokingActions(t *testing.T) {
	called := false
	executor := NewExecutor(Actions{Status: func(context.Context) (string, error) {
		called = true
		return "ok", nil
	}})

	if _, _, err := executor.Execute(context.Background(), 2, "/status"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
	if called {
		t.Fatal("unauthorized request invoked a monitor action")
	}
}

func TestExecutorDispatchesReadOnlyCommands(t *testing.T) {
	executor := NewExecutor(Actions{
		Status: func(context.Context) (string, error) { return "all healthy", nil },
		Route: func(_ context.Context, userID int64) (string, error) {
			if userID != 42 {
				t.Fatalf("unexpected user id: %d", userID)
			}
			return "df-a", nil
		},
	})

	name, response, err := executor.Execute(context.Background(), AdminUserID, "/status")
	if err != nil || name != "status" || response != "all healthy" {
		t.Fatalf("unexpected status result: name=%q response=%q err=%v", name, response, err)
	}
	name, response, err = executor.Execute(context.Background(), AdminUserID, "/route 42")
	if err != nil || name != "route" || response != "df-a" {
		t.Fatalf("unexpected route result: name=%q response=%q err=%v", name, response, err)
	}
}

func TestExecutorProtectsAdminAndMonitorFromKick(t *testing.T) {
	called := false
	executor := NewExecutor(Actions{Kick: func(context.Context, int64) (string, error) {
		called = true
		return "kicked", nil
	}})

	for _, userID := range []int64{AdminUserID, CurrentProfile().UserID} {
		_, response, err := executor.Execute(context.Background(), AdminUserID, "/kick "+strconvFormat(userID))
		if err != nil || !strings.Contains(response, "拒绝操作") {
			t.Fatalf("expected protected kick rejection for %d: response=%q err=%v", userID, response, err)
		}
	}
	if called {
		t.Fatal("protected kick invoked the mutation callback")
	}
}

func TestExecutorSupportsReadOnlyUserInspectionForAdminOnly(t *testing.T) {
	called := int64(0)
	executor := NewExecutor(Actions{User: func(_ context.Context, userID int64) (string, error) {
		called = userID
		return "summary", nil
	}})
	if _, _, err := executor.Execute(context.Background(), 2, "/user 42"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin user inspection should be forbidden, got %v", err)
	}
	_, response, err := executor.Execute(context.Background(), AdminUserID, "/user 42")
	if err != nil || response != "summary" || called != 42 {
		t.Fatalf("admin user inspection failed: response=%q called=%d err=%v", response, called, err)
	}
}

func TestExecutorAllowsKickForOrdinaryPositiveUser(t *testing.T) {
	calledWith := int64(0)
	executor := NewExecutor(Actions{Kick: func(_ context.Context, userID int64) (string, error) {
		calledWith = userID
		return "queued", nil
	}})
	name, response, err := executor.Execute(context.Background(), AdminUserID, "/kick 42")
	if err != nil || name != "kick" || response != "queued" || calledWith != 42 {
		t.Fatalf("unexpected kick result: name=%q response=%q called=%d err=%v", name, response, calledWith, err)
	}
}

func TestCurrentProfileRejectsAdminAndInvalidConfiguredIDs(t *testing.T) {
	for _, value := range []string{"1", "0", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MONITOR_USER_ID", value)
			if got := CurrentProfile().UserID; got != DefaultMonitorID {
				t.Fatalf("unsafe configured id %q produced %d", value, got)
			}
		})
	}
}

func TestExecutorRejectsMalformedAndUnknownCommands(t *testing.T) {
	executor := NewExecutor(Actions{})
	tests := []struct {
		input string
		want  string
	}{
		{input: "/route", want: "用法"},
		{input: "/route nope", want: "正整数"},
		{input: "/shell rm -rf /", want: "未知指令"},
	}
	for _, test := range tests {
		_, response, err := executor.Execute(context.Background(), AdminUserID, test.input)
		if err != nil || !strings.Contains(response, test.want) {
			t.Fatalf("input=%q response=%q err=%v", test.input, response, err)
		}
	}
}

func strconvFormat(value int64) string {
	return fmt.Sprintf("%d", value)
}
