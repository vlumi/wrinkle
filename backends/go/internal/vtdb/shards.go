package vtdb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/cespare/xxhash/v2"
)

// KeyspaceID is where the xxhash vindex places an id: its 64-bit xxhash,
// little-endian.
func KeyspaceID(id []byte) []byte {
	return binary.LittleEndian.AppendUint64(nil, xxhash.Sum64(id))
}

// Shards maps ids sharded by the xxhash vindex to the shards of one
// keyspace, so work can be grouped into one transaction per shard. The zero
// value, or a keyspace that is not sharded, puts every id in one shard.
type Shards struct {
	names  []string
	starts [][]byte // each shard's first keyspace id, ascending; empty for the first
}

// LoadShards reads the keyspace's shards from vtgate. A server that is not
// vtgate gives one shard.
func LoadShards(ctx context.Context, db *sql.DB, keyspace string) (Shards, error) {
	rows, err := db.QueryContext(ctx, `SHOW VITESS_SHARDS`)
	if err != nil {
		return Shards{}, nil
	}
	defer rows.Close()
	var s Shards
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return Shards{}, err
		}
		ks, shard, ok := strings.Cut(name, "/")
		if !ok || ks != keyspace {
			continue
		}
		start, _, _ := strings.Cut(shard, "-")
		b, err := hex.DecodeString(start)
		if err != nil {
			return Shards{}, fmt.Errorf("shard %q: %w", name, err)
		}
		i, _ := slices.BinarySearchFunc(s.starts, b, bytes.Compare)
		s.names = slices.Insert(s.names, i, shard)
		s.starts = slices.Insert(s.starts, i, b)
	}
	return s, rows.Err()
}

// Names returns the shards' names in keyspace order; none when the keyspace
// is not sharded.
func (s Shards) Names() []string { return s.names }

// Of returns the name of the shard holding the id.
func (s Shards) Of(id []byte) string {
	ksid := KeyspaceID(id)
	shard := ""
	for i, start := range s.starts {
		if bytes.Compare(ksid, start) >= 0 {
			shard = s.names[i]
		}
	}
	return shard
}
