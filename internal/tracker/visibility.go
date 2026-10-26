package tracker

import (
	"encoding/binary"
	"slices"

	"github.com/dhis2/facility-hub/internal/store"
)

// Visibility of central-origin objects is one bitmap per user over object slots:
// a bit says central showed this object to this user (spec 8.2, 11.3). With 50 users
// and 750 000 objects that is under 5 MB, against one map entry per object and user
// before (docs/PERFORMANCE.md).

type bitmap []uint64

func (b bitmap) has(slot uint32) bool {
	w := int(slot / 64)
	return w < len(b) && b[w]&(1<<(slot%64)) != 0
}

func (b bitmap) with(changes map[uint32]bool) bitmap {
	n := slices.Clone(b)
	for slot, on := range changes {
		w := int(slot / 64)
		if w >= len(n) {
			if !on {
				continue
			}
			n = append(n, make([]uint64, w-len(n)+1)...)
		}
		if on {
			n[w] |= 1 << (slot % 64)
		} else {
			n[w] &^= 1 << (slot % 64)
		}
	}
	return n
}

const chunkWords = store.VisibilityChunkBits / 64

// chunk returns the stored form of chunk c: its words, little-endian.
func (b bitmap) chunk(c int64) []byte {
	out := make([]byte, chunkWords*8)
	for i := 0; i < chunkWords; i++ {
		if w := int(c)*chunkWords + i; w < len(b) {
			binary.LittleEndian.PutUint64(out[i*8:], b[w])
		}
	}
	return out
}

func bitmapFromChunks(chunks map[int64][]byte) bitmap {
	var b bitmap
	for c, bits := range chunks {
		for i := 0; i+8 <= len(bits) && i/8 < chunkWords; i += 8 {
			w := int(c)*chunkWords + i/8
			if v := binary.LittleEndian.Uint64(bits[i:]); v != 0 {
				if w >= len(b) {
					b = append(b, make([]uint64, w-len(b)+1)...)
				}
				b[w] = v
			}
		}
	}
	return b
}

// SlotFor returns the object's slot, allocating one for an object not yet stored. Set
// it on an object before storing it; an object read from the index already has its own.
func (ix *Index) SlotFor(uid string) uint32 {
	ix.slotMu.Lock()
	defer ix.slotMu.Unlock()
	if s, ok := ix.pending[uid]; ok {
		return s
	}
	// Looked up while slots are held, so a slot cannot be handed out twice for one
	// object: a pending slot is dropped only after its object is stored (Put).
	if ix.store != nil && ix.store.Unlocked() {
		if s := ix.store.SlotOf(uid); s > 0 {
			return uint32(s)
		}
	}
	ix.nextSlot++
	ix.pending[uid] = ix.nextSlot
	return ix.nextSlot
}

// bitmapOf returns the user's bitmap. Bitmaps are replaced, never changed in place,
// so the caller may read it without the lock.
func (ix *Index) bitmapOf(userID int64) bitmap {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.vis[userID]
}

// VisibleTo reports whether central showed the object to the user.
func (ix *Index) VisibleTo(o *Obj, userID int64) bool {
	if o == nil || o.Slot == 0 {
		return false
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.vis[userID].has(o.Slot)
}

// VisibilityChange is a pending change to the visibility of one or more users.
// Prepare it before starting the transaction that stores it (the lock order is
// visibility, then database), write it in that transaction, then call Done with
// whether it committed. Several users' changes go in one transaction: an object
// shared with every user cost a transaction per user, each flushed to disk.
type VisibilityChange struct {
	ix     *Index
	next   map[int64]bitmap
	Chunks map[int64]map[int64][]byte // user -> chunk -> new contents, only the chunks that change
}

// PrepareVisibility starts a change for one user: changes maps slots to visible or
// not. Changes for all users are serialised until Done.
func (ix *Index) PrepareVisibility(userID int64, changes map[uint32]bool) *VisibilityChange {
	return ix.PrepareVisibilities(map[int64]map[uint32]bool{userID: changes})
}

// PrepareVisibilities is PrepareVisibility for several users.
func (ix *Index) PrepareVisibilities(changes map[int64]map[uint32]bool) *VisibilityChange {
	ix.visMu.Lock()
	c := &VisibilityChange{ix: ix, next: map[int64]bitmap{}, Chunks: map[int64]map[int64][]byte{}}
	for user, slots := range changes {
		ix.mu.RLock()
		cur := ix.vis[user]
		ix.mu.RUnlock()
		next := cur.with(slots)
		c.next[user], c.Chunks[user] = next, map[int64][]byte{}
		for slot, on := range slots {
			if cur.has(slot) == on {
				continue
			}
			ch := int64(slot) / store.VisibilityChunkBits
			if _, ok := c.Chunks[user][ch]; !ok {
				c.Chunks[user][ch] = next.chunk(ch)
			}
		}
	}
	return c
}

// Write stores the changed chunks.
func (c *VisibilityChange) Write(s *store.Store, q store.Execer) error {
	for user, chunks := range c.Chunks {
		for ch, b := range chunks {
			if err := s.PutVisibilityChunk(q, user, ch, b); err != nil {
				return err
			}
		}
	}
	return nil
}

// Done applies the change in memory if it was stored, and lets the next one start.
func (c *VisibilityChange) Done(committed bool) {
	if committed {
		c.ix.mu.Lock()
		for user, next := range c.next {
			c.ix.vis[user] = next
		}
		c.ix.mu.Unlock()
	}
	c.ix.visMu.Unlock()
}
