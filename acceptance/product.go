//go:build acceptance

package acceptance

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// product is the client for the binary under test. The acceptance run drives the
// server the way a user does — create a batch, poll the task list — rather than
// calling the packages in-process: the seam the assertions read is the one the
// frontend reads.
type product struct {
	base string
}

// taskView mirrors the list row the API returns. Only the columns the assertions
// speak about are decoded; a wider struct would be a second copy of the API contract
// that then has to be maintained.
type taskView struct {
	ID        int64    `json:"id"`
	Status    string   `json:"status"`
	Number    string   `json:"number"`
	BatchID   int64    `json:"batch_id"`
	LastError string   `json:"last_error"`
	Stage     string   `json:"stage"`
	Title     string   `json:"title"`
	FileCount int      `json:"file_count"`
	OutDir    string   `json:"out_dir"`
	Missing   []string `json:"missing"`
	Partial   bool     `json:"partial"`
}

type taskDetail struct {
	taskView
	TaskFiles []struct {
		Path      string `json:"path"`
		Role      string `json:"role"`
		PartIndex int    `json:"part_index"`
	} `json:"task_files"`
}

type batchResult struct {
	BatchID int64 `json:"batch_id"`
	Created int   `json:"created"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// createBatch triggers a manual batch over one corpus root.
func (p *product) createBatch(t *testing.T, pipeline string, roots []string) batchResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{"pipeline": pipeline, "roots": roots})
	if err != nil {
		t.Fatalf("acceptance: encode the batch request: %v", err)
	}
	reply := p.post(t, "/api/tasks", body)
	if reply.status != http.StatusOK {
		t.Fatalf("acceptance: create the batch over %v: HTTP %d: %s", roots, reply.status, summarize(reply.body))
	}
	var out batchResult
	if err := json.Unmarshal(reply.body, &out); err != nil {
		t.Fatalf("acceptance: decode the batch answer: %v (%s)", err, summarize(reply.body))
	}
	t.Logf("acceptance: batch %d created over %v, %d task(s), %d path(s) skipped",
		out.BatchID, roots, out.Created, len(out.Skipped))
	return out
}

// tasks lists one batch's tasks in full.
func (p *product) tasks(t *testing.T, batchID int64) []taskView {
	t.Helper()
	q := url.Values{"batch": {strconv.FormatInt(batchID, 10)}, "per_page": {"100"}}
	reply := p.get(t, "/api/tasks?"+q.Encode())
	if reply.status != http.StatusOK {
		t.Fatalf("acceptance: list the tasks of batch %d: HTTP %d: %s", batchID, reply.status, summarize(reply.body))
	}
	var page struct {
		Items []taskView `json:"items"`
	}
	if err := json.Unmarshal(reply.body, &page); err != nil {
		t.Fatalf("acceptance: decode the task list: %v (%s)", err, summarize(reply.body))
	}
	return page.Items
}

// detail reads one task's four tabs.
func (p *product) detail(t *testing.T, id int64) taskDetail {
	t.Helper()
	reply := p.get(t, "/api/tasks/"+strconv.FormatInt(id, 10))
	if reply.status != http.StatusOK {
		t.Fatalf("acceptance: read task %d: HTTP %d: %s", id, reply.status, summarize(reply.body))
	}
	var out taskDetail
	if err := json.Unmarshal(reply.body, &out); err != nil {
		t.Fatalf("acceptance: decode task %d: %v (%s)", id, err, summarize(reply.body))
	}
	return out
}

// waitForBatchSettled waits until no task in the batch is queued or running, and
// returns the tasks as they ended. A task waiting for a human (`needs_number`) counts
// as settled: it is a terminal answer to "what did the grouping make of these files",
// which is what the counterexample assertions ask.
func (e *env) waitForBatchSettled(t *testing.T, p *product, batchID int64) []taskView {
	t.Helper()
	var settled []taskView
	e.waitFor(t, 3*time.Minute, "every task of the batch to leave queued/running", func() (bool, string) {
		tasks := p.tasks(t, batchID)
		pending := 0
		statuses := ""
		for _, task := range tasks {
			statuses += " " + strconv.FormatInt(task.ID, 10) + ":" + task.Status
			if task.Status == "queued" || task.Status == "running" {
				pending++
			}
		}
		settled = tasks
		if pending > 0 {
			return false, statuses
		}
		return true, statuses
	})
	t.Logf("acceptance: batch %d settled with %d task(s)", batchID, len(settled))
	return settled
}

// ---- transport ----

func (p *product) get(t *testing.T, path string) reply {
	t.Helper()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(p.base + path)
	if err != nil {
		t.Fatalf("acceptance: GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: body}
}

func (p *product) post(t *testing.T, path string, body []byte) reply {
	t.Helper()
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Post(p.base+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("acceptance: POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: raw}
}

// nfo is what the pipeline wrote, read back as XML. The assertions compare the media
// server's view with this file rather than with a literal: a title could come from the
// directory name, a premiere date could not, so agreement proves the file was read.
type nfo struct {
	XMLName   xml.Name `xml:"movie"`
	Title     string   `xml:"title"`
	Premiered string   `xml:"premiered"`
	Plot      string   `xml:"plot"`
	Studio    string   `xml:"studio"`
	Runtime   int      `xml:"runtime"`
	Actors    []struct {
		Name string `xml:"name"`
		Role string `xml:"role"`
	} `xml:"actor"`
}

// readNFO reads one release's movie.nfo out of the output root.
func readNFO(t *testing.T, dir string) nfo {
	t.Helper()
	path := filepath.Join(dir, "movie.nfo")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("acceptance: read %s: %v", path, err)
	}
	var out nfo
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("acceptance: %s is not the XML a media server reads: %v", path, err)
	}
	return out
}
