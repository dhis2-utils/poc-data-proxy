package tracker

import "strings"

// Fields is a parsed DHIS2 "fields" parameter. The hub returns full objects; it only
// uses fields to decide which nested collections (enrollments, events, relationships,
// attributes) to include (spec 10.1).
type Fields struct {
	all     bool
	include map[string]*Fields
	exclude map[string]bool
}

// ParseFields parses a fields expression such as "*,enrollments[*,events[*]],!relationships".
func ParseFields(s string) *Fields {
	f, _ := parseFields(s, 0)
	return f
}

func parseFields(s string, i int) (*Fields, int) {
	f := &Fields{include: map[string]*Fields{}, exclude: map[string]bool{}}
	tok := strings.Builder{}
	flush := func(sub *Fields) {
		t := strings.TrimSpace(tok.String())
		tok.Reset()
		switch {
		case t == "":
		case t == "*" || t == ":all" || t == ":owner":
			f.all = true
		case strings.HasPrefix(t, "!"):
			f.exclude[t[1:]] = true
		default:
			// "name~rename(x)" and "name::transform" forms: keep only the name.
			if j := strings.IndexAny(t, "~:"); j > 0 {
				t = t[:j]
			}
			if sub == nil {
				sub = &Fields{all: true}
			}
			f.include[t] = sub
		}
	}
	for i < len(s) {
		c := s[i]
		switch c {
		case ',':
			flush(nil)
			i++
		case '[', '(':
			sub, j := parseFields(s, i+1)
			flush(sub)
			i = j
			// skip a following ',' handled by the loop
		case ']', ')':
			flush(nil)
			return f, i + 1
		default:
			tok.WriteByte(c)
			i++
		}
	}
	flush(nil)
	return f, i
}

// Has reports whether the nested collection name is requested.
func (f *Fields) Has(name string) bool {
	if f == nil || f.exclude[name] {
		return false
	}
	if _, ok := f.include[name]; ok {
		return true
	}
	return f.all
}

// Sub returns the field selection for a nested collection.
func (f *Fields) Sub(name string) *Fields {
	if f == nil {
		return nil
	}
	if sub, ok := f.include[name]; ok {
		return sub
	}
	if f.all {
		return &Fields{all: true}
	}
	return nil
}

// Default field selections per collection in DHIS2 2.42 when "fields" is absent.
var defaultFields = map[string]string{
	"trackedEntities": "*,!relationships,!enrollments,!events,!programOwners",
	"enrollments":     "*,!relationships,!events,!attributes",
	"events":          "*,!relationships",
	"relationships":   "relationship,relationshipType,from[trackedEntity[trackedEntity],enrollment[enrollment],event[event]],to[trackedEntity[trackedEntity],enrollment[enrollment],event[event]]",
}

func FieldsOrDefault(collection, s string) *Fields {
	if strings.TrimSpace(s) == "" {
		s = defaultFields[collection]
	}
	return ParseFields(s)
}
