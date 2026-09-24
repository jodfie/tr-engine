package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/snarg/tr-engine/internal/database"
	"github.com/snarg/tr-engine/internal/transcribe"
)

// BackfillJob represents a queued backfill request.
//
// Each matching call is handed to the transcription queue at most once per
// job. Completed counts calls successfully enqueued (not calls whose
// transcription later succeeded); Failed counts calls that could not be
// enqueued (load error, unsupported by the provider, or queue full). Total is
// the number of matching calls when the job starts, raised if more calls
// become eligible while it runs, so Completed+Failed never exceeds it.
// Once the job is submitted, Total is guarded by the manager's mutex.
type BackfillJob struct {
	ID        int
	Filters   BackfillFilters
	Total     int
	Completed atomic.Int64
	Failed    atomic.Int64
	StartedAt time.Time
	CreatedAt time.Time
}

// BackfillFilters are the user-provided filters for a backfill job.
type BackfillFilters struct {
	SystemID  *int       `json:"system_id,omitempty"`
	Tgids     []int      `json:"tgids,omitempty"`
	StartTime *time.Time `json:"start_time,omitempty"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}

// BackfillStatus is the API-facing status of the backfill manager.
type BackfillStatus struct {
	Active *BackfillJobStatus  `json:"active"`
	Queued []BackfillJobStatus `json:"queued"`
}

// BackfillJobStatus is the API-facing status of a single backfill job.
type BackfillJobStatus struct {
	JobID     int             `json:"job_id"`
	Filters   BackfillFilters `json:"filters"`
	Total     int             `json:"total"`
	Completed int64           `json:"completed"`
	Failed    int64           `json:"failed"`
	StartedAt *time.Time      `json:"started_at,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// backfillStore is the subset of *database.DB used by the backfill manager.
type backfillStore interface {
	CountUntranscribedCalls(ctx context.Context, filter database.BackfillFilter) (int, error)
	ListUntranscribedCalls(ctx context.Context, filter database.BackfillFilter, after *database.UntranscribedCallKey, limit int) ([]database.UntranscribedCallKey, error)
	GetCallForTranscription(ctx context.Context, callID int64) (*database.CallTranscriptionInfo, error)
}

// backfillTranscriber is the subset of *transcribe.WorkerPool used by the
// backfill manager.
type backfillTranscriber interface {
	Enqueue(j transcribe.Job) bool
	Stats() transcribe.QueueStats
	ProviderName() string
	MinDuration() float64
	MaxDuration() float64
}

// backfillBatchSize is the number of calls fetched per keyset page.
const backfillBatchSize = 100

// BackfillManager processes a queue of backfill jobs sequentially,
// drip-feeding untranscribed calls into the transcription worker pool.
type BackfillManager struct {
	db          backfillStore
	transcriber backfillTranscriber
	log         zerolog.Logger
	minDuration float64
	maxDuration float64

	mu       sync.Mutex
	nextID   int
	queue    []*BackfillJob
	active   *BackfillJob
	cancelFn context.CancelFunc // cancels the active job

	submit chan struct{} // signals the loop that a new job was submitted
	ctx    context.Context
}

// NewBackfillManager creates a new backfill manager.
func NewBackfillManager(ctx context.Context, db *database.DB, transcriber *transcribe.WorkerPool, log zerolog.Logger) *BackfillManager {
	return newBackfillManager(ctx, db, transcriber, log)
}

func newBackfillManager(ctx context.Context, db backfillStore, transcriber backfillTranscriber, log zerolog.Logger) *BackfillManager {
	return &BackfillManager{
		db:          db,
		transcriber: transcriber,
		log:         log.With().Str("component", "backfill").Logger(),
		minDuration: transcriber.MinDuration(),
		maxDuration: transcriber.MaxDuration(),
		submit:      make(chan struct{}, 1),
		ctx:         ctx,
	}
}

// Start launches the background processing goroutine.
func (bm *BackfillManager) Start() {
	go bm.loop()
	bm.log.Info().Msg("backfill manager started")
}

// Submit adds a backfill job to the queue. Returns the job ID, queue position, and total count.
func (bm *BackfillManager) Submit(ctx context.Context, filters BackfillFilters) (jobID, position, total int, err error) {
	// Count matching calls
	dbFilter := bm.toDBFilter(filters)
	total, err = bm.db.CountUntranscribedCalls(ctx, dbFilter)
	if err != nil {
		return 0, 0, 0, err
	}

	bm.mu.Lock()
	bm.nextID++
	job := &BackfillJob{
		ID:        bm.nextID,
		Filters:   filters,
		Total:     total,
		CreatedAt: time.Now(),
	}
	bm.queue = append(bm.queue, job)
	position = len(bm.queue) - 1
	if bm.active != nil {
		position++ // account for the running job
	}
	jobID = job.ID
	bm.mu.Unlock()

	// Signal the loop
	select {
	case bm.submit <- struct{}{}:
	default:
	}

	bm.log.Info().Int("job_id", jobID).Int("total", total).Int("position", position).Msg("backfill job submitted")
	return jobID, position, total, nil
}

// Status returns the current backfill status.
func (bm *BackfillManager) Status() BackfillStatus {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	var status BackfillStatus
	if bm.active != nil {
		sa := bm.active.StartedAt
		status.Active = &BackfillJobStatus{
			JobID:     bm.active.ID,
			Filters:   bm.active.Filters,
			Total:     bm.active.Total,
			Completed: bm.active.Completed.Load(),
			Failed:    bm.active.Failed.Load(),
			StartedAt: &sa,
			CreatedAt: bm.active.CreatedAt,
		}
	}
	status.Queued = make([]BackfillJobStatus, 0, len(bm.queue))
	for _, j := range bm.queue {
		status.Queued = append(status.Queued, BackfillJobStatus{
			JobID:     j.ID,
			Filters:   j.Filters,
			Total:     j.Total,
			CreatedAt: j.CreatedAt,
		})
	}
	return status
}

// Cancel cancels a job by ID. If id <= 0, cancels all jobs.
// Returns true if a job was found and cancelled.
func (bm *BackfillManager) Cancel(id int) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if id <= 0 {
		// Cancel all
		found := bm.active != nil || len(bm.queue) > 0
		bm.queue = nil
		if bm.cancelFn != nil {
			bm.cancelFn()
		}
		return found
	}

	// Cancel active job
	if bm.active != nil && bm.active.ID == id {
		if bm.cancelFn != nil {
			bm.cancelFn()
		}
		return true
	}

	// Remove from queue
	for i, j := range bm.queue {
		if j.ID == id {
			bm.queue = append(bm.queue[:i], bm.queue[i+1:]...)
			return true
		}
	}
	return false
}

func (bm *BackfillManager) loop() {
	for {
		// Try to grab the next job
		bm.mu.Lock()
		if len(bm.queue) == 0 {
			bm.mu.Unlock()
			// Wait for a submission or shutdown
			select {
			case <-bm.ctx.Done():
				return
			case <-bm.submit:
				continue
			}
		}
		job := bm.queue[0]
		bm.queue = bm.queue[1:]
		jobCtx, cancel := context.WithCancel(bm.ctx)
		job.StartedAt = time.Now()
		bm.active = job
		bm.cancelFn = cancel
		bm.mu.Unlock()

		bm.log.Info().Int("job_id", job.ID).Int("total", job.Total).Msg("backfill job starting")

		bm.processJob(jobCtx, job)
		cancel()

		bm.mu.Lock()
		bm.active = nil
		bm.cancelFn = nil
		bm.mu.Unlock()

		msg := "backfill job finished"
		if jobCtx.Err() != nil {
			msg = "backfill job cancelled"
		}
		bm.log.Info().
			Int("job_id", job.ID).
			Int64("completed", job.Completed.Load()).
			Int64("failed", job.Failed.Load()).
			Msg(msg)
	}
}

func (bm *BackfillManager) processJob(ctx context.Context, job *BackfillJob) {
	dbFilter := bm.toDBFilter(job.Filters)

	// A queued job's count can be stale by the time it starts (an earlier job
	// may have covered the same calls), so refresh it.
	countCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	total, err := bm.db.CountUntranscribedCalls(countCtx, dbFilter)
	cancel()
	if err == nil {
		bm.mu.Lock()
		job.Total = total
		bm.mu.Unlock()
	} else if ctx.Err() == nil {
		bm.log.Warn().Err(err).Int("job_id", job.ID).Msg("backfill recount failed, keeping submit-time total")
	}

	// Walk the matching calls with a keyset cursor. Each page starts strictly
	// after the last call handed out, so a call is enqueued at most once per
	// job and the job ends when the cursor runs off the end, whether or not
	// the enqueued calls were transcribed. (Re-querying from the top and
	// relying on transcribed calls dropping out loops forever on any call
	// that fails during transcription: issue #59.)
	var cursor *database.UntranscribedCallKey
	for {
		if ctx.Err() != nil {
			return
		}

		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		batch, err := bm.db.ListUntranscribedCalls(queryCtx, dbFilter, cursor, backfillBatchSize)
		cancel()

		if err != nil {
			bm.log.Warn().Err(err).Int("job_id", job.ID).Msg("backfill query failed")
			return
		}

		for _, key := range batch {
			// The query guarantees strictly advancing keys; enforce it here too so
			// a regression can never re-enqueue a call or spin the job.
			if cursor != nil && !cursor.Before(key) {
				bm.log.Error().
					Int("job_id", job.ID).
					Int64("call_id", key.CallID).
					Int64("cursor_call_id", cursor.CallID).
					Msg("backfill query returned a call at or before the cursor, stopping job")
				return
			}
			k := key
			cursor = &k

			if ctx.Err() != nil {
				return
			}
			// Wait until the transcription queue has room
			bm.waitForQueueRoom(ctx)
			if ctx.Err() != nil {
				return
			}

			bm.recordResult(job, bm.enqueueCall(ctx, key.CallID))
		}

		if len(batch) < backfillBatchSize {
			return // last page
		}
	}
}

// recordResult counts one call as enqueued or failed. Total is raised if calls
// became eligible after it was counted, so progress never exceeds it.
func (bm *BackfillManager) recordResult(job *BackfillJob, enqueued bool) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if enqueued {
		job.Completed.Add(1)
	} else {
		job.Failed.Add(1)
	}
	if done := int(job.Completed.Load() + job.Failed.Load()); done > job.Total {
		job.Total = done
	}
}

// waitForQueueRoom blocks until the transcription queue has <= 1 pending jobs.
func (bm *BackfillManager) waitForQueueRoom(ctx context.Context) {
	if bm.transcriber.Stats().Pending <= 1 {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if bm.transcriber.Stats().Pending <= 1 {
				return
			}
		}
	}
}

func (bm *BackfillManager) enqueueCall(ctx context.Context, callID int64) bool {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	c, err := bm.db.GetCallForTranscription(queryCtx, callID)
	if err != nil {
		bm.log.Warn().Err(err).Int64("call_id", callID).Msg("backfill: failed to load call")
		return false
	}
	if backfillShouldSkipForProvider(bm.transcriber.ProviderName(), c.AudioFilePath) {
		bm.log.Debug().
			Int64("call_id", callID).
			Str("provider", bm.transcriber.ProviderName()).
			Str("audio_file_path", c.AudioFilePath).
			Msg("backfill: skipping call without dvcf file for IMBE provider")
		return false
	}
	return bm.transcriber.Enqueue(transcribe.Job{
		CallID:        c.CallID,
		CallStartTime: c.StartTime,
		SystemID:      c.SystemID,
		Tgid:          c.Tgid,
		Duration:      derefFloat32(c.Duration),
		AudioFilePath: c.AudioFilePath,
		CallFilename:  c.CallFilename,
		SrcList:       c.SrcList,
		TgAlphaTag:    c.TgAlphaTag,
		TgDescription: c.TgDescription,
		TgTag:         c.TgTag,
		TgGroup:       c.TgGroup,
	})
}

func backfillShouldSkipForProvider(providerName, audioFilePath string) bool {
	return strings.EqualFold(providerName, "imbe") && !strings.EqualFold(filepath.Ext(audioFilePath), ".dvcf")
}

func (bm *BackfillManager) toDBFilter(f BackfillFilters) database.BackfillFilter {
	return database.BackfillFilter{
		SystemID:    f.SystemID,
		Tgids:       f.Tgids,
		StartTime:   f.StartTime,
		EndTime:     f.EndTime,
		MinDuration: bm.minDuration,
		MaxDuration: bm.maxDuration,
	}
}
