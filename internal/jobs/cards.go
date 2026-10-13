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
// age, between these: soon after an Ollama restart, and no more often than
// twice an hour through a long outage, within the deferral budget.
const (
	minCardsWait = time.Minute
	maxCardsWait = 30 * time.Minute
)

// cardsWaitReason is what a deferred cards job waits for, worded to follow
// "waiting for".
const cardsWaitReason = "the embedding model to answer"

// cardsHandler builds the closure that embeds a tenant's stale cards
// (Indexer.EmbedStaleCards). A card the model can't take, or a vector of
// the wrong width, fails it permanently. An embed that got no answer
// (ollama.ErrUnreachable) is no verdict: the job is deferred, its attempt
// refunded, for half its age (deferral), and the cards embedded before stay
// embedded. Any other error is retried through the queue's backoff. A
// failed cards job marks no document: stale cards wait for the next job,
// which any card's change, a reindex-all or the next daemon start queues.
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
		case errors.Is(err, embedder.ErrInputTooLong), errors.Is(err, embedder.ErrWrongDimension):
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
