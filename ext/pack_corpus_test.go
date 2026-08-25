package ext_test

import (
	"testing"

	"github.com/mind-vm/agentloop/ext"
	"github.com/mind-vm/agentloop/sandbox"
)

// --- documentSearch ---

func TestSearchPack_ReturnsHits(t *testing.T) {
	var gotQuery string
	var gotTopK int
	search := func(query string, topK int) ([]ext.SearchHit, error) {
		gotQuery, gotTopK = query, topK
		return []ext.SearchHit{
			{DocumentID: "d1", DocumentTitle: "Policy", ChunkIndex: 2, Content: "refunds within 30 days", Score: 0.91},
		}, nil
	}
	s := sandbox.New(ext.SearchPack(search))

	out, err := s.Execute("var h = documentSearch(\"refund\", 3);\nh[0].documentTitle + \":\" + h[0].chunkIndex + \":\" + h.length")
	if err != nil {
		t.Fatalf("documentSearch: %v", err)
	}
	if out != "Policy:2:1" {
		t.Fatalf("got %q, want %q", out, "Policy:2:1")
	}
	if gotQuery != "refund" || gotTopK != 3 {
		t.Errorf("backend args: query=%q topK=%d", gotQuery, gotTopK)
	}
}

func TestSearchPack_DefaultTopK(t *testing.T) {
	var gotTopK int
	search := func(_ string, topK int) ([]ext.SearchHit, error) {
		gotTopK = topK
		return nil, nil
	}
	s := sandbox.New(ext.SearchPack(search))
	if _, err := s.Execute(`documentSearch("q")`); err != nil {
		t.Fatalf("documentSearch: %v", err)
	}
	if gotTopK != 5 {
		t.Errorf("default topK: got %d, want 5", gotTopK)
	}
}

func TestSearchPack_QueryRequired(t *testing.T) {
	s := sandbox.New(ext.SearchPack(func(string, int) ([]ext.SearchHit, error) { return nil, nil }))
	if _, err := s.Execute(`documentSearch("")`); err == nil {
		t.Fatalf("expected error for empty query")
	}
}

// --- stores ---

type fakeStores struct {
	docs []ext.StoreDoc
	text map[string]string
}

func (f fakeStores) List() ([]ext.StoreDoc, error)  { return f.docs, nil }
func (f fakeStores) Read(id string) (string, error) { return f.text[id], nil }

func TestStoresPack_ListAndRead(t *testing.T) {
	backend := fakeStores{
		docs: []ext.StoreDoc{{ID: "d1", Title: "Handbook", ChunkCount: 4}},
		text: map[string]string{"d1": "full document text"},
	}
	s := sandbox.New(ext.StoresPack(backend))

	out, err := s.Execute("var d = stores.list();\nd[0].title + \":\" + d[0].chunkCount")
	if err != nil {
		t.Fatalf("stores.list: %v", err)
	}
	if out != "Handbook:4" {
		t.Fatalf("list got %q, want Handbook:4", out)
	}

	out, err = s.Execute(`stores.read("d1")`)
	if err != nil {
		t.Fatalf("stores.read: %v", err)
	}
	if out != "full document text" {
		t.Fatalf("read got %q", out)
	}
}

func TestStoresPack_ReadRequiresID(t *testing.T) {
	s := sandbox.New(ext.StoresPack(fakeStores{}))
	if _, err := s.Execute(`stores.read("")`); err == nil {
		t.Fatalf("expected error for empty documentId")
	}
}
