package ingest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/snarg/tr-engine/internal/database"
	"github.com/snarg/tr-engine/internal/transcribe"
)

func TestBackfillShouldSkipForProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		audioPath string
		want      bool
	}{
		{"whisper accepts m4a", "whisper", "calls/2026/05/call.m4a", false},
		{"whisper accepts empty path", "whisper", "", false},
		{"imbe accepts dvcf", "imbe", "calls/2026/05/call.dvcf", false},
		{"imbe accepts uppercase dvcf", "imbe", "calls/2026/05/call.DVCF", false},
		{"imbe skips m4a", "imbe", "calls/2026/05/call.m4a", true},
		{"imbe skips empty path", "imbe", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backfillShouldSkipForProvider(tt.provider, tt.audioPath)
			if got != tt.want {
				t.Fatalf("backfillShouldSkipForProvider(%q, %q) = %v, want %v", tt.provider, tt.audioPath, got, tt.want)
			}
		})
	}
}

// fakeBackfillCall is one row of the fake calls table.
type fakeBackfillCall struct {
	id          int64
	start       time.Time
	audioPath   string
	transcribed bool
	loadErr     bool // GetCallForTranscription fails
}

// fakeBackfillStore mimics the untranscribed-call queries over an in-memory
// table: a call is listed until it is marked transcribed.
type fakeBackfillStore struct {
	mu        sync.Mutex
	calls     map[int64]*fakeBackfillCall
	listCalls int
	// ignoreCursor makes ListUntranscribedCalls always return the first page,
	// like the pre-#59 re-query at offset 0.
	ignoreCursor bool
}

func newFakeBackfillStore() *fakeBackfillStore {
	return &fakeBackfillStore{calls: make(map[int64]*fakeBackfillCall)}
}

func (s *fakeBackfillStore) add(c *fakeBackfillCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.audioPath == "" {
		c.audioPath = "calls/call.m4a"
	}
	s.calls[c.id] = c
}

func (s *fakeBackfillStore) markTranscribed(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[id].transcribed = true
}

// untranscribed returns eligible calls in backfill order: start DESC, id DESC.
// Caller holds s.mu.
func (s *fakeBackfillStore) untranscribed() []*fakeBackfillCall {
	var out []*fakeBackfillCall
	for _, c := range s.calls {
		if !c.transcribed {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].start.Equal(out[j].start) {
			return out[i].start.After(out[j].start)
		}
		return out[i].id > out[j].id
	})
	return out
}

func (s *fakeBackfillStore) CountUntranscribedCalls(ctx context.Context, filter database.BackfillFilter) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.untranscribed()), nil
}

func (s *fakeBackfillStore) ListUntranscribedCalls(ctx context.Context, filter database.BackfillFilter, after *database.UntranscribedCallKey, limit int) ([]database.UntranscribedCallKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	var out []database.UntranscribedCallKey
	for _, c := range s.untranscribed() {
		if after != nil && !s.ignoreCursor {
			// keep only rows with (start, id) < (after.start, after.id)
			if c.start.After(after.StartTime) || (c.start.Equal(after.StartTime) && c.id >= after.CallID) {
				continue
			}
		}
		out = append(out, database.UntranscribedCallKey{CallID: c.id, StartTime: c.start})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *fakeBackfillStore) GetCallForTranscription(ctx context.Context, callID int64) (*database.CallTranscriptionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.calls[callID]
	if !ok || c.loadErr {
		return nil, errors.New("call not found")
	}
	return &database.CallTranscriptionInfo{CallID: c.id, StartTime: c.start, AudioFilePath: c.audioPath}, nil
}

// fakeBackfillTranscriber stands in for the worker pool. An enqueued call is
// transcribed immediately unless it is listed in failInWorker, which models a
// call that enqueues fine but fails during transcription (e.g. ResolveFile
// cannot find its audio) and so stays untranscribed.
type fakeBackfillTranscriber struct {
	store        *fakeBackfillStore
	provider     string
	failInWorker map[int64]bool
	queueFull    map[int64]bool
	pending      int
	onEnqueue    func(callID int64)

	mu       sync.Mutex
	enqueued map[int64]int
}

func newFakeBackfillTranscriber(store *fakeBackfillStore) *fakeBackfillTranscriber {
	return &fakeBackfillTranscriber{
		store:        store,
		provider:     "whisper",
		failInWorker: make(map[int64]bool),
		queueFull:    make(map[int64]bool),
		enqueued:     make(map[int64]int),
	}
}

func (f *fakeBackfillTranscriber) Enqueue(j transcribe.Job) bool {
	if f.queueFull[j.CallID] {
		return false
	}
	f.mu.Lock()
	f.enqueued[j.CallID]++
	f.mu.Unlock()
	if !f.failInWorker[j.CallID] {
		f.store.markTranscribed(j.CallID)
	}
	if f.onEnqueue != nil {
		f.onEnqueue(j.CallID)
	}
	return true
}

func (f *fakeBackfillTranscriber) Stats() transcribe.QueueStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return transcribe.QueueStats{Pending: f.pending}
}

func (f *fakeBackfillTranscriber) ProviderName() string { return f.provider }
func (f *fakeBackfillTranscriber) MinDuration() float64 { return 0 }
func (f *fakeBackfillTranscriber) MaxDuration() float64 { return 0 }

// assertEnqueuedAtMostOnce fails if any call was enqueued more than once and
// returns the number of distinct calls enqueued.
func (f *fakeBackfillTranscriber) assertEnqueuedAtMostOnce(t *testing.T) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, n := range f.enqueued {
		if n > 1 {
			t.Errorf("call %d enqueued %d times, want at most once", id, n)
		}
	}
	return len(f.enqueued)
}

var backfillBase = time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

// seedBackfillCalls adds n untranscribed calls with ids 1..n. Every group of
// three shares a start time so paging has to break ties on call_id.
func seedBackfillCalls(store *fakeBackfillStore, n int) {
	for i := 1; i <= n; i++ {
		store.add(&fakeBackfillCall{id: int64(i), start: backfillBase.Add(time.Duration(i/3) * time.Second)})
	}
}

// runBackfillJob runs one job to completion, failing the test if it does not
// terminate (the #59 symptom) instead of hanging.
func runBackfillJob(t *testing.T, bm *BackfillManager, total int) *BackfillJob {
	t.Helper()
	job := &BackfillJob{ID: 1, Total: total, StartedAt: time.Now(), CreatedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		bm.processJob(ctx, job)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatalf("backfill job did not terminate: completed=%d failed=%d total=%d",
			job.Completed.Load(), job.Failed.Load(), job.Total)
	}
	return job
}

func assertBackfillCounts(t *testing.T, job *BackfillJob, completed, failed int64, total int) {
	t.Helper()
	if got := job.Completed.Load(); got != completed {
		t.Errorf("completed = %d, want %d", got, completed)
	}
	if got := job.Failed.Load(); got != failed {
		t.Errorf("failed = %d, want %d", got, failed)
	}
	if job.Total != total {
		t.Errorf("total = %d, want %d", job.Total, total)
	}
	if done := job.Completed.Load() + job.Failed.Load(); done > int64(job.Total) {
		t.Errorf("completed+failed = %d exceeds total %d", done, job.Total)
	}
}

// Issue #59: calls that enqueue fine but fail during transcription stay in the
// untranscribed set. The job must still terminate and enqueue each call once.
func TestBackfillProcessJob_TerminatesWhenCallsFailDuringTranscription(t *testing.T) {
	const n = 250 // spans three pages
	store := newFakeBackfillStore()
	seedBackfillCalls(store, n)
	tr := newFakeBackfillTranscriber(store)
	for id := int64(1); id <= n; id += 3 {
		tr.failInWorker[id] = true // e.g. audio file missing on disk
	}
	bm := newBackfillManager(context.Background(), store, tr, zerolog.Nop())

	job := runBackfillJob(t, bm, n)

	assertBackfillCounts(t, job, n, 0, n)
	if got := tr.assertEnqueuedAtMostOnce(t); got != n {
		t.Errorf("distinct calls enqueued = %d, want %d", got, n)
	}
	if store.listCalls != 3 {
		t.Errorf("list queries = %d, want 3 (pages of 100, 100, 50)", store.listCalls)
	}
}

// The reporter's case: every call in the set fails in the worker, so nothing
// ever drops out of the result set.
func TestBackfillProcessJob_TerminatesWhenEveryCallFailsDuringTranscription(t *testing.T) {
	const n = 1495
	store := newFakeBackfillStore()
	seedBackfillCalls(store, n)
	tr := newFakeBackfillTranscriber(store)
	for id := int64(1); id <= n; id++ {
		tr.failInWorker[id] = true
	}
	bm := newBackfillManager(context.Background(), store, tr, zerolog.Nop())

	job := runBackfillJob(t, bm, n)

	assertBackfillCounts(t, job, n, 0, n)
	if got := tr.assertEnqueuedAtMostOnce(t); got != n {
		t.Errorf("distinct calls enqueued = %d, want %d", got, n)
	}
}

// Enqueue failures are counted once each and not retried within the job.
func TestBackfillProcessJob_CountsEnqueueFailuresOnce(t *testing.T) {
	const n = 120
	store := newFakeBackfillStore()
	seedBackfillCalls(store, n)
	store.calls[5].loadErr = true
	store.calls[6].audioPath = "calls/call.dvcf"
	tr := newFakeBackfillTranscriber(store)
	tr.provider = "imbe" // skips every non-.dvcf call
	tr.queueFull[6] = true

	bm := newBackfillManager(context.Background(), store, tr, zerolog.Nop())
	job := runBackfillJob(t, bm, n)

	// Nothing is enqueued: 5 fails to load, 6 hits a full queue, and the rest
	// are skipped for IMBE. All of them stay untranscribed.
	assertBackfillCounts(t, job, 0, n, n)
	if got := tr.assertEnqueuedAtMostOnce(t); got != 0 {
		t.Errorf("distinct calls enqueued = %d, want 0", got)
	}
}

// Calls that become eligible mid-job (e.g. audio arrives for an older call)
// raise Total rather than pushing progress past it.
func TestBackfillProcessJob_TotalRaisedForCallsAddedMidJob(t *testing.T) {
	const n = 150 // more than one page, so the job queries again after the add
	store := newFakeBackfillStore()
	seedBackfillCalls(store, n)
	tr := newFakeBackfillTranscriber(store)
	added := false
	tr.onEnqueue = func(callID int64) {
		if !added {
			added = true
			store.add(&fakeBackfillCall{id: 10_000, start: backfillBase.Add(-time.Hour)})
		}
	}
	bm := newBackfillManager(context.Background(), store, tr, zerolog.Nop())

	job := runBackfillJob(t, bm, n)

	assertBackfillCounts(t, job, n+1, 0, n+1)
	tr.assertEnqueuedAtMostOnce(t)
}

// A queued job's total is refreshed when it starts.
func TestBackfillProcessJob_RecountsTotalAtStart(t *testing.T) {
	store := newFakeBackfillStore()
	seedBackfillCalls(store, 4)
	tr := newFakeBackfillTranscriber(store)
	bm := newBackfillManager(context.Background(), store, tr, zerolog.Nop())

	job := runBackfillJob(t, bm, 1495) // stale submit-time count

	assertBackfillCounts(t, job, 4, 0, 4)
}

// Defence in depth: if the store ever ignores the cursor (as the old offset-0
// re-query effectively did), the job stops instead of re-enqueueing calls.
func TestBackfillProcessJob_StopsWhenStoreDoesNotAdvance(t *testing.T) {
	const n = 250
	store := newFakeBackfillStore()
	store.ignoreCursor = true
	seedBackfillCalls(store, n)
	tr := newFakeBackfillTranscriber(store)
	for id := int64(1); id <= n; id++ {
		tr.failInWorker[id] = true
	}
	bm := newBackfillManager(context.Background(), store, tr, zerolog.Nop())

	job := runBackfillJob(t, bm, n)

	assertBackfillCounts(t, job, backfillBatchSize, 0, n)
	tr.assertEnqueuedAtMostOnce(t)
}

func waitForBackfill(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// DELETE /admin/transcribe-backfill[/{id}] maps to Cancel, which must stop a
// running job even while it is blocked waiting for queue room.
func TestBackfillCancelStopsActiveJob(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   func(jobID int) int
	}{
		{"by id", func(jobID int) int { return jobID }},
		{"all", func(int) int { return 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeBackfillStore()
			seedBackfillCalls(store, 10)
			tr := newFakeBackfillTranscriber(store)
			tr.pending = 50 // queue never drains, so the job blocks in waitForQueueRoom

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			bm := newBackfillManager(ctx, store, tr, zerolog.Nop())
			bm.Start()

			jobID, _, _, err := bm.Submit(ctx, BackfillFilters{})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := bm.Submit(ctx, BackfillFilters{}); err != nil {
				t.Fatal(err)
			}
			waitForBackfill(t, "job to start", func() bool {
				s := bm.Status()
				return s.Active != nil && s.Active.JobID == jobID
			})

			if !bm.Cancel(tc.id(jobID)) {
				t.Fatal("Cancel returned false for the active job")
			}
			if tc.id(jobID) == 0 {
				waitForBackfill(t, "all jobs to stop", func() bool {
					s := bm.Status()
					return s.Active == nil && len(s.Queued) == 0
				})
			} else {
				waitForBackfill(t, "next job to start", func() bool {
					s := bm.Status()
					return s.Active != nil && s.Active.JobID != jobID
				})
			}
			if got := tr.assertEnqueuedAtMostOnce(t); got != 0 {
				t.Errorf("distinct calls enqueued = %d, want 0", got)
			}
		})
	}
}

func TestUntranscribedCallKeyBefore(t *testing.T) {
	t0 := backfillBase
	tests := []struct {
		name string
		a, b database.UntranscribedCallKey
		want bool
	}{
		{"newer start first", database.UntranscribedCallKey{CallID: 1, StartTime: t0.Add(time.Second)}, database.UntranscribedCallKey{CallID: 2, StartTime: t0}, true},
		{"older start later", database.UntranscribedCallKey{CallID: 2, StartTime: t0}, database.UntranscribedCallKey{CallID: 1, StartTime: t0.Add(time.Second)}, false},
		{"tie: higher id first", database.UntranscribedCallKey{CallID: 9, StartTime: t0}, database.UntranscribedCallKey{CallID: 3, StartTime: t0}, true},
		{"tie: lower id later", database.UntranscribedCallKey{CallID: 3, StartTime: t0}, database.UntranscribedCallKey{CallID: 9, StartTime: t0}, false},
		{"equal is not before", database.UntranscribedCallKey{CallID: 3, StartTime: t0}, database.UntranscribedCallKey{CallID: 3, StartTime: t0.In(time.FixedZone("x", 3600))}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Before(tt.b); got != tt.want {
				t.Fatalf("Before = %v, want %v", got, tt.want)
			}
		})
	}
}
