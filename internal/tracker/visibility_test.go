package tracker

import (
	"testing"

	"github.com/dhis2/facility-hub/internal/store"
)

func TestBitmapChunksRoundTrip(t *testing.T) {
	slots := []uint32{1, 63, 64, 4095, 4096, 4097, 100000}
	changes := map[uint32]bool{}
	for _, s := range slots {
		changes[s] = true
	}
	b := bitmap(nil).with(changes)
	chunks := map[int64][]byte{}
	for _, s := range slots {
		c := int64(s) / store.VisibilityChunkBits
		chunks[c] = b.chunk(c)
	}
	back := bitmapFromChunks(chunks)
	for _, s := range slots {
		if !back.has(s) {
			t.Errorf("slot %d lost", s)
		}
	}
	for _, s := range []uint32{0, 2, 65, 4098, 99999, 200000} {
		if back.has(s) {
			t.Errorf("slot %d set", s)
		}
	}
	off := back.with(map[uint32]bool{4096: false, 300000: false})
	if off.has(4096) || !off.has(4097) || !back.has(4096) {
		t.Error("clearing a bit changed the wrong bitmap or bit")
	}
}

// A change is invisible until it commits, and a failed one leaves nothing behind.
func TestVisibilityChangeAppliesOnCommit(t *testing.T) {
	ix := NewStoreIndex(nil)
	o := &Obj{UID: "TeAaaaaaaa1", Kind: store.KindTrackedEntity, Origin: store.OriginCentral, Slot: ix.SlotFor("TeAaaaaaaa1")}
	vc := ix.PrepareVisibility(7, map[uint32]bool{o.Slot: true})
	if ix.VisibleTo(o, 7) || len(vc.Chunks[7]) != 1 {
		t.Fatal("visible before commit, or no chunk to write")
	}
	vc.Done(false)
	if ix.VisibleTo(o, 7) {
		t.Fatal("visible after a failed commit")
	}
	ix.PrepareVisibility(7, map[uint32]bool{o.Slot: true}).Done(true)
	if !ix.VisibleTo(o, 7) || ix.VisibleTo(o, 8) {
		t.Fatal("commit not applied to exactly that user")
	}
}
