package cache

import (
	"Betterfly2/shared/db"
	"reflect"
	"testing"
	"time"
)

func TestGobCacheEncodingRoundTripsSupportedValues(t *testing.T) {
	initGobTypes()
	tests := []struct {
		name  string
		value interface{}
	}{
		{name: "message", value: &db.Message{MessageID: 7, Content: "hello", MessageType: "text"}},
		{name: "user", value: &db.User{ID: 9, Account: "alice"}},
		{name: "string", value: "cached-value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := encode(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, tt.value) {
				t.Fatalf("round trip mismatch: got %#v want %#v", decoded, tt.value)
			}
		})
	}
}

func TestDecodeRejectsMalformedCacheData(t *testing.T) {
	if value, err := decode([]byte("not-gob-data")); err == nil || value != nil {
		t.Fatalf("expected malformed data rejection, got value=%#v err=%v", value, err)
	}
}

func TestL1CacheSetGetDeleteAndTTL(t *testing.T) {
	cache := NewL1Cache()
	key := "cache-test-key"
	cache.Del(key)

	if !cache.Set(key, "value", 40*time.Millisecond) {
		t.Fatal("expected cache set to be accepted")
	}
	l1Cache.Wait()
	if value, ok := cache.Get(key); !ok || value != "value" {
		t.Fatalf("unexpected cached value: value=%#v ok=%v", value, ok)
	}

	time.Sleep(60 * time.Millisecond)
	if value, ok := cache.Get(key); ok || value != nil {
		t.Fatalf("expected TTL expiration: value=%#v ok=%v", value, ok)
	}

	if !cache.Set(key, "second", time.Minute) {
		t.Fatal("expected second cache set to be accepted")
	}
	l1Cache.Wait()
	cache.Del(key)
	l1Cache.Wait()
	if _, ok := cache.Get(key); ok {
		t.Fatal("expected deleted key to be absent")
	}
}

func TestL2CacheGracefullyHandlesNilClient(t *testing.T) {
	cache := &L2Redis{}
	if cache.Set("key", "value", time.Minute) {
		t.Fatal("nil Redis client must not accept writes")
	}
	if value, ok := cache.Get("key"); ok || value != nil {
		t.Fatalf("nil Redis client must miss: value=%#v ok=%v", value, ok)
	}
	cache.Del("key")
	cache.Close()
}
