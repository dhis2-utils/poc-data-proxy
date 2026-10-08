package tracker

import (
	"encoding/json"
	"log/slog"

	"github.com/dhis2/facility-hub/internal/store"
)

// Objects in a loaded index are held slim: top-level scalar fields (identifiers,
// status, dates), a tracked entity's attribute values and a relationship's ends, which
// is what queries filter and sort on. Rendering and merging read the full payload
// from the store. A decoded event with its data values took about 6 KB in memory
// (docs/PERFORMANCE.md).

func slimmed(o *Obj) *Obj {
	n := *o
	d := M{}
	for k, v := range o.Data {
		switch v.(type) {
		case string, bool, float64, json.Number, nil:
			d[k] = v
		}
	}
	switch o.Kind {
	case store.KindTrackedEntity:
		var attrs []any
		for _, a := range anyList(o.Data["attributes"]) {
			if m, ok := a.(M); ok {
				attrs = append(attrs, M{"attribute": m["attribute"], "value": m["value"]})
			}
		}
		d["attributes"] = attrs
	case store.KindRelationship:
		d["from"], d["to"] = o.Data["from"], o.Data["to"]
	}
	n.Data, n.slim = d, true
	return &n
}

// Full returns the object's full payload: from the store for a slim object. Treat it
// as read-only.
func (ix *Index) Full(o *Obj) M {
	if o == nil || !o.slim {
		if o == nil {
			return nil
		}
		return o.Data
	}
	so, err := ix.store.GetObject(o.UID)
	if err != nil || so == nil {
		slog.Warn("payload not readable; using the index copy", "uid", o.UID, "err", err)
		return o.Data
	}
	var d M
	if err := json.Unmarshal(so.Payload, &d); err != nil {
		return o.Data
	}
	return d
}
