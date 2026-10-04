package recall

import (
	"context"
	"sync"
)

// group runs functions in goroutines, keeps the first error and cancels the
// shared context when one fails. It is a minimal errgroup built with the
// standard library.
type group struct {
	wg     sync.WaitGroup
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

// newGroup returns a group and the context its functions must use.
func newGroup(ctx context.Context) (*group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &group{cancel: cancel}, ctx
}

// Go runs f in a new goroutine. It may be called from a function running in
// the group, before that function returns.
func (g *group) Go(f func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := f(); err != nil {
			g.once.Do(func() {
				g.err = err
				g.cancel()
			})
		}
	}()
}

// Wait blocks until every function has returned and gives back the first error.
func (g *group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}
