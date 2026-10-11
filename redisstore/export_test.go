package redisstore

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// ScanNodeForTest exposes scanNode to the black-box tests. Type aliases are not allowed in this
// repository, so the node scanner is taken as an inline interface.
func ScanNodeForTest(ctx context.Context, s interface {
	Scan(context.Context, uint64, string, int64) *redis.ScanCmd
}, match string, fn func([]string) error) error {
	return scanNode(ctx, s, match, fn)
}

// GlobLiteralForTest exposes globLiteral to the black-box tests.
var GlobLiteralForTest = globLiteral
