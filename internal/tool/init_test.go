package tool

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ovid/internal/ir"
)

func TestInitCreatesCheckableProgramWithoutProjection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "app")
	var out bytes.Buffer
	if code := Init(dir, &out); code != 0 {
		t.Fatalf("init %d: %s", code, &out)
	}
	var response struct {
		OK       bool   `json:"ok"`
		Revision string `json:"revision"`
		Entry    string `json:"entry"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Entry != "app" || len(response.Revision) != 64 {
		t.Fatalf("response: %s", &out)
	}
	file, _, err := ir.ReadFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	stored, computed, err := ir.Hash(file)
	if err != nil || stored != computed || computed != response.Revision {
		t.Fatalf("revision: %q %q %v", stored, computed, err)
	}
	out.Reset()
	if code := Check(dir, &out); code != 0 {
		t.Fatalf("check %d: %s", code, &out)
	}
	if _, err := os.Stat(filepath.Join(dir, "PROJECTION")); !os.IsNotExist(err) {
		t.Fatalf("unexpected projection: %v", err)
	}
	out.Reset()
	if code := Init(dir, &out); code != 1 || !bytes.Contains(out.Bytes(), []byte(`"error":"exists"`)) {
		t.Fatalf("repeat init %d: %s", code, &out)
	}
	after, err := os.ReadFile(filepath.Join(dir, "ovid.json"))
	if err != nil || !bytes.Equal(after, file) {
		t.Fatalf("repeat init changed module: %v", err)
	}
}

func TestInitNeverOverwritesExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ovid.json")
	if err := os.Symlink("missing-target", path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := Init(dir, &out); code != 1 || !bytes.Contains(out.Bytes(), []byte(`"error":"exists"`)) {
		t.Fatalf("init %d: %s", code, &out)
	}
	if target, err := os.Readlink(path); err != nil || target != "missing-target" {
		t.Fatalf("link changed: %s %v", target, err)
	}
}

func TestConcurrentInitHasOneWinner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app")
	const workers = 12
	start := make(chan struct{})
	results := make(chan int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var out bytes.Buffer
			code := Init(dir, &out)
			if code != 0 && !bytes.Contains(out.Bytes(), []byte(`"error":"exists"`)) {
				t.Errorf("unexpected init response %d: %s", code, &out)
			}
			results <- code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for code := range results {
		if code == 0 {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winners", winners)
	}
	var out bytes.Buffer
	if code := Check(dir, &out); code != 0 {
		t.Fatalf("check %d: %s", code, &out)
	}
}

func TestQueryRefusesMismatchedRevision(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if code := Init(dir, &out); code != 0 {
		t.Fatalf("init %d: %s", code, &out)
	}
	path := filepath.Join(dir, "ovid.json")
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(file, []byte(`"value": 0`), []byte(`"value": 1`), 1)
	if bytes.Equal(changed, file) {
		t.Fatal("did not edit fixture")
	}
	if err := os.WriteFile(path, changed, 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Query(dir, "fn:app.main", "", "", "", &out); code != 1 || !bytes.Contains(out.Bytes(), []byte(`"error":"revision_mismatch"`)) {
		t.Fatalf("query %d: %s", code, &out)
	}
}
