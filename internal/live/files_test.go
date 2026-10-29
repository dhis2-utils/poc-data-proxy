package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Image and file values (docs/FINDINGS.md, "Images and files"). Central keeps the
// bytes as file resources: a client uploads the file (POST /api/fileResources), gets
// a UID and saves it as the value of an IMAGE or FILE_RESOURCE data element or
// attribute; it reads the bytes back from /api/tracker/.../file or /image. The hub
// passes all of this through online and stores none of it.
//
// These tests add an IMAGE and a FILE_RESOURCE data element to the primary program's
// first stage and an IMAGE attribute to the program, and take them out again at the
// end (the data elements and the attribute stay on central once data refers to them,
// under fixed UIDs, and are reused). HUB_FILES=1 runs them: they change the program.
// HUB_FILES_KEEP=1 leaves the fields in the program, for a run on a tablet.
const (
	fileDeImage = "HubTstDeImg"
	fileDeFile  = "HubTstDeFil"
	fileAtImage = "HubTstAtImg"
)

// TestFileValues: what a client gets today, online and offline, for images and files.
func TestFileValues(t *testing.T) {
	e, c, p, stage := fileEnv(t)
	m := e.M
	img, pdf := testPNG(t, 1100, 800), randomBytes(2_000_000)

	// Online: upload, save, read back, all through the hub.
	imgID := uploadOK(t, e, c, "photo.png", "image/png", img)
	pdfID := uploadOK(t, e, c, "report.pdf", "application/pdf", pdf)
	t.Logf("uploaded through the hub online: image %s (%d bytes), file %s (%d bytes)", imgID, len(img), pdfID, len(pdf))
	pr := fillPerson(t, e, p, m.NewPerson(p, "Fi"+tk.UID()[:6], stage))
	pr.SetAttr(fileAtImage, imgID)
	setValues(pr.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any)[0].(map[string]any)["events"].([]any)[0].(map[string]any),
		map[string]string{fileDeImage: imgID, fileDeFile: pdfID})
	if st, b := c.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
		t.Fatalf("save online through the hub: %d %.1500s", st, b)
	}
	ev := pr.Events[0]
	waitStored(t, e, imgID)
	waitStored(t, e, pdfID)
	if fr := centralFile(t, e, imgID); fr["assigned"] != true {
		t.Errorf("central: image %s not assigned after the save: %v", imgID, fr)
	}
	reads := map[string]string{
		"event file":            "/api/tracker/events/" + ev + "/dataValues/" + fileDeFile + "/file",
		"event image original":  "/api/tracker/events/" + ev + "/dataValues/" + fileDeImage + "/image?dimension=original",
		"event image small":     "/api/tracker/events/" + ev + "/dataValues/" + fileDeImage + "/image?dimension=small",
		"person image":          "/api/tracker/trackedEntities/" + pr.TE + "/attributes/" + fileAtImage + "/image?program=" + p.ID + "&dimension=small",
		"file resource (name)":  "/api/fileResources/" + pdfID,
		"file resource content": "/api/fileResources/" + pdfID + "/data",
	}
	names := []string{"event file", "event image original", "event image small", "person image", "file resource (name)", "file resource content"}
	online := map[string]int{}
	for _, n := range names {
		st, h, b := c.DoHeader("GET", reads[n])
		online[n] = st
		t.Logf("online  %-22s %d %s %d bytes", n, st, h.Get("Content-Type"), len(b))
		switch n {
		case "event file":
			if st != 200 || !bytes.Equal(b, pdf) {
				t.Errorf("online: the file read back through the hub: %d, %d bytes, want the %d uploaded", st, len(b), len(pdf))
			}
		case "event image original":
			if st != 200 || !bytes.Equal(b, img) {
				t.Errorf("online: the image read back through the hub: %d, %d bytes, want the %d uploaded", st, len(b), len(img))
			}
		case "event image small", "person image", "file resource (name)":
			if st != 200 {
				t.Errorf("online: %s through the hub: %d %.300s", n, st, b)
			}
		case "file resource content":
			// Central serves no DATA_VALUE file here (E1006), to anyone.
			if st != 403 {
				t.Errorf("online: %s: got %d, central refuses data value files there (403)", n, st)
			}
		}
	}

	// A file uploaded online, saved offline: Capture web uploads when the file is
	// picked, so the connection can go between the upload and the save.
	laterID := uploadOK(t, e, c, "later.pdf", "application/pdf", randomBytes(300_000))
	e.Sync() // the hub has the person, so events can be added offline

	e.Offline()
	for _, n := range names {
		st, h, b := c.DoHeader("GET", reads[n])
		t.Logf("offline %-22s %d %s %.120s", n, st, h.Get("X-Hub-Cache"), b)
		want := 503 // the hub keeps no files, and caches no 403
		if n == "file resource (name)" {
			want = online[n] // proxied, cached per user while online
		}
		if st != want {
			t.Errorf("offline: %s: got %d, want %d", n, st, want)
		}
	}
	st, b := upload(t, e, c, "offline.png", "image/png", testPNG(t, 200, 200), "")
	t.Logf("offline upload: %d %s", st, b)
	if st != 503 || !strings.Contains(string(b), "needs connection") {
		t.Errorf("offline upload: got %d %s, want 503 needs connection", st, b)
	}
	ev2, body2 := m.NewEvent(p, stage, pr.Enrollment)
	setValues(body2["events"].([]any)[0].(map[string]any), map[string]string{fileDeFile: laterID})
	if st, b := c.Do("POST", tk.SyncImport, body2); st != 200 {
		t.Errorf("offline save of a file uploaded online: %d %.800s", st, b)
	}
	// A value naming a file central does not have: the hub cannot tell offline.
	ev3, body3 := m.NewEvent(p, stage, pr.Enrollment)
	setValues(body3["events"].([]any)[0].(map[string]any), map[string]string{fileDeFile: tk.UID()})
	st3, b3 := c.Do("POST", tk.SyncImport, body3)
	t.Logf("offline save naming an unknown file: %d %.200s", st3, b3)

	// Back online, before the outbox is pushed, the hub answers tracker requests
	// itself (core.PassThrough): file reads still need a connection.
	e.Online()
	if !e.App.Hub.OutboxWaiting() {
		t.Fatal("outbox not waiting after offline saves")
	}
	st, _, b = c.DoHeader("GET", reads["event file"])
	t.Logf("online, outbox waiting: event file %d %.120s", st, b)
	if st != 503 {
		t.Errorf("online with the outbox waiting: event file got %d, want 503 until the push", st)
	}
	e.Sync()
	if o := centralEvent(t, e, ev2); o == nil || !hasValue(o, fileDeFile, laterID) {
		t.Errorf("push: the event saved offline with a file uploaded online is not on central with it: %v", o != nil)
	} else if fr := centralFile(t, e, laterID); fr["assigned"] != true {
		t.Errorf("push: file %s not assigned on central", laterID)
	}
	if st3 == 200 {
		codes := issueCodes(e, ev3)
		t.Logf("push of the event naming an unknown file: on central %v, issue %v", centralEvent(t, e, ev3) != nil, codes)
		if centralEvent(t, e, ev3) != nil || !slices.Contains(codes, "E1302") {
			t.Errorf("unknown file: central should refuse it with E1302, got %v", codes)
		}
	}
	st, _, _ = c.DoHeader("GET", reads["event file"])
	if st != 200 {
		t.Errorf("after the push: event file got %d, want 200", st)
	}
}

// TestFileSizes: central refuses files over max.file_upload_size (dhis.conf, 10 MB
// by default) and outside its list of file types; the hub refuses bodies over
// core.MaxBody before central sees them.
func TestFileSizes(t *testing.T) {
	e, c, _, _ := fileEnv(t)
	limit := 10_000_000
	for _, tc := range []struct {
		name, file, ctype string
		size, want        int
	}{
		{"just under central's limit", "big.pdf", "application/pdf", limit - 1000, 202},
		{"just over central's limit", "big.pdf", "application/pdf", limit + 1, 409},
		{"a type central does not take", "data.bin", "application/octet-stream", 1000, 409},
		{"over the hub's body limit", "huge.pdf", "application/pdf", core.MaxBody + 1, 413},
	} {
		start := time.Now()
		st, b := upload(t, e, c, tc.file, tc.ctype, randomBytes(tc.size), "")
		t.Logf("%-30s %9d bytes: %d in %v %.160s", tc.name, tc.size, st, time.Since(start).Round(time.Millisecond), b)
		if st != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, st, tc.want)
		}
	}
}

// TestFileUploadOffline is what offline support would mean: a file picked offline is
// kept by the hub and reaches central before the save that refers to it, and an image
// seen online can be seen offline. The hub does neither (FINDINGS.md, "Images and
// files").
func TestFileUploadOffline(t *testing.T) {
	if os.Getenv("HUB_FINDINGS") == "" {
		t.Skip("FINDING: images and files need a connection to upload and to view; the hub keeps none (HUB_FINDINGS=1 runs it)")
	}
	e, c, p, stage := fileEnv(t)
	m := e.M
	pr := fillPerson(t, e, p, m.NewPerson(p, "Fo"+tk.UID()[:6], stage))
	if st, b := c.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
		t.Fatalf("save online: %d %.800s", st, b)
	}
	e.Sync()
	e.Offline()
	img := testPNG(t, 400, 300)
	id := uploadOK(t, e, c, "offline.png", "image/png", img)
	ev, body := m.NewEvent(p, stage, pr.Enrollment)
	setValues(body["events"].([]any)[0].(map[string]any), map[string]string{fileDeImage: id})
	if st, b := c.Do("POST", tk.SyncImport, body); st != 200 {
		t.Fatalf("offline save: %d %.800s", st, b)
	}
	if st, _, b := c.DoHeader("GET", "/api/tracker/events/"+ev+"/dataValues/"+fileDeImage+"/image?dimension=original"); st != 200 || !bytes.Equal(b, img) {
		t.Errorf("offline: the image saved offline: %d", st)
	}
	e.Online()
	e.Sync()
	if o := centralEvent(t, e, ev); o == nil || !hasValue(o, fileDeImage, id) {
		t.Errorf("push: the event and its image are not on central")
	}
}

// --- helpers ------------------------------------------------------------------------

// allowCapture lets a user open Capture web for the test, if a role of theirs does not
// already (the eRegisters instance's nurses have that app only); cleanup takes it back.
func allowCapture(t *testing.T, e *tk.Env, username string) {
	var us struct {
		Users []struct {
			UserRoles []struct{ ID string } `json:"userRoles"`
		} `json:"users"`
	}
	if err := e.Central.JSON("GET", "/api/users?filter=username:eq:"+username+"&fields=userRoles%5Bid%5D", nil, &us); err != nil || len(us.Users) == 0 {
		t.Fatalf("user %s: %v", username, err)
	}
	const auth = "M_dhis-web-capture"
	var first map[string]any
	for i, r := range us.Users[0].UserRoles {
		var role map[string]any
		if err := e.Central.JSON("GET", "/api/userRoles/"+r.ID+"?fields=:owner", nil, &role); err != nil {
			t.Fatal(err)
		}
		as, _ := role["authorities"].([]any)
		if slices.Contains(as, any(auth)) || slices.Contains(as, any("ALL")) {
			return
		}
		if i == 0 {
			first = role
		}
	}
	orig, _ := json.Marshal(first)
	id := first["id"].(string)
	first["authorities"] = append(first["authorities"].([]any), auth)
	if err := e.Central.JSON("PUT", "/api/userRoles/"+id, first, nil); err != nil {
		t.Fatalf("add %s to role %s: %v", auth, id, err)
	}
	t.Cleanup(func() {
		if st, b, err := e.Central.Do("PUT", "/api/userRoles/"+id, json.RawMessage(orig)); err != nil || st/100 != 2 {
			t.Errorf("put role %s back: %d %v %.300s", id, st, err, b)
		}
	})
}

// fileEnv starts a hub on a live central with the test's file metadata in the primary
// program, set up, with the nurse's Android client known to the hub.
func fileEnv(t *testing.T) (*tk.Env, *tk.Client, tk.Program, string) {
	if os.Getenv("HUB_FILES") == "" {
		t.Skip("set HUB_FILES=1: adds image and file fields to the primary program for the test")
	}
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	p := e.M.Primary()
	stage := p.Stages[0]
	addFileMetadata(t, e, p.ID, stage)
	e.Setup()
	c := e.Android(tk.RoleNurse)
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("nurse login through the hub: %d %s", st, b)
	}
	e.Sync()
	return e, c, p, stage
}

// addFileMetadata creates the image and file fields on central and adds them to the
// program and its stage; the test's cleanup puts the program and stage back.
func addFileMetadata(t *testing.T, e *tk.Env, program, stage string) {
	pub := map[string]any{"public": "rw------"}
	de := func(id, name, vt string) map[string]any {
		return map[string]any{"id": id, "name": name, "shortName": name, "valueType": vt, "domainType": "TRACKER",
			"aggregationType": "NONE", "sharing": pub}
	}
	meta := map[string]any{
		"dataElements": []any{de(fileDeImage, "Hub test image", "IMAGE"), de(fileDeFile, "Hub test file", "FILE_RESOURCE")},
		"trackedEntityAttributes": []any{map[string]any{"id": fileAtImage, "name": "Hub test photo", "shortName": "Hub test photo",
			"valueType": "IMAGE", "aggregationType": "NONE", "sharing": pub}},
	}
	if err := e.Central.JSON("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE", meta, nil); err != nil {
		t.Fatalf("create file metadata: %v", err)
	}
	edit := func(kind, id, list string, add map[string]any, key string) {
		var obj map[string]any
		if err := e.Central.JSON("GET", "/api/"+kind+"/"+id+"?fields=:owner", nil, &obj); err != nil {
			t.Fatal(err)
		}
		// Without any of the test's fields, should an earlier run have left them there.
		var items []any
		for _, it := range obj[list].([]any) {
			if id, _ := refID(it, key).(string); !strings.HasPrefix(id, "HubTst") {
				items = append(items, it)
			}
		}
		obj[list] = items
		orig, _ := json.Marshal(obj)
		obj[list] = append(items, add)
		if err := e.Central.JSON("PUT", "/api/"+kind+"/"+id, obj, nil); err != nil {
			t.Fatalf("add file fields to %s %s: %v", kind, id, err)
		}
		if os.Getenv("HUB_FILES_KEEP") != "" {
			return // left for a manual run on a tablet (ADVERSARIAL-TESTING.md, I11c)
		}
		t.Cleanup(func() {
			if st, b, err := e.Central.Do("PUT", "/api/"+kind+"/"+id, json.RawMessage(orig)); err != nil || st/100 != 2 {
				t.Errorf("put %s %s back: %d %v %.300s", kind, id, st, err, b)
			}
		})
	}
	for _, d := range []string{fileDeImage, fileDeFile} {
		edit("programStages", stage, "programStageDataElements", map[string]any{"dataElement": map[string]any{"id": d}, "compulsory": false}, "dataElement")
	}
	// A stage with sections shows only what its sections list (Capture web).
	var st struct {
		Sections []struct{ ID string } `json:"programStageSections"`
	}
	if err := e.Central.JSON("GET", "/api/programStages/"+stage+"?fields=programStageSections%5Bid%5D", nil, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Sections) > 0 {
		for _, d := range []string{fileDeImage, fileDeFile} {
			edit("programStageSections", st.Sections[0].ID, "dataElements", map[string]any{"id": d}, "")
		}
	}
	edit("programs", program, "programTrackedEntityAttributes", map[string]any{"trackedEntityAttribute": map[string]any{"id": fileAtImage},
		"mandatory": false, "displayInList": false, "searchable": false}, "trackedEntityAttribute")
}

// refID is the UID an item of a metadata list refers to: item[key].id, or item.id
// when key is empty.
func refID(item any, key string) any {
	m, _ := item.(map[string]any)
	if key == "" {
		return m["id"]
	}
	ref, _ := m[key].(map[string]any)
	return ref["id"]
}

// fillPerson gives a person the program's mandatory attributes when the manifest
// names none (the eRegisters profile).
func fillPerson(t *testing.T, e *tk.Env, p tk.Program, pr *tk.Person) *tk.Person {
	if len(p.RequiredAttrs) > 0 {
		return pr
	}
	var prog struct {
		Attrs []struct {
			Mandatory bool `json:"mandatory"`
			Attr      struct {
				ID        string `json:"id"`
				ValueType string `json:"valueType"`
				OptionSet struct {
					Options []struct {
						Code string `json:"code"`
					} `json:"options"`
				} `json:"optionSet"`
			} `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	path := "/api/programs/" + p.ID + "?fields=programTrackedEntityAttributes%5Bmandatory,trackedEntityAttribute%5Bid,valueType,optionSet%5Boptions%5Bcode%5D%5D%5D%5D"
	if err := e.Central.JSON("GET", path, nil, &prog); err != nil {
		t.Fatal(err)
	}
	for _, a := range prog.Attrs {
		if !a.Mandatory || a.Attr.ID == p.SearchAttr {
			continue
		}
		v := "Test"
		switch {
		case len(a.Attr.OptionSet.Options) > 0:
			v = a.Attr.OptionSet.Options[0].Code
		case a.Attr.ValueType == "AGE" || a.Attr.ValueType == "DATE":
			v = "1990-01-01"
		case strings.HasPrefix(a.Attr.ValueType, "INTEGER") || a.Attr.ValueType == "NUMBER":
			v = "1"
		}
		pr.SetAttr(a.Attr.ID, v)
	}
	return pr
}

func setValues(ev map[string]any, values map[string]string) {
	dvs, _ := ev["dataValues"].([]any)
	for de, v := range values {
		dvs = append(dvs, map[string]any{"dataElement": de, "value": v})
	}
	ev["dataValues"] = dvs
}

func hasValue(ev map[string]any, de, v string) bool {
	dvs, _ := ev["dataValues"].([]any)
	return slices.ContainsFunc(dvs, func(x any) bool {
		m, _ := x.(map[string]any)
		return m["dataElement"] == de && m["value"] == v
	})
}

// upload posts a file as Android Capture and Capture web do (multipart, field "file";
// central's default domain is DATA_VALUE), with the nurse's Basic credentials.
func upload(t *testing.T, e *tk.Env, c *tk.Client, name, ctype string, data []byte, query string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	h.Set("Content-Type", ctype)
	pw, _ := mw.CreatePart(h)
	pw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", e.Srv.URL+"/api/fileResources"+query, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetBasicAuth(c.User, e.M.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func uploadOK(t *testing.T, e *tk.Env, c *tk.Client, name, ctype string, data []byte) string {
	t.Helper()
	st, b := upload(t, e, c, name, ctype, data, "")
	var r struct {
		Response struct {
			FileResource struct {
				ID string `json:"id"`
			} `json:"fileResource"`
		} `json:"response"`
	}
	json.Unmarshal(b, &r)
	if st != 202 || r.Response.FileResource.ID == "" {
		t.Fatalf("upload %s through the hub: %d %.500s", name, st, b)
	}
	return r.Response.FileResource.ID
}

// centralEvent reads an event on central as the manifest's admin user: the instance's
// superuser may have no search scope over the facility (the eRegisters instance).
func centralEvent(t *testing.T, e *tk.Env, uid string) map[string]any {
	t.Helper()
	u, _ := e.M.User(tk.RoleAdmin)
	st, b, err := e.Central.As(u.Username, e.M.Password).Do("GET", "/api/tracker/events/"+uid+"?fields=*", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st != 200 {
		return nil
	}
	var o map[string]any
	json.Unmarshal(b, &o)
	return o
}

func centralFile(t *testing.T, e *tk.Env, id string) map[string]any {
	t.Helper()
	var fr map[string]any
	if err := e.Central.JSON("GET", "/api/fileResources/"+id, nil, &fr); err != nil {
		t.Fatal(err)
	}
	return fr
}

// waitStored waits for central to move the file to its store: until then it answers
// reads of the value with 409 ("Content is being processed").
func waitStored(t *testing.T, e *tk.Env, id string) {
	t.Helper()
	for start := time.Now(); time.Since(start) < time.Minute; time.Sleep(500 * time.Millisecond) {
		if centralFile(t, e, id)["storageStatus"] == "STORED" {
			return
		}
	}
	t.Fatalf("file %s not stored on central after a minute", id)
}

// testPNG is a photo-sized PNG that does not compress (noise): about 4 bytes a pixel.
func testPNG(t *testing.T, w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(uint64(w), uint64(h)))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.UintN(256))
	}
	img.Set(0, 0, color.NRGBA{A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(uint64(n), 1))
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	// A PDF header, so a file type sniffer agrees with the name.
	copy(b, "%PDF-1.4\n")
	return b
}

// TestCaptureWebFiles drives Capture web (clients/capture_web_scenarios.py, action
// files) with an image field: an event's image online and offline, and an image
// picked for a new event online and offline. HUB_CLIENTS=1 and HUB_FILES=1.
func TestCaptureWebFiles(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real Capture web app")
	}
	e, c, p, stage := fileEnv(t)
	m := e.M
	img := testPNG(t, 1100, 800)
	imgID := uploadOK(t, e, c, "photo.png", "image/png", img)
	pr := fillPerson(t, e, p, m.NewPerson(p, "Fw"+tk.UID()[:6], stage))
	setValues(pr.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any)[0].(map[string]any)["events"].([]any)[0].(map[string]any),
		map[string]string{fileDeImage: imgID})
	if st, b := c.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
		t.Fatalf("save online: %d %.800s", st, b)
	}
	waitStored(t, e, imgID)
	e.Sync()
	allowCapture(t, e, m.Users[tk.RoleNurse].Username)
	e.Web(tk.RoleNurse).Login()
	var st struct{ Name string }
	if err := e.Central.JSON("GET", "/api/programStages/"+stage+"?fields=name", nil, &st); err != nil {
		t.Fatal(err)
	}
	out := reportDir(t, "capture-files")
	path := out + "/photo.png"
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	res := runScenario(t, e, "files", out, nil, "USER="+m.Users[tk.RoleNurse].Username, "ENROLLMENT="+pr.Enrollment,
		"EVENT="+pr.Events[0], "STAGE_NAME="+st.Name, "IMAGE_LABEL=Hub test image", "IMAGE_PATH="+path)
	for _, step := range []string{"online-view", "offline-view", "online-pick", "offline-pick"} {
		s := res.Steps[step]
		b, _ := json.Marshal(map[string]any{"images": s["images"], "requests": s["requests"], "upload": s["upload"],
			"uploadAnswer": s["uploadAnswer"], "alertsAfterPick": s["alertsAfterPick"], "save": s["save"], "alerts": s["alerts"]})
		t.Logf("%s: %s", step, b)
	}
	if up := res.Steps["online-pick"]["upload"]; up != 202.0 {
		t.Errorf("online: Capture's upload through the hub got %v, want 202", up)
	}
	if up := res.Steps["offline-pick"]["upload"]; up != 503.0 {
		t.Errorf("offline: Capture's upload got %v, want the hub's 503", up)
	}
	t.Logf("screenshots in %s", out)
}
