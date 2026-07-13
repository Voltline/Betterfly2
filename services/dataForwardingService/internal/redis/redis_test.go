package redisClient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func useTestRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	server := miniredis.RunT(t)
	previous := Rdb
	Rdb = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = Rdb.Close()
		Rdb = previous
	})
	return server
}

func TestGetContainerByConnectionRequiresMatchingLease(t *testing.T) {
	server := useTestRedis(t)
	if err := RegisterConnection(context.Background(), "1", "pod-a", "owner-a"); err != nil {
		t.Fatal(err)
	}
	if got, err := GetContainerByConnection("1"); err != nil || got != "pod-a" {
		t.Fatalf("expected valid route, got route=%q err=%v", got, err)
	}

	server.Del(routeLeaseKey("1"))
	if got, err := GetContainerByConnection("1"); !errors.Is(err, ErrRouteNotFound) || got != "" {
		t.Fatalf("missing lease returned stale route: route=%q err=%v", got, err)
	}
	if server.HGet("ws_connection_mapping", "1") != "" {
		t.Fatal("stale hash mapping was not cleaned")
	}
}

func TestGetContainerByConnectionRejectsMismatchedLeaseWithoutDeletingMigratedRoute(t *testing.T) {
	server := useTestRedis(t)
	server.HSet("ws_connection_mapping", "1", "pod-old")
	server.SAdd("container_connections:pod-old", "1")
	server.Set(routeLeaseKey("1"), "pod-new|owner-new")
	if _, err := GetContainerByConnection("1"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("expected mismatched route rejection, got %v", err)
	}

	if err := RegisterConnection(context.Background(), "1", "pod-new", "owner-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := UnregisterConnectionResultForTest("1", "pod-old", "owner-old"); err != nil {
		t.Fatal(err)
	}
	if got, err := GetContainerByConnection("1"); err != nil || got != "pod-new" {
		t.Fatalf("old cleanup deleted migrated route: route=%q err=%v", got, err)
	}
}

func UnregisterConnectionResultForTest(id, containerID, ownerToken string) (bool, error) {
	result, err := unregisterConnectionScript.Run(ctx, Rdb,
		[]string{"ws_connection_mapping", routeLeaseKey(id)}, id, containerID, ownerToken,
	).Int()
	return result == 1, err
}

func TestExpiredLeaseIsNotRoutable(t *testing.T) {
	server := useTestRedis(t)
	if err := RegisterConnection(context.Background(), "1", "pod-a", "owner-a"); err != nil {
		t.Fatal(err)
	}
	server.FastForward(routeLeaseTTL + time.Second)
	if _, err := GetContainerByConnection("1"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("expired lease remained routable: %v", err)
	}
}

func TestOldOwnerCannotUnregisterNewRouteInSameContainer(t *testing.T) {
	useTestRedis(t)
	ctx := context.Background()
	if err := RegisterConnection(ctx, "1", "pod-a", "old-owner"); err != nil {
		t.Fatal(err)
	}
	if err := RegisterConnection(ctx, "1", "pod-a", "new-owner"); err != nil {
		t.Fatal(err)
	}
	if err := UnregisterConnection(ctx, "1", "pod-a", "old-owner"); err != nil {
		t.Fatal(err)
	}
	if route, err := GetContainerByConnection("1"); err != nil || route != "pod-a" {
		t.Fatalf("old owner removed new route: route=%q err=%v", route, err)
	}
}

func TestGetContainersByConnectionsReturnsOnlyValidRoutes(t *testing.T) {
	server := useTestRedis(t)
	if err := RegisterConnection(context.Background(), "1", "pod-a", "owner-a"); err != nil {
		t.Fatal(err)
	}
	server.HSet("ws_connection_mapping", "2", "pod-b")
	server.HSet("ws_connection_mapping", "3", "pod-old")
	server.Set(routeLeaseKey("3"), "pod-new|owner-new")

	routes, err := GetContainersByConnections([]string{"1", "2", "3", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes["1"] != "pod-a" {
		t.Fatalf("batch returned invalid routes: %+v", routes)
	}
}

func TestRedisFailureIsDistinctFromOfflineRoute(t *testing.T) {
	previous := Rdb
	Rdb = redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:1",
		DialTimeout: 10 * time.Millisecond,
		ReadTimeout: 10 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = Rdb.Close()
		Rdb = previous
	})

	_, err := GetContainerByConnection("1")
	if err == nil || errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("Redis failure was mistaken for offline route: %v", err)
	}
}

func TestSessionAndLockCleanupRequireMatchingOwner(t *testing.T) {
	useTestRedis(t)
	ctx := context.Background()
	dsm := &DistributedSessionManager{}
	locked, err := dsm.AcquireUserLock(ctx, "9", "owner-a", time.Minute)
	if err != nil || !locked {
		t.Fatalf("failed to acquire lock: locked=%v err=%v", locked, err)
	}
	if err := dsm.ReleaseUserLock(ctx, "9", "owner-b"); err != nil {
		t.Fatal(err)
	}
	if value := Rdb.Get(ctx, userLockKey("9")).Val(); value != "owner-a" {
		t.Fatalf("foreign owner released lock: %q", value)
	}

	old := SessionData{ConnectionID: "old", ContainerID: "pod-a", OwnerToken: "owner-a"}
	newSession := SessionData{ConnectionID: "new", ContainerID: "pod-a", OwnerToken: "owner-b"}
	if err := dsm.ClaimSessionAndRoute(ctx, "9", old, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := dsm.ClaimSessionAndRoute(ctx, "9", newSession, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := dsm.RemoveOwnedSessionAndRoute(ctx, "9", old); err != nil {
		t.Fatal(err)
	}
	current, exists, err := dsm.GetUserSession(ctx, "9")
	if err != nil || !exists || current.OwnerToken != "owner-b" {
		t.Fatalf("foreign cleanup removed current session: %+v exists=%v err=%v", current, exists, err)
	}
}
