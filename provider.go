// Package thelittlehost implements a DNS record management client compatible
// with the libdns interfaces for The Little Host DNS API.
package thelittlehost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libdns/libdns"
)

// apiRecord represents a DNS record as returned by The Little Host API.
// Note: the API accepts "record_type" in requests but returns "type" in responses.
type apiRecord struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
}

// recordInput represents the fields for creating or updating a record.
// Note: removed omitempty from the TTL json field as it returns a nil when
// TTL=0
type recordInput struct {
	RecordType string `json:"record_type,omitempty"`
	Name       string `json:"name,omitempty"`
	Value      string `json:"value,omitempty"`
	TTL        int    `json:"ttl"`
	Priority   *int   `json:"priority,omitempty"`
}

// recordRequest wraps a recordInput for the API request body.
type recordRequest struct {
	Record recordInput `json:"record"`
}

// rrsetKey uniquely identifies an RRset by name and type.
type rrsetKey struct {
	Name string
	Type string
}

// Provider facilitates DNS record manipulation with The Little Host.
type Provider struct {
	// APIToken is the bearer token for authentication with The Little Host API.
	// Tokens are prefixed with "tlh_" and can be generated in the control panel.
	APIToken string `json:"api_token,omitempty"`

	// ServerURL overrides the default API base URL. Leave empty to use the
	// production endpoint (https://control.thelittlehost.com/api/v1).
	ServerURL string `json:"server_url,omitempty"`

	mu     sync.Mutex
	client *http.Client
}

func (p *Provider) initClient() {
	if p.client == nil {
		p.client = &http.Client{Timeout: 30 * time.Second}
	}
	if p.ServerURL == "" {
		p.ServerURL = "https://control.thelittlehost.com/api/v1"
	}
}

// doRequest executes an authenticated HTTP request against The Little Host API.
func (p *Provider) doRequest(ctx context.Context, method, path string, body any) (*http.Response, error) {
	p.initClient()

	url := p.ServerURL + "/" + path

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.APIToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return p.client.Do(req)
}

// readResponse reads the response body and checks the status code.
func readResponse(resp *http.Response, wantStatuses ...int) ([]byte, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	for _, s := range wantStatuses {
		if resp.StatusCode == s {
			return body, nil
		}
	}

	return body, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
}

// unmarshalRecord decodes a single apiRecord from JSON, trying both
// unwrapped ({"id":...}) and wrapped ({"record":{"id":...}}) formats.
func unmarshalRecord(body []byte) (apiRecord, error) {
	// Try unwrapped (flat object).
	var flat apiRecord
	if err := json.Unmarshal(body, &flat); err == nil && flat.ID != 0 {
		return flat, nil
	}

	// Try wrapped ({"record": {...}}).
	var wrapped struct {
		Record apiRecord `json:"record"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Record.ID != 0 {
		return wrapped.Record, nil
	}

	return apiRecord{}, fmt.Errorf("decode record: unrecognized response format: %.256s", body)
}

// unFQDN strips the trailing dot from a fully qualified domain name.
func unFQDN(zone string) string {
	return strings.TrimSuffix(zone, ".")
}

// toLibdnsRecord converts an API record to a libdns.Record (type-specific struct).
func toLibdnsRecord(r apiRecord) (libdns.Record, error) {
	data := r.Value
	if r.Type == "MX" && r.Priority != nil {
		data = strconv.Itoa(*r.Priority) + " " + r.Value
	}

	rr := libdns.RR{
		Name: r.Name,
		TTL:  time.Duration(r.TTL) * time.Second,
		Type: r.Type,
		Data: data,
	}

	rec, err := rr.Parse()
	if err != nil {
		return nil, fmt.Errorf("parse record %s %q: %w", r.Type, r.Name, err)
	}
	return rec, nil
}

// toRecordInput converts a libdns.Record to the API input format.
func toRecordInput(rec libdns.Record) recordInput {
	rr := rec.RR()

	input := recordInput{
		RecordType: rr.Type,
		Name:       rr.Name,
		Value:      rr.Data,
		TTL:        int(rr.TTL.Seconds()),
	}

	// MX records encode priority in Data as "preference target".
	if rr.Type == "MX" {
		parts := strings.SplitN(rr.Data, " ", 2)
		if len(parts) == 2 {
			if prio, err := strconv.Atoi(parts[0]); err == nil {
				input.Priority = &prio
				input.Value = parts[1]
			}
		}
	}

	return input
}

// apiRecordMatchesRR reports whether an API record matches a libdns RR for deletion.
// Empty fields in rr act as wildcards per the libdns.RecordDeleter contract.
func apiRecordMatchesRR(ar apiRecord, rr libdns.RR) bool {
	if ar.Name != rr.Name {
		return false
	}
	if rr.Type != "" && ar.Type != rr.Type {
		return false
	}
	if rr.TTL != 0 && time.Duration(ar.TTL)*time.Second != rr.TTL {
		return false
	}
	if rr.Data != "" {
		apiData := ar.Value
		if ar.Type == "MX" && ar.Priority != nil {
			apiData = strconv.Itoa(*ar.Priority) + " " + ar.Value
		}
		if apiData != rr.Data {
			return false
		}
	}
	return true
}

// getAllRecords fetches all records from the zone via the API.
func (p *Provider) getAllRecords(ctx context.Context, zone string) ([]apiRecord, error) {
	resp, err := p.doRequest(ctx, http.MethodGet, fmt.Sprintf("zones/%s/records", zone), nil)
	if err != nil {
		return nil, fmt.Errorf("list records for zone %s: %w", zone, err)
	}

	body, err := readResponse(resp, http.StatusOK)
	if err != nil {
		return nil, fmt.Errorf("list records for zone %s: %w", zone, err)
	}

	// Try plain array first, then wrapped {"records": [...]}.
	var records []apiRecord
	if err := json.Unmarshal(body, &records); err != nil {
		var wrapped struct {
			Records []apiRecord `json:"records"`
		}
		if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
			return nil, fmt.Errorf("decode records for zone %s: %w", zone, err)
		}
		records = wrapped.Records
	}
	return records, nil
}

// createRecord creates a single record in the zone via the API.
func (p *Provider) createRecord(ctx context.Context, zone string, input recordInput) (apiRecord, error) {
	resp, err := p.doRequest(ctx, http.MethodPost, fmt.Sprintf("zones/%s/records", zone), recordRequest{Record: input})
	if err != nil {
		return apiRecord{}, fmt.Errorf("create record in zone %s: %w", zone, err)
	}

	body, err := readResponse(resp, http.StatusCreated, http.StatusOK)
	if err != nil {
		return apiRecord{}, fmt.Errorf("create record in zone %s: %w", zone, err)
	}

	record, err := unmarshalRecord(body)
	if err != nil {
		return apiRecord{}, fmt.Errorf("create record in zone %s: %w", zone, err)
	}
	return record, nil
}

// updateRecord updates a single record in the zone via the API.
func (p *Provider) updateRecord(ctx context.Context, zone string, id int, input recordInput) (apiRecord, error) {
	resp, err := p.doRequest(ctx, http.MethodPatch, fmt.Sprintf("zones/%s/records/%d", zone, id), recordRequest{Record: input})
	if err != nil {
		return apiRecord{}, fmt.Errorf("update record %d in zone %s: %w", id, zone, err)
	}

	body, err := readResponse(resp, http.StatusOK)
	if err != nil {
		return apiRecord{}, fmt.Errorf("update record %d in zone %s: %w", id, zone, err)
	}

	record, err := unmarshalRecord(body)
	if err != nil {
		return apiRecord{}, fmt.Errorf("update record %d in zone %s: %w", id, zone, err)
	}
	return record, nil
}

// deleteRecordByID deletes a single record by ID via the API.
func (p *Provider) deleteRecordByID(ctx context.Context, zone string, id int) error {
	resp, err := p.doRequest(ctx, http.MethodDelete, fmt.Sprintf("zones/%s/records/%d", zone, id), nil)
	if err != nil {
		return fmt.Errorf("delete record %d in zone %s: %w", id, zone, err)
	}

	if _, err := readResponse(resp, http.StatusNoContent); err != nil {
		return fmt.Errorf("delete record %d in zone %s: %w", id, zone, err)
	}
	return nil
}

// GetRecords lists all the records in the zone. zone must be a fully-qualified
// domain name with a trailing dot (e.g. "example.com."). Record names in the
// returned slice are relative to the zone.
func (p *Provider) GetRecords(ctx context.Context, zone string) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	zone = unFQDN(zone)

	apiRecs, err := p.getAllRecords(ctx, zone)
	if err != nil {
		return nil, err
	}

	records := make([]libdns.Record, 0, len(apiRecs))
	for _, ar := range apiRecs {
		rec, err := toLibdnsRecord(ar)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// AppendRecords adds records to the zone without replacing any existing records.
// It returns the records that were added. Record names must be relative to the zone.
func (p *Provider) AppendRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	zone = unFQDN(zone)

	created := make([]libdns.Record, 0, len(recs))
	for _, rec := range recs {
		input := toRecordInput(rec)
		ar, err := p.createRecord(ctx, zone, input)
		if err != nil {
			return created, err
		}
		libRec, err := toLibdnsRecord(ar)
		if err != nil {
			return created, err
		}
		created = append(created, libRec)
	}
	return created, nil
}

// SetRecords sets the records in the zone, either by updating existing records
// or creating new ones. For each (name, type) pair present in recs, it ensures
// the zone contains exactly those records — surplus existing records for that
// pair are deleted. Records for (name, type) pairs not mentioned in recs are
// left untouched. It returns the records that were set.
func (p *Provider) SetRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	zone = unFQDN(zone)

	existing, err := p.getAllRecords(ctx, zone)
	if err != nil {
		return nil, err
	}

	// Group input records by (name, type).
	inputByKey := make(map[rrsetKey][]libdns.Record)
	for _, rec := range recs {
		rr := rec.RR()
		key := rrsetKey{Name: rr.Name, Type: rr.Type}
		inputByKey[key] = append(inputByKey[key], rec)
	}

	// Group existing API records by (name, type).
	existingByKey := make(map[rrsetKey][]apiRecord)
	for _, ar := range existing {
		key := rrsetKey{Name: ar.Name, Type: ar.Type}
		existingByKey[key] = append(existingByKey[key], ar)
	}

	var result []libdns.Record

	for key, inputRecs := range inputByKey {
		existingRecs := existingByKey[key]

		// Update existing records where possible, create the rest.
		for i, rec := range inputRecs {
			input := toRecordInput(rec)
			var ar apiRecord
			if i < len(existingRecs) {
				ar, err = p.updateRecord(ctx, zone, existingRecs[i].ID, input)
			} else {
				ar, err = p.createRecord(ctx, zone, input)
			}
			if err != nil {
				return result, err
			}
			libRec, err := toLibdnsRecord(ar)
			if err != nil {
				return result, err
			}
			result = append(result, libRec)
		}

		// Delete surplus existing records not covered by input.
		for i := len(inputRecs); i < len(existingRecs); i++ {
			if err := p.deleteRecordByID(ctx, zone, existingRecs[i].ID); err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

// DeleteRecords deletes the specified records from the zone.
// Records that don't exist in the zone are silently ignored.
// Empty Type, TTL, or Data fields in the input act as wildcards.
func (p *Provider) DeleteRecords(ctx context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	zone = unFQDN(zone)

	existing, err := p.getAllRecords(ctx, zone)
	if err != nil {
		return nil, err
	}

	// Track which API record IDs have been deleted to avoid double-deletion.
	deletedIDs := make(map[int]bool)

	var deleted []libdns.Record
	for _, rec := range recs {
		rr := rec.RR()
		for _, ar := range existing {
			if deletedIDs[ar.ID] {
				continue
			}
			if !apiRecordMatchesRR(ar, rr) {
				continue
			}
			if err := p.deleteRecordByID(ctx, zone, ar.ID); err != nil {
				return deleted, err
			}
			deletedIDs[ar.ID] = true
			libRec, err := toLibdnsRecord(ar)
			if err != nil {
				return deleted, err
			}
			deleted = append(deleted, libRec)
		}
	}

	return deleted, nil
}

// Interface guards
var (
	_ libdns.RecordGetter   = (*Provider)(nil)
	_ libdns.RecordAppender = (*Provider)(nil)
	_ libdns.RecordSetter   = (*Provider)(nil)
	_ libdns.RecordDeleter  = (*Provider)(nil)
)
