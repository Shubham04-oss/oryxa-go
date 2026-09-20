package rooms

import (
	"context"
	"fmt"
)

// catchupMessageCap bounds how many unseen messages [Catchup] hands back.
// It sits in the same range as compactionBackstop: in a healthy room,
// compaction keeps the tail after a brief's ThroughSeq under that many
// messages anyway, so this cap mostly guards the unhealthy case —
// compaction stuck or failing and the brief no longer advancing. When a
// person is further behind than the cap, Catchup does not try to hand back
// everything they missed: whatever is older than what it returns is
// exactly what the brief exists to cover, so filling that gap is the
// brief's job, not this function's.
const catchupMessageCap = 200

// Catchup is what a person who has been away is handed: the room's brief,
// plus the messages it does not yet account for. sinceSeq is the seq of the
// last message they read.
//
// Messages are fetched from whichever is later, sinceSeq or the brief's own
// ThroughSeq — never from sinceSeq alone. In the ordinary case the brief is
// caught up (ThroughSeq >= sinceSeq), so that means fetching from
// ThroughSeq: everything between the two seqs is exactly what the brief
// already consolidates, so it is never handed back a second time as raw
// messages, no matter how long the person was away.
//
// The brief can also be the one running behind: if compaction has not run
// since the person last read, ThroughSeq < sinceSeq, and the brief
// describes an earlier point than they already reached on their own.
// Catchup does not paper over that by advancing ThroughSeq or attaching a
// note — there is nowhere on [Brief] to put one — it returns the brief
// exactly as the store holds it, so ThroughSeq < sinceSeq stays visible to
// whatever reads these fields next, rather than quietly presenting a stale
// brief as though it covered everything up to where the person actually
// left off.
func Catchup(ctx context.Context, store Store, roomID string, sinceSeq int64) (Brief, []Message, error) {
	if store == nil {
		return Brief{}, nil, fmt.Errorf("rooms: catchup needs a store")
	}
	brief, err := store.Brief(ctx, roomID)
	if err != nil {
		return Brief{}, nil, fmt.Errorf("rooms: catchup: load brief: %w", err)
	}
	from := sinceSeq
	if brief.ThroughSeq > from {
		from = brief.ThroughSeq
	}
	messages, err := store.Messages(ctx, roomID, from, catchupMessageCap)
	if err != nil {
		return Brief{}, nil, fmt.Errorf("rooms: catchup: load messages: %w", err)
	}
	return brief, messages, nil
}
