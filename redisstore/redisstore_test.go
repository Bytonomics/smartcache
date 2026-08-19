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

// fakeConn implements redisstore.RedisConn for testing.
type fakeConn struct {
	getVal         string
	getErr         error
	setErr         error
	delErr         error
	existsN        int64
	existsErr      error
	lastSetKey     string
	lastSetVal     any
	lastSetTTL     time.Duration
	mgetVals       []interface{}
	mgetErr        error
	evalResult     any
	evalErr        error
	lastEvalScript string
	lastEvalKeys   []string
	lastEvalArgs   []any
}

var _ redisstore.RedisConn = (*fakeConn)(nil)

func (f *fakeConn) Get(ctx context.Context, key string) *redis.StringCmd {
	return redis.NewStringResult(f.getVal, f.getErr)
}

func (f *fakeConn) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	f.lastSetKey = key
	f.lastSetVal = value
	f.lastSetTTL = ttl
	return redis.NewStatusResult("OK", f.setErr)
}

func (f *fakeConn) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	return redis.NewIntResult(1, f.delErr)
}

func (f *fakeConn) Exists(ctx context.Context, keys ...string) *redis.IntCmd {
	return redis.NewIntResult(f.existsN, f.existsErr)
}

func (f *fakeConn) MGet(ctx context.Context, keys ...string) *redis.SliceCmd {
	return redis.NewSliceResult(f.mgetVals, f.mgetErr)
}

func (f *fakeConn) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	f.lastEvalScript = script
	f.lastEvalKeys = keys
	f.lastEvalArgs = args
	return redis.NewCmdResult(f.evalResult, f.evalErr)
}

func TestGet_Present(t *testing.T) {
	ctx := context.Background()
	fc := &fakeConn{getVal: "payload"}
	st := redisstore.New(fc)

	b, err := st.Get(ctx, "k")

	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if string(b) != "payload" {
		t.Errorf("expected 'payload', got %q", string(b))
	}
}

func TestGet_NilMapsToStoreMiss(t *testing.T) {
	ctx := context.Background()
	fc := &fakeConn{getErr: redis.Nil}
	st := redisstore.New(fc)

	b, err := st.Get(ctx, "k")

	if !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("expected ErrStoreMiss, got %v", err)
	}
	if b != nil {
		t.Errorf("expected nil bytes, got %v", b)
	}
}

func TestGet_OtherErrorPropagates(t *testing.T) {
	ctx := context.Background()
	fc := &fakeConn{getErr: errors.New("conn refused")}
	st := redisstore.New(fc)

	_, err := st.Get(ctx, "k")

	if err == nil {
		t.Error("expected error, got nil")
	}
	if errors.Is(err, smartcache.ErrStoreMiss) {
		t.Error("should not be ErrStoreMiss")
	}
}

func TestSet_PassThrough(t *testing.T) {
	ctx := context.Background()
	fc := &fakeConn{}
	st := redisstore.New(fc)

	err := st.Set(ctx, "k", []byte("v"), 5*time.Second)

	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if fc.lastSetKey != "k" {
		t.Errorf("expected key 'k', got %q", fc.lastSetKey)
	}
	if fc.lastSetTTL != 5*time.Second {
		t.Errorf("expected TTL 5s, got %v", fc.lastSetTTL)
	}
}

func TestSet_ErrorPropagates(t *testing.T) {
	ctx := context.Background()
	fc := &fakeConn{setErr: errors.New("x")}
	st := redisstore.New(fc)

	err := st.Set(ctx, "k", []byte("v"), time.Second)

	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()

	// Success case
	fc := &fakeConn{}
	st := redisstore.New(fc)
	err := st.Delete(ctx, "k")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}

	// Error case
	fc2 := &fakeConn{delErr: errors.New("x")}
	st2 := redisstore.New(fc2)
	err = st2.Delete(ctx, "k")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestExists(t *testing.T) {
	ctx := context.Background()

	// Key exists
	fc := &fakeConn{existsN: 1}
	st := redisstore.New(fc)
	ok, err := st.Exists(ctx, "k")
	if ok != true {
		t.Errorf("expected true, got %v", ok)
	}
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}

	// Key does not exist
	fc2 := &fakeConn{existsN: 0}
	st2 := redisstore.New(fc2)
	ok, err = st2.Exists(ctx, "k")
	if ok != false {
		t.Errorf("expected false, got %v", ok)
	}
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}

	// Error case
	fc3 := &fakeConn{existsErr: errors.New("x")}
	st3 := redisstore.New(fc3)
	_, err = st3.Exists(ctx, "k")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestGetMany_MapsPresentOmitsNil(t *testing.T) {
	ctx := context.Background()
	fc := &fakeConn{mgetVals: []interface{}{"A", nil, "C"}}
	bs, ok := redisstore.New(fc).(smartcache.BatchCacheStore)
	if !ok {
		t.Fatal("redisstore must implement smartcache.BatchCacheStore")
	}
	got, err := bs.GetMany(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if string(got["a"]) != "A" || string(got["c"]) != "C" {
		t.Errorf("values: %v", got)
	}
	if _, present := got["b"]; present {
		t.Error("nil (absent) element must be omitted")
	}
}

func TestGetMany_ErrorPropagates(t *testing.T) {
	fc := &fakeConn{mgetErr: errors.New("mget boom")}
	bs := redisstore.New(fc).(smartcache.BatchCacheStore)
	if _, err := bs.GetMany(context.Background(), []string{"a"}); err == nil {
		t.Error("expected error from MGet")
	}
}

func TestGetMany_Empty(t *testing.T) {
	fc := &fakeConn{}
	bs := redisstore.New(fc).(smartcache.BatchCacheStore)
	got, err := bs.GetMany(context.Background(), nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty map, got %v", got)
	}
}
