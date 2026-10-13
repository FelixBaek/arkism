package memory

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Rehearsal check for the payload conversion: every row of a disposable copy
// must decode with the driver-free codec. Opt-in via STORE_CODEC_COPY_DSN.
func TestStoreCodecDecodesEveryCopiedRow(t *testing.T) {
	dsn := os.Getenv("STORE_CODEC_COPY_DSN")
	if dsn == "" {
		t.Skip("STORE_CODEC_COPY_DSN not set")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(context.Background(), `SELECT collection, id, payload FROM arkism.memory_records`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var col, id string
		var payload []byte
		if err = rows.Scan(&col, &id, &payload); err != nil {
			t.Fatal(err)
		}
		r, err := pgDecode(payload)
		if err != nil || r.ID != id {
			t.Fatalf("%s/%s: %v", col, id, err)
		}
		n++
	}
	t.Logf("decoded %d rows", n)
}

func TestStorableTimeMatchesWhatSQLCanCast(t *testing.T) {
	ok := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want bool
	}{{ok, true}, {time.Unix(0, 0), true}, {time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC), false}, {time.Time{}, false}, {time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false}} {
		if storableTime(c.at) != c.want || traceTime(c.at) != c.want {
			t.Fatalf("%v: want %v", c.at, c.want)
		}
	}
}
