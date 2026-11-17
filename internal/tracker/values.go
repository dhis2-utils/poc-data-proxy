package tracker

import (
	"fmt"

	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/valuetype"
)

// checkValueTypes refuses number values central would refuse at the push, with
// central's codes and messages (2.43.2.1): E1007 for an attribute, E1302 for a data
// value. Before, the hub confirmed them offline and central refused them later (I8c).
// Only the number types are checked (valuetype); values that are not JSON strings,
// and empty ones (a value cleared), are left to central.
func (im *Importer) checkValueTypes(it *Item) []ErrorReport {
	if im.V.Catalog == nil {
		return nil
	}
	listKey, idKey, code, format := "attributes", "attribute", "E1007",
		"Error validating attribute value type: `%s`; Error: `Value type is %s but the value `%s` is not.`."
	if it.Kind == store.KindEvent {
		listKey, idKey, code, format = "dataValues", "dataElement", "E1302",
			"DataElement `%s` is not valid: `Value type is %s but the value `%s` is not.`."
	}
	var errs []ErrorReport
	for _, a := range list(it.Data, listKey) {
		v, ok := a["value"].(string)
		vt := im.V.Catalog.ValueTypes[Str(a, idKey)]
		if !ok || v == "" || !valuetype.Numeric(vt) || valuetype.Valid(vt, v) {
			continue
		}
		errs = append(errs, ErrorReport{Message: fmt.Sprintf(format, vt, vt, v), ErrorCode: code,
			TrackerType: TrackerType(it.Kind), UID: it.UID})
	}
	return errs
}
