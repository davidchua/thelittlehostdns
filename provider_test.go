package thelittlehost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// newTestServer creates a mock API server with the given handler and returns
// a Provider configured to use it.
func newTestServer(t *testing.T, handler http.Handler) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Provider{
		APIToken:  "tlh_test_token",
		ServerURL: srv.URL,
	}
}

// apiHandler builds an http.ServeMux that simulates The Little Host API.
// It stores records in memory and supports list, create, update, and delete.
func apiHandler(t *testing.T) (*http.ServeMux, *[]apiRecord) {
	t.Helper()
	mux := http.NewServeMux()
	nextID := 1
	records := &[]apiRecord{}

	// GET /zones/{zone}/records
	mux.HandleFunc("GET /zones/{zone}/records", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(*records)
	})

	// POST /zones/{zone}/records
	mux.HandleFunc("POST /zones/{zone}/records", func(w http.ResponseWriter, r *http.Request) {
		var req recordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec := apiRecord{
			ID:       nextID,
			Type:     req.Record.RecordType,
			Name:     req.Record.Name,
			Value:    req.Record.Value,
			TTL:      req.Record.TTL,
			Priority: req.Record.Priority,
		}
		nextID++
		*records = append(*records, rec)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(rec)
	})

	// PATCH /zones/{zone}/records/{id}
	mux.HandleFunc("PATCH /zones/{zone}/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		var req recordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for i, rec := range *records {
			if idStr == fmt.Sprintf("%d", rec.ID) {
				if req.Record.RecordType != "" {
					(*records)[i].Type = req.Record.RecordType
				}
				if req.Record.Name != "" {
					(*records)[i].Name = req.Record.Name
				}
				if req.Record.Value != "" {
					(*records)[i].Value = req.Record.Value
				}
				if req.Record.TTL != 0 {
					(*records)[i].TTL = req.Record.TTL
				}
				(*records)[i].Priority = req.Record.Priority
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode((*records)[i])
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	// DELETE /zones/{zone}/records/{id}
	mux.HandleFunc("DELETE /zones/{zone}/records/{id}", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")
		for i, rec := range *records {
			if idStr == fmt.Sprintf("%d", rec.ID) {
				*records = append((*records)[:i], (*records)[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	})

	return mux, records
}

func TestGetRecords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		seed     []apiRecord
		wantLen  int
		wantName string
		wantType string
	}{
		{
			name:    "empty zone",
			seed:    nil,
			wantLen: 0,
		},
		{
			name: "single A record",
			seed: []apiRecord{
				{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
			},
			wantLen:  1,
			wantName: "www",
			wantType: "A",
		},
		{
			name: "multiple records",
			seed: []apiRecord{
				{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
				{ID: 2, Type: "CNAME", Name: "blog", Value: "example.netlify.app", TTL: 3600},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mux, records := apiHandler(t)
			if tt.seed != nil {
				*records = tt.seed
			}
			p := newTestServer(t, mux)

			got, err := p.GetRecords(context.Background(), "example.com.")
			if err != nil {
				t.Fatalf("GetRecords: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Fatalf("got %d records, want %d", len(got), tt.wantLen)
			}
			if tt.wantName != "" {
				rr := got[0].RR()
				if rr.Name != tt.wantName {
					t.Errorf("name = %q, want %q", rr.Name, tt.wantName)
				}
			}
			if tt.wantType != "" {
				rr := got[0].RR()
				if rr.Type != tt.wantType {
					t.Errorf("type = %q, want %q", rr.Type, tt.wantType)
				}
			}
		})
	}
}

func TestAppendRecords(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	p := newTestServer(t, mux)

	input := []libdns.Record{
		libdns.Address{
			Name: "www",
			TTL:  3600 * time.Second,
			IP:   netip.MustParseAddr("1.2.3.4"),
		},
	}

	got, err := p.AppendRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("AppendRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if len(*records) != 1 {
		t.Fatalf("server has %d records, want 1", len(*records))
	}

	rr := got[0].RR()
	if rr.Name != "www" {
		t.Errorf("name = %q, want %q", rr.Name, "www")
	}
	if rr.Type != "A" {
		t.Errorf("type = %q, want %q", rr.Type, "A")
	}
}

func TestSetRecords_CreateNew(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	p := newTestServer(t, mux)

	input := []libdns.Record{
		libdns.Address{
			Name: "www",
			TTL:  3600 * time.Second,
			IP:   netip.MustParseAddr("1.2.3.4"),
		},
	}

	got, err := p.SetRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("SetRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if len(*records) != 1 {
		t.Fatalf("server has %d records, want 1", len(*records))
	}
}

func TestSetRecords_UpdateExisting(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	*records = []apiRecord{
		{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
	}
	p := newTestServer(t, mux)

	input := []libdns.Record{
		libdns.Address{
			Name: "www",
			TTL:  3600 * time.Second,
			IP:   netip.MustParseAddr("5.6.7.8"),
		},
	}

	got, err := p.SetRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("SetRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if len(*records) != 1 {
		t.Fatalf("server should still have 1 record, has %d", len(*records))
	}
	if (*records)[0].Value != "5.6.7.8" {
		t.Errorf("value = %q, want %q", (*records)[0].Value, "5.6.7.8")
	}
}

func TestSetRecords_DeleteSurplus(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	*records = []apiRecord{
		{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
		{ID: 2, Type: "A", Name: "www", Value: "5.6.7.8", TTL: 3600},
	}
	p := newTestServer(t, mux)

	// Set only one A record for "www" — the second should be deleted.
	input := []libdns.Record{
		libdns.Address{
			Name: "www",
			TTL:  3600 * time.Second,
			IP:   netip.MustParseAddr("9.9.9.9"),
		},
	}

	got, err := p.SetRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("SetRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if len(*records) != 1 {
		t.Fatalf("server should have 1 record, has %d", len(*records))
	}
}

func TestDeleteRecords(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	*records = []apiRecord{
		{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
		{ID: 2, Type: "CNAME", Name: "blog", Value: "example.netlify.app", TTL: 3600},
	}
	p := newTestServer(t, mux)

	input := []libdns.Record{
		libdns.Address{
			Name: "www",
			TTL:  3600 * time.Second,
			IP:   netip.MustParseAddr("1.2.3.4"),
		},
	}

	got, err := p.DeleteRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("DeleteRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deleted records, want 1", len(got))
	}
	if len(*records) != 1 {
		t.Fatalf("server should have 1 record, has %d", len(*records))
	}
	if (*records)[0].Name != "blog" {
		t.Errorf("remaining record name = %q, want %q", (*records)[0].Name, "blog")
	}
}

func TestDeleteRecords_Nonexistent(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	*records = []apiRecord{
		{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
	}
	p := newTestServer(t, mux)

	// Try to delete a record that doesn't exist — should be silently ignored.
	input := []libdns.Record{
		libdns.Address{
			Name: "missing",
			TTL:  3600 * time.Second,
			IP:   netip.MustParseAddr("9.9.9.9"),
		},
	}

	got, err := p.DeleteRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("DeleteRecords: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d deleted records, want 0", len(got))
	}
	if len(*records) != 1 {
		t.Fatalf("server should still have 1 record, has %d", len(*records))
	}
}

func TestDeleteRecords_WildcardType(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	*records = []apiRecord{
		{ID: 1, Type: "A", Name: "www", Value: "1.2.3.4", TTL: 3600},
		{ID: 2, Type: "AAAA", Name: "www", Value: "2001:db8::1", TTL: 3600},
	}
	p := newTestServer(t, mux)

	// Delete all records named "www" regardless of type (empty Type acts as wildcard).
	input := []libdns.Record{
		libdns.RR{Name: "www"},
	}

	got, err := p.DeleteRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("DeleteRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d deleted records, want 2", len(got))
	}
	if len(*records) != 0 {
		t.Fatalf("server should have 0 records, has %d", len(*records))
	}
}

func TestMXRecordConversion(t *testing.T) {
	t.Parallel()
	mux, _ := apiHandler(t)
	p := newTestServer(t, mux)

	input := []libdns.Record{
		libdns.MX{
			Name:       "@",
			TTL:        3600 * time.Second,
			Preference: 10,
			Target:     "mail.example.com",
		},
	}

	got, err := p.AppendRecords(context.Background(), "example.com.", input)
	if err != nil {
		t.Fatalf("AppendRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}

	rr := got[0].RR()
	if rr.Type != "MX" {
		t.Errorf("type = %q, want %q", rr.Type, "MX")
	}
	if rr.Name != "@" {
		t.Errorf("name = %q, want %q", rr.Name, "@")
	}
}

// TestTXTRecordRoundTrip mimics the CertMagic ACME DNS-01 flow:
// create a TXT record, then delete it.
func TestTXTRecordRoundTrip(t *testing.T) {
	t.Parallel()
	mux, records := apiHandler(t)
	p := newTestServer(t, mux)

	// CertMagic creates a TXT record for the challenge.
	input := []libdns.Record{
		libdns.TXT{
			Name: "_acme-challenge.testme",
			TTL:  120 * time.Second,
			Text: "3X4yy4dn5TSTWXYcIanf09hbty9755qW9FLyR0175Ws",
		},
	}

	// Present step: append the TXT record.
	created, err := p.AppendRecords(context.Background(), "kaopeh.com.", input)
	if err != nil {
		t.Fatalf("AppendRecords (present): %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("got %d records, want 1", len(created))
	}

	rr := created[0].RR()
	if rr.Type != "TXT" {
		t.Errorf("type = %q, want TXT", rr.Type)
	}
	if rr.Name != "_acme-challenge.testme" {
		t.Errorf("name = %q, want _acme-challenge.testme", rr.Name)
	}

	// CleanUp step: delete the TXT record.
	deleted, err := p.DeleteRecords(context.Background(), "kaopeh.com.", created)
	if err != nil {
		t.Fatalf("DeleteRecords (cleanup): %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("got %d deleted, want 1", len(deleted))
	}
	if len(*records) != 0 {
		t.Fatalf("server should have 0 records, has %d", len(*records))
	}
}

func TestUnFQDN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"example.com.", "example.com"},
		{"example.com", "example.com"},
		{".", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := unFQDN(tt.input); got != tt.want {
				t.Errorf("unFQDN(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAuthorizationHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]apiRecord{})
	}))
	t.Cleanup(srv.Close)

	p := &Provider{
		APIToken:  "tlh_my_secret_token",
		ServerURL: srv.URL,
	}

	_, err := p.GetRecords(context.Background(), "example.com.")
	if err != nil {
		t.Fatalf("GetRecords: %v", err)
	}
	if gotAuth != "Bearer tlh_my_secret_token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tlh_my_secret_token")
	}
}
