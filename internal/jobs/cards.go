package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
)

// cardsPoolSize is the cards pool's one goroutine: the job is corpus-wide,
// so it never takes an index slot, and runs once at a time per daemon.
const cardsPoolSize = 1

// A cards job that finds the embedding model not answering waits half its
// age, between these, within the deferral budget: soon after an Ollama
// restart, at most 5 minutes after Ollama answers again, which is what
// bounds the wait once it does (nothing tells the queue it came back), and
// so at most 12 "job deferred" lines an hour through a long outage. A
// deferred job is pending, so the card changes meanwhile share it.
const (
	minCardsWait = time.Minute
	maxCardsWait = 5 * time.Minute
)

// cardsRetryDelay is how long after a cards job fails for good the next
// one runs, when cards are still stale (retryCards).
const cardsRetryDelay = time.Hour

// cardsWaitReason is what a deferred cards job waits for, worded to follow
// "waiting for".
const cardsWaitReason = "the embedding model to answer"

// cardsHandler builds the closure that embeds a tenant's stale cards
// (Indexer.EmbedStaleCards). A card the model can't take is the indexer's
// to set aside, as unembeddable, and the job goes on; a vector of the
// wrong width is the model's, answering every card at its width, so it
// fails the job permanently. An embed that got no answer
// (ollama.ErrUnreachable) is no verdict: the job is deferred, its attempt
// refunded, for half its age (deferral), and the cards embedded before stay
// embedded. Any other error is retried through the queue's backoff. A
// failed cards job marks no document; its permanent-failure hook queues the
// next an hour ahead while cards are stale (retryCards), and any card's
// change, a reindex-all or the next daemon start queues one sooner.
func cardsHandler(d Deps) HandlerFunc {
	return func(ctx context.Context, job *store.Job) error {
		start := time.Now()
		n, err := d.Indexer.EmbedStaleCards(ctx, d.Cards, job.TenantID)
		if n > 0 {
			d.Log.Info("cards embedded", "embedded", n, "took_ms", time.Since(start).Milliseconds())
		}
		switch {
		case err == nil:
			return nil
		case errors.Is(err, embedder.ErrWrongDimension):
			return fmt.Errorf("%w: cards: %w", ErrPermanent, err)
		case errors.Is(err, ollama.ErrUnreachable):
			return &DeferError{Until: start.Add(deferral(start.Sub(job.CreatedAt))), Reason: cardsWaitReason,
				Err: fmt.Errorf("cards: %w", err)}
		}
		return fmt.Errorf("cards: %w", err)
	}
}

// deferral is how long a cards job of age waits for the embedding model:
// half its age, at least minCardsWait and at most maxCardsWait.
func deferral(age time.Duration) time.Duration {
	return min(max(age/2, minCardsWait), maxCardsWait)
}

// retryCards is the cards pool's permanent-failure hook. A cards job that
// failed for good (a model answering at another width, its attempts spent,
// a day of deferrals) leaves its stale cards with no job until a card
// changes, a reindex-all or the next start; so while the tenant has stale
// cards it queues the next job cardsRetryDelay ahead, through EnqueueOnce,
// which keeps a job already pending. An hour, not sooner: a verdict stays
// one until the model is fixed, and the drift monitor reports a model
// answering at another width meanwhile, so the retry costs one failed job
// an hour and resumes by itself once the model is fixed. It runs for an
// orphan out of attempts too, when the daemon starts.
func retryCards(d Deps) PermFailHook {
	return func(ctx context.Context, job *store.Job, cause error) error {
		stale, err := d.Cards.CountStaleCards(ctx, job.TenantID)
		if err != nil {
			return fmt.Errorf("retry the cards: %w", err)
		}
		if stale == 0 {
			return nil
		}
		next := &store.Job{TenantID: job.TenantID, Kind: store.JobKindCards,
			RunAfter: time.Now().UTC().Add(cardsRetryDelay)}
		if _, err := d.Queue.EnqueueOnce(ctx, next); err != nil {
			return fmt.Errorf("retry the cards: queue the next cards job: %w", err)
		}
		d.Log.Warn("cards: the cards job failed for good; the next runs later", "err", cause, "stale", stale,
			"next_job", next.ID, "run_after", next.RunAfter)
		return nil
	}
}
