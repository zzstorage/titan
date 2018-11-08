package db

import (
	"github.com/pingcap/tidb/store/mockstore"
)

func MockDB() *DB {
	store, err := mockstore.NewMockTikvStore()
	if err != nil {
		panic(err)
	}
	redis := &RedisStore{store}
	return &DB{Namespace: "ns", ID: DBID(1), kv: redis}
}