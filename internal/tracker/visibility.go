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
// it on an object before storing it.
func (ix *Index) SlotFor(uid string) uint32 {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.slotFor(uid)
}

func (ix *Index) slotFor(uid string) uint32 {
	if o := ix.objs[uid]; o != nil && o.Slot != 0 {
		return o.Slot
	}
	if s, ok := ix.pending[uid]; ok {
		return s
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

// VisibilityChange is a pending change to one user's visibility. Prepare it before
// starting the transaction that stores it (the lock order is visibility, then
// database), write Chunks in that transaction, then call Done with whether it
// committed.
type VisibilityChange struct {
	ix     *Index
	user   int64
	next   bitmap
	Chunks map[int64][]byte // chunk -> new contents, only the chunks that change
}

// PrepareVisibility starts a change: changes maps slots to visible or not.
// Changes for all users are serialised until Done.
func (ix *Index) PrepareVisibility(userID int64, changes map[uint32]bool) *VisibilityChange {
	ix.visMu.Lock()
	ix.mu.RLock()
	cur := ix.vis[userID]
	ix.mu.RUnlock()
	c := &VisibilityChange{ix: ix, user: userID, next: cur.with(changes), Chunks: map[int64][]byte{}}
	for slot, on := range changes {
		if cur.has(slot) == on {
			continue
		}
		ch := int64(slot) / store.VisibilityChunkBits
		if _, ok := c.Chunks[ch]; !ok {
			c.Chunks[ch] = c.next.chunk(ch)
		}
	}
	return c
}

// Write stores the changed chunks.
func (c *VisibilityChange) Write(s *store.Store, q store.Execer) error {
	for ch, b := range c.Chunks {
		if err := s.PutVisibilityChunk(q, c.user, ch, b); err != nil {
			return err
		}
	}
	return nil
}

// Done applies the change in memory if it was stored, and lets the next one start.
func (c *VisibilityChange) Done(committed bool) {
	if committed {
		c.ix.mu.Lock()
		c.ix.vis[c.user] = c.next
		c.ix.mu.Unlock()
	}
	c.ix.visMu.Unlock()
}
