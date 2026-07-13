package redisClient

import (
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
	if err := RegisterConnection("1", "pod-a"); err != nil {
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
	server.Set(routeLeaseKey("1"), "pod-new")
	if _, err := GetContainerByConnection("1"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("expected mismatched route rejection, got %v", err)
	}

	if err := RegisterConnection("1", "pod-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := UnregisterConnectionResultForTest("1", "pod-old"); err != nil {
		t.Fatal(err)
	}
	if got, err := GetContainerByConnection("1"); err != nil || got != "pod-new" {
		t.Fatalf("old cleanup deleted migrated route: route=%q err=%v", got, err)
	}
}

func UnregisterConnectionResultForTest(id, containerID string) (bool, error) {
	result, err := unregisterConnectionScript.Run(ctx, Rdb,
		[]string{"ws_connection_mapping", routeLeaseKey(id)}, id, containerID,
	).Int()
	return result == 1, err
}

func TestExpiredLeaseIsNotRoutable(t *testing.T) {
	server := useTestRedis(t)
	if err := RegisterConnection("1", "pod-a"); err != nil {
		t.Fatal(err)
	}
	server.FastForward(routeLeaseTTL + time.Second)
	if _, err := GetContainerByConnection("1"); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("expired lease remained routable: %v", err)
	}
}

func TestGetContainersByConnectionsReturnsOnlyValidRoutes(t *testing.T) {
	server := useTestRedis(t)
	if err := RegisterConnection("1", "pod-a"); err != nil {
		t.Fatal(err)
	}
	server.HSet("ws_connection_mapping", "2", "pod-b")
	server.HSet("ws_connection_mapping", "3", "pod-old")
	server.Set(routeLeaseKey("3"), "pod-new")

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
