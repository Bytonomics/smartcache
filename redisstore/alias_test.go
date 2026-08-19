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

func opsFor(fc *fakeConn, mode smartcache.AliasMode) smartcache.AliasOps {
	return redisstore.New(fc).(smartcache.AliasCacheStore).AliasGroup("user", mode)
}

// --- Colocated: one Eval per op, keys/args exact ---

func TestColocated_PutByAlias_EvalShape(t *testing.T) {
	fc := &fakeConn{}
	o := opsFor(fc, smartcache.AliasColocated)
	if err := o.PutByAlias(context.Background(), "5", smartcache.AliasRef{Field: "email", Value: "ada"}, []byte("V"), time.Minute); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	if len(fc.evalCalls) != 1 {
		t.Fatalf("want 1 Eval, got %d", len(fc.evalCalls))
	}
	e := fc.evalCalls[0]
	if len(e.keys) != 2 || e.keys[0] != "bc:{user}:5" || e.keys[1] != "bc:memb:{user}:5" {
		t.Errorf("keys: %v", e.keys)
	}
	if len(e.args) != 7 || e.args[1] != int64(60000) || e.args[2] != "email" || e.args[3] != "ada" || e.args[4] != "5" || e.args[5] != "bc:grp:{user}:" || e.args[6] != "bc:memb:{user}:" {
		t.Errorf("args: %v", e.args)
	}
}

func TestColocated_GetByAlias_EvalShape(t *testing.T) {
	fc := &fakeConn{evalResult: "VAL"}
	o := opsFor(fc, smartcache.AliasColocated)
	b, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"})
	if err != nil || string(b) != "VAL" {
		t.Fatalf("GetByAlias: %q err=%v", b, err)
	}
	e := fc.evalCalls[len(fc.evalCalls)-1]
	if len(e.keys) != 1 || e.keys[0] != "bc:grp:{user}:email:ada" {
		t.Errorf("keys: %v", e.keys)
	}
	if len(e.args) != 1 || e.args[0] != "bc:{user}:" {
		t.Errorf("args: %v", e.args)
	}
}

func TestColocated_GetByAlias_Miss(t *testing.T) {
	fc := &fakeConn{evalErr: redis.Nil}
	o := opsFor(fc, smartcache.AliasColocated)
	if _, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"}); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("want ErrStoreMiss, got %v", err)
	}
}

func TestColocated_EvictByPrimary_EvalShape(t *testing.T) {
	fc := &fakeConn{}
	o := opsFor(fc, smartcache.AliasColocated)
	if err := o.EvictByPrimary(context.Background(), "5"); err != nil {
		t.Fatalf("EvictByPrimary: %v", err)
	}
	e := fc.evalCalls[len(fc.evalCalls)-1]
	if len(e.keys) != 2 || e.keys[0] != "bc:{user}:5" || e.keys[1] != "bc:memb:{user}:5" {
		t.Errorf("keys: %v", e.keys)
	}
	if len(e.args) != 1 || e.args[0] != "bc:grp:{user}:" {
		t.Errorf("args: %v", e.args)
	}
}

func TestColocated_EvictByAlias_EvalShape(t *testing.T) {
	fc := &fakeConn{}
	o := opsFor(fc, smartcache.AliasColocated)
	if err := o.EvictByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"}); err != nil {
		t.Fatalf("EvictByAlias: %v", err)
	}
	e := fc.evalCalls[len(fc.evalCalls)-1]
	if len(e.keys) != 1 || e.keys[0] != "bc:grp:{user}:email:ada" {
		t.Errorf("keys: %v", e.keys)
	}
	if len(e.args) != 3 || e.args[0] != "bc:{user}:" || e.args[1] != "bc:memb:{user}:" || e.args[2] != "bc:grp:{user}:" {
		t.Errorf("args: %v", e.args)
	}
}

// --- Sharded: multi-step sequences ---

func TestSharded_GetByAlias_ResolveThenValidate(t *testing.T) {
	fc := &fakeConn{getVal: "5", evalResult: "VAL"} // Get(pointer)->pk "5"; validate Eval->"VAL"
	o := opsFor(fc, smartcache.AliasSharded)
	b, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"})
	if err != nil || string(b) != "VAL" {
		t.Fatalf("GetByAlias: %q err=%v", b, err)
	}
	if len(fc.evalCalls) != 1 {
		t.Fatalf("want 1 validate Eval, got %d", len(fc.evalCalls))
	}
	e := fc.evalCalls[0]
	if len(e.keys) != 2 || e.keys[0] != "bc:{user:5}" || e.keys[1] != "bc:memb:{user:5}" {
		t.Errorf("validate keys: %v", e.keys)
	}
	if len(e.args) != 2 || e.args[0] != "email" || e.args[1] != "ada" {
		t.Errorf("validate args: %v", e.args)
	}
}

func TestSharded_GetByAlias_PointerMiss(t *testing.T) {
	fc := &fakeConn{getErr: redis.Nil}
	o := opsFor(fc, smartcache.AliasSharded)
	if _, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"}); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("want ErrStoreMiss, got %v", err)
	}
	if len(fc.evalCalls) != 0 {
		t.Errorf("pointer miss must not issue a validate Eval, got %d", len(fc.evalCalls))
	}
}

func TestSharded_PutByAlias_RecordThenPointer(t *testing.T) {
	fc := &fakeConn{}
	o := opsFor(fc, smartcache.AliasSharded)
	if err := o.PutByAlias(context.Background(), "5", smartcache.AliasRef{Field: "email", Value: "ada"}, []byte("V"), time.Minute); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	if len(fc.evalCalls) != 1 {
		t.Fatalf("want 1 record Eval, got %d", len(fc.evalCalls))
	}
	e := fc.evalCalls[0]
	if len(e.keys) != 2 || e.keys[0] != "bc:{user:5}" || e.keys[1] != "bc:memb:{user:5}" {
		t.Errorf("record keys: %v", e.keys)
	}
	if len(e.args) != 4 || e.args[1] != int64(60000) || e.args[2] != "email" || e.args[3] != "ada" {
		t.Errorf("record args: %v", e.args)
	}
	if len(fc.setCalls) != 1 || fc.setCalls[0].key != "bc:grp:{user:email:ada}" || fc.setCalls[0].val != "5" {
		t.Errorf("pointer Set: %+v", fc.setCalls)
	}
}

func TestSharded_EvictByPrimary_EvictThenCompareDelete(t *testing.T) {
	// record_evict Eval returns the members HASH flat array; every Eval returns evalResult here.
	fc := &fakeConn{evalResult: []any{"email", "ada"}}
	o := opsFor(fc, smartcache.AliasSharded)
	if err := o.EvictByPrimary(context.Background(), "5"); err != nil {
		t.Fatalf("EvictByPrimary: %v", err)
	}
	if len(fc.evalCalls) != 2 {
		t.Fatalf("want record-evict + 1 compare-delete Eval, got %d", len(fc.evalCalls))
	}
	rec := fc.evalCalls[0]
	if len(rec.keys) != 2 || rec.keys[0] != "bc:{user:5}" || rec.keys[1] != "bc:memb:{user:5}" {
		t.Errorf("record-evict keys: %v", rec.keys)
	}
	cdel := fc.evalCalls[1]
	if len(cdel.keys) != 1 || cdel.keys[0] != "bc:grp:{user:email:ada}" {
		t.Errorf("compare-delete keys: %v", cdel.keys)
	}
	if len(cdel.args) != 1 || cdel.args[0] != "5" {
		t.Errorf("compare-delete args (expected pk): %v", cdel.args)
	}
}

func TestSharded_EvictByAlias_ResolvesAndEvicts(t *testing.T) {
	fc := &fakeConn{getVal: "5", evalResult: []any{"email", "ada"}}
	o := opsFor(fc, smartcache.AliasSharded)
	if err := o.EvictByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"}); err != nil {
		t.Fatalf("EvictByAlias: %v", err)
	}
	if len(fc.evalCalls) != 2 {
		t.Fatalf("want record-evict + 1 compare-delete Eval, got %d", len(fc.evalCalls))
	}
	rec := fc.evalCalls[0]
	if len(rec.keys) != 2 || rec.keys[0] != "bc:{user:5}" || rec.keys[1] != "bc:memb:{user:5}" {
		t.Errorf("record-evict keys: %v", rec.keys)
	}
}

func TestSharded_EvictByAlias_PointerMiss(t *testing.T) {
	fc := &fakeConn{getErr: redis.Nil}
	o := opsFor(fc, smartcache.AliasSharded)
	if err := o.EvictByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"}); err != nil {
		t.Fatalf("EvictByAlias on pointer miss must be a no-op, got: %v", err)
	}
	if len(fc.evalCalls) != 0 {
		t.Errorf("pointer miss must not issue any Eval, got %d", len(fc.evalCalls))
	}
}

func TestSharded_EvictByAlias_ResolveError(t *testing.T) {
	fc := &fakeConn{getErr: errors.New("conn refused")}
	o := opsFor(fc, smartcache.AliasSharded)
	if err := o.EvictByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"}); err == nil {
		t.Error("expected a transport error to propagate")
	}
}

func TestColocated_GetValue_Hit(t *testing.T) {
	fc := &fakeConn{getVal: "VAL"}
	o := opsFor(fc, smartcache.AliasColocated)
	b, err := o.GetValue(context.Background(), "5")
	if err != nil || string(b) != "VAL" {
		t.Fatalf("GetValue: %q err=%v", b, err)
	}
}

func TestColocated_GetValue_Miss(t *testing.T) {
	fc := &fakeConn{getErr: redis.Nil}
	o := opsFor(fc, smartcache.AliasColocated)
	if _, err := o.GetValue(context.Background(), "5"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("want ErrStoreMiss, got %v", err)
	}
}

func TestSharded_GetValue_Hit(t *testing.T) {
	fc := &fakeConn{getVal: "VAL"}
	o := opsFor(fc, smartcache.AliasSharded)
	b, err := o.GetValue(context.Background(), "5")
	if err != nil || string(b) != "VAL" {
		t.Fatalf("GetValue: %q err=%v", b, err)
	}
}

func TestSharded_GetValue_Miss(t *testing.T) {
	fc := &fakeConn{getErr: redis.Nil}
	o := opsFor(fc, smartcache.AliasSharded)
	if _, err := o.GetValue(context.Background(), "5"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("want ErrStoreMiss, got %v", err)
	}
}

func TestColocated_GetByAlias_BytesResult(t *testing.T) {
	fc := &fakeConn{evalResult: []byte("VAL")}
	o := opsFor(fc, smartcache.AliasColocated)
	b, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"})
	if err != nil || string(b) != "VAL" {
		t.Fatalf("GetByAlias: %q err=%v", b, err)
	}
}

func TestSharded_GetByAlias_BytesResult(t *testing.T) {
	fc := &fakeConn{getVal: "5", evalResult: []byte("VAL")}
	o := opsFor(fc, smartcache.AliasSharded)
	b, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "ada"})
	if err != nil || string(b) != "VAL" {
		t.Fatalf("GetByAlias: %q err=%v", b, err)
	}
}
