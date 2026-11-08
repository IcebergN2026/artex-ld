package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// countingReader hides its concrete type so httptest.NewRequest leaves
// ContentLength = -1, exercising the chunked path, and records how many bytes
// the handler actually pulled off the wire.
type countingReader struct {
	data []byte
	read int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	r.read += int64(n)
	return n, nil
}

// assertSingleErrorResponse verifies the handler wrote exactly one JSON error
// object (no double write after the decode helper already answered).
func assertSingleErrorResponse(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(rec.Body.String()))
	var payload map[string]string
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("error response is not a JSON object: %v; body=%q", err, rec.Body.String())
	}
	if _, ok := payload["error"]; !ok {
		t.Fatalf("error response missing error field: %q", rec.Body.String())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("response was written more than once (extra JSON %v): %q", extra, rec.Body.String())
	}
	return payload["error"]
}

func TestDecodeJSONRequiredBodySemantics(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantOK   bool
		wantCode int
		wantErr  string
	}{
		{name: "empty body is required", body: "", wantCode: http.StatusBadRequest, wantErr: "请求正文为空"},
		{name: "whitespace only is empty", body: " \n\t\r ", wantCode: http.StatusBadRequest, wantErr: "请求正文为空"},
		{name: "malformed JSON", body: `{"n":`, wantCode: http.StatusBadRequest, wantErr: "invalid JSON"},
		{name: "null leaves zero value", body: "null", wantOK: true},
		{name: "trailing value tolerated", body: `{"n":1}{"n":2}`, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v struct {
				N int `json:"n"`
			}
			rec := httptest.NewRecorder()
			ok := decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(tc.body)), &v, maxSmallJSONBodyBytes)
			if ok != tc.wantOK {
				t.Fatalf("decodeJSON ok=%v want %v; status=%d body=%q", ok, tc.wantOK, rec.Code, rec.Body.String())
			}
			if tc.wantOK {
				if rec.Body.Len() != 0 {
					t.Fatalf("successful decode wrote a response: %q", rec.Body.String())
				}
				if tc.body == "null" && v.N != 0 {
					t.Fatalf("null body changed the value: %+v", v)
				}
				if strings.HasPrefix(tc.body, `{"n":1}`) && v.N != 1 {
					t.Fatalf("first JSON value not decoded: %+v", v)
				}
				return
			}
			if rec.Code != tc.wantCode {
				t.Fatalf("status=%d want %d; body=%q", rec.Code, tc.wantCode, rec.Body.String())
			}
			if msg := assertSingleErrorResponse(t, rec); !strings.Contains(msg, tc.wantErr) {
				t.Fatalf("error=%q want substring %q", msg, tc.wantErr)
			}
		})
	}
}

func TestDecodeJSONOptionalBodySemantics(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantOK   bool
		wantCode int
	}{
		{name: "empty body keeps defaults", body: "", wantOK: true},
		{name: "whitespace keeps defaults", body: " \n\t ", wantOK: true},
		{name: "null keeps defaults", body: "null", wantOK: true},
		{name: "malformed JSON rejected", body: `{"n":`, wantCode: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := struct {
				N int `json:"n"`
			}{N: 7}
			rec := httptest.NewRecorder()
			ok := decodeJSONOptional(rec, httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(tc.body)), &v, maxSmallJSONBodyBytes)
			if ok != tc.wantOK {
				t.Fatalf("decodeJSONOptional ok=%v want %v; status=%d body=%q", ok, tc.wantOK, rec.Code, rec.Body.String())
			}
			if tc.wantOK {
				if v.N != 7 {
					t.Fatalf("optional empty body changed defaults: %+v", v)
				}
				return
			}
			if rec.Code != tc.wantCode {
				t.Fatalf("status=%d want %d; body=%q", rec.Code, tc.wantCode, rec.Body.String())
			}
			assertSingleErrorResponse(t, rec)
		})
	}
}

func TestDecodeJSONSizeBoundary(t *testing.T) {
	body := `{"v":"` + strings.Repeat("x", 24) + `"}`
	var v struct {
		V string `json:"v"`
	}

	rec := httptest.NewRecorder()
	if !decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(body)), &v, int64(len(body))) {
		t.Fatalf("body exactly at the limit must decode; status=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ok := decodeJSON(rec, httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(body)), &v, int64(len(body)-1))
	if ok {
		t.Fatal("body one byte over the limit must be rejected")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413; body=%q", rec.Code, rec.Body.String())
	}
	if msg := assertSingleErrorResponse(t, rec); !strings.Contains(msg, "请求正文过大") {
		t.Fatalf("error=%q", msg)
	}
}

func TestDecodeJSONChunkedOversizeIs413(t *testing.T) {
	body := `{"password":"` + strings.Repeat("a", maxAuthJSONBodyBytes) + `"}`
	reader := &countingReader{data: []byte(body)}
	req := httptest.NewRequest(http.MethodPost, "/api/test", reader)
	if req.ContentLength != -1 {
		t.Fatalf("test request must be chunked, ContentLength=%d", req.ContentLength)
	}
	var v struct {
		Password string `json:"password"`
	}
	rec := httptest.NewRecorder()
	if decodeJSON(rec, req, &v, maxAuthJSONBodyBytes) {
		t.Fatal("oversized chunked body must be rejected")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413; body=%q", rec.Code, rec.Body.String())
	}
	assertSingleErrorResponse(t, rec)
	if reader.read > maxAuthJSONBodyBytes+1 {
		t.Fatalf("handler read %d bytes for a %d byte cap (body must stay bounded)", reader.read, maxAuthJSONBodyBytes)
	}
}

func TestCreateTaskRejectsEmptyAndOversizedBody(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.createTask(rec, httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader("")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body status=%d want 400; body=%q", rec.Code, rec.Body.String())
	}

	body := `{"description":"` + strings.Repeat("x", maxCreateTaskBodyBytes) + `"}`
	reader := &countingReader{data: []byte(body)}
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", reader)
	rec = httptest.NewRecorder()
	s.createTask(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status=%d want 413; body=%q", rec.Code, rec.Body.String())
	}
	assertSingleErrorResponse(t, rec)
	if reader.read > maxCreateTaskBodyBytes+1 {
		t.Fatalf("createTask read %d bytes for a %d byte cap", reader.read, maxCreateTaskBodyBytes)
	}
}

// deleteCompany is the one endpoint that deliberately rejects trailing JSON. Its
// body is optional, so empty must still delete; oversized bodies are 413 and must
// not delete the company.
func TestDeleteCompanyBodySemantics(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	create := func(t *testing.T) int64 {
		t.Helper()
		name := "json-body-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		id, err := m.pg.Companies().UpsertByName(name)
		if err != nil {
			t.Fatalf("create company: %v", err)
		}
		return id
	}
	delete := func(t *testing.T, id int64, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/api/companies/"+strconv.FormatInt(id, 10), strings.NewReader(body))
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		rec := httptest.NewRecorder()
		s.deleteCompany(rec, req)
		return rec
	}

	t.Run("empty body is optional", func(t *testing.T) {
		id := create(t)
		rec := delete(t, id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want 200; body=%q", rec.Code, rec.Body.String())
		}
	})

	t.Run("trailing JSON is rejected", func(t *testing.T) {
		id := create(t)
		defer func() { _ = m.pg.Companies().DeleteCompany(id) }()
		rec := delete(t, id, `{"delete_assets":false}{"delete_assets":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400; body=%q", rec.Code, rec.Body.String())
		}
		if msg := assertSingleErrorResponse(t, rec); !strings.Contains(msg, "multiple values") {
			t.Fatalf("error=%q", msg)
		}
		if _, err := m.pg.Companies().GetCompany(id); err != nil {
			t.Fatalf("company was deleted despite trailing JSON: %v", err)
		}
	})

	t.Run("oversized chunked body is 413", func(t *testing.T) {
		id := create(t)
		defer func() { _ = m.pg.Companies().DeleteCompany(id) }()
		body := `{"delete_assets":false,"pad":"` + strings.Repeat("x", maxSmallJSONBodyBytes) + `"}`
		reader := &countingReader{data: []byte(body)}
		req := httptest.NewRequest(http.MethodDelete, "/api/companies/"+strconv.FormatInt(id, 10), reader)
		req.SetPathValue("id", strconv.FormatInt(id, 10))
		rec := httptest.NewRecorder()
		s.deleteCompany(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413; body=%q", rec.Code, rec.Body.String())
		}
		if reader.read > maxSmallJSONBodyBytes+1 {
			t.Fatalf("handler read %d bytes for a %d byte cap", reader.read, maxSmallJSONBodyBytes)
		}
		if _, err := m.pg.Companies().GetCompany(id); err != nil {
			t.Fatalf("company was deleted despite oversized body: %v", err)
		}
	})
}
