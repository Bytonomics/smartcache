package redisstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/redisstore"
)

func aliasStoreFor(fc *fakeConn) smartcache.AliasCacheStore {
	return redisstore.New(fc).(smartcache.AliasCacheStore)
}

func TestRedisAlias_GetByAlias_Hit(t *testing.T) {
	fc := &fakeConn{evalResult: "VAL"}
	as := aliasStoreFor(fc)
	got, err := as.GetByAlias(context.Background(), "ptrkey")
	if err != nil || string(got) != "VAL" {
		t.Fatalf("GetByAlias: got %q err=%v", got, err)
	}
	if len(fc.lastEvalKeys) != 1 || fc.lastEvalKeys[0] != "ptrkey" {
		t.Errorf("Eval keys: got %v", fc.lastEvalKeys)
	}
	if fc.lastEvalScript == "" {
		t.Error("expected a non-empty Lua script")
	}
}

func TestRedisAlias_GetByAlias_Miss(t *testing.T) {
	fc := &fakeConn{evalErr: redis.Nil}
	as := aliasStoreFor(fc)
	if _, err := as.GetByAlias(context.Background(), "ptrkey"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("want ErrStoreMiss, got %v", err)
	}
}

func TestRedisAlias_PutByAlias_ArgsAndKeys(t *testing.T) {
	fc := &fakeConn{}
	as := aliasStoreFor(fc)
	spec := &smartcache.AliasWriteSpec{
		ValueKey:         "bc:{user}:5",
		MembersKey:       "bc:memb:{user}:5",
		PointerKey:       "bc:grp:{user}:email:ada",
		FieldPrefix:      "bc:grp:{user}:email:",
		ValueKeyPrefix:   "bc:{user}:",
		MembersKeyPrefix: "bc:memb:{user}:",
		Value:            []byte("VAL"),
		TTL:              time.Minute,
	}
	if err := as.PutByAlias(context.Background(), spec); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	if len(fc.lastEvalKeys) != 2 || fc.lastEvalKeys[0] != "bc:{user}:5" || fc.lastEvalKeys[1] != "bc:memb:{user}:5" {
		t.Errorf("Eval keys: got %v", fc.lastEvalKeys)
	}
	if len(fc.lastEvalArgs) != 6 {
		t.Fatalf("Eval args len: got %d want 6 (%v)", len(fc.lastEvalArgs), fc.lastEvalArgs)
	}
	if got := fc.lastEvalArgs[1]; got != int64(60000) {
		t.Errorf("ttlMillis arg: got %v want int64(60000)", got)
	}
	if got := fc.lastEvalArgs[2]; got != "bc:grp:{user}:email:ada" {
		t.Errorf("pointerKey arg: got %v", got)
	}
}

func TestRedisAlias_EvictByPrimary_Keys(t *testing.T) {
	fc := &fakeConn{}
	as := aliasStoreFor(fc)
	if err := as.EvictByPrimary(context.Background(), "bc:{user}:5", "bc:memb:{user}:5"); err != nil {
		t.Fatalf("EvictByPrimary: %v", err)
	}
	if len(fc.lastEvalKeys) != 2 || fc.lastEvalKeys[0] != "bc:{user}:5" || fc.lastEvalKeys[1] != "bc:memb:{user}:5" {
		t.Errorf("Eval keys: got %v", fc.lastEvalKeys)
	}
}

func TestRedisAlias_EvictByAlias_KeysAndArgs(t *testing.T) {
	fc := &fakeConn{}
	as := aliasStoreFor(fc)
	if err := as.EvictByAlias(context.Background(), "bc:grp:{user}:email:ada", "bc:{user}:", "bc:memb:{user}:"); err != nil {
		t.Fatalf("EvictByAlias: %v", err)
	}
	if len(fc.lastEvalKeys) != 1 || fc.lastEvalKeys[0] != "bc:grp:{user}:email:ada" {
		t.Errorf("Eval keys: got %v", fc.lastEvalKeys)
	}
	if len(fc.lastEvalArgs) != 2 || fc.lastEvalArgs[0] != "bc:{user}:" || fc.lastEvalArgs[1] != "bc:memb:{user}:" {
		t.Errorf("Eval args: got %v", fc.lastEvalArgs)
	}
}
