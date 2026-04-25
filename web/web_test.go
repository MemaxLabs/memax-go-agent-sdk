package web

import (
	"context"
	"testing"
)

func TestSearcherFuncNil(t *testing.T) {
	t.Parallel()

	var fn SearcherFunc
	if _, err := fn.SearchWeb(context.Background(), SearchRequest{}); err == nil {
		t.Fatal("SearchWeb returned nil error for nil SearcherFunc")
	}
}

func TestSearcherFunc(t *testing.T) {
	t.Parallel()

	called := false
	fn := SearcherFunc(func(ctx context.Context, req SearchRequest) ([]SearchResult, error) {
		called = true
		if req.Query != "agent runtime" {
			t.Fatalf("Query = %q", req.Query)
		}
		return []SearchResult{{Title: "Memax"}}, nil
	})

	results, err := fn.SearchWeb(context.Background(), SearchRequest{Query: "agent runtime"})
	if err != nil {
		t.Fatalf("SearchWeb error = %v", err)
	}
	if !called || len(results) != 1 || results[0].Title != "Memax" {
		t.Fatalf("SearchWeb results = %#v called=%t", results, called)
	}
}

func TestFetcherFuncNil(t *testing.T) {
	t.Parallel()

	var fn FetcherFunc
	if _, err := fn.FetchURL(context.Background(), FetchRequest{}); err == nil {
		t.Fatal("FetchURL returned nil error for nil FetcherFunc")
	}
}

func TestFetcherFunc(t *testing.T) {
	t.Parallel()

	called := false
	fn := FetcherFunc(func(ctx context.Context, req FetchRequest) (FetchResult, error) {
		called = true
		if req.URL != "https://example.com" {
			t.Fatalf("URL = %q", req.URL)
		}
		return FetchResult{URL: req.URL, Content: "ok"}, nil
	})

	result, err := fn.FetchURL(context.Background(), FetchRequest{URL: "https://example.com"})
	if err != nil {
		t.Fatalf("FetchURL error = %v", err)
	}
	if !called || result.Content != "ok" {
		t.Fatalf("FetchURL result = %#v called=%t", result, called)
	}
}
