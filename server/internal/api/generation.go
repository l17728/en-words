package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var errGenerationRetired = errors.New("classroom generation retired")
var errGenerationFailed = errors.New("classroom generation failed closed")

const generationClosedBit uint64 = 1 << 63
const generationFailedBit uint64 = 1 << 62
const generationCountMask uint64 = generationFailedBit - 1

// generationCoordinator owns per-lesson generations. The atomic admission
// word lets permits be acquired while Room.mu is held without nesting the
// coordinator map lock under classroom locks.
type generationCoordinator struct {
	mu      sync.Mutex
	seq     uint64
	lessons map[int64]*lessonGeneration
	failed  map[int64]bool
}

type lessonGeneration struct {
	id         uint64
	state      atomic.Uint64 // closed bit plus admitted leases/effects
	mu         sync.Mutex
	cancels    map[*generationLease]context.CancelFunc
	closers    map[uint64]func()
	nextCloser uint64
	drained    chan struct{}
	drainOnce  sync.Once
}

type generationToken struct {
	owner  *generationCoordinator
	lesson int64
	id     uint64
	state  *lessonGeneration
}

type generationLease struct {
	token  generationToken
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
}

type generationEffect struct {
	token  generationToken
	mu     sync.Mutex
	closed bool
}

type generationRetirement struct {
	owner *generationCoordinator
	token generationToken
	done  <-chan struct{}
}

func newGenerationCoordinator() *generationCoordinator {
	return &generationCoordinator{lessons: make(map[int64]*lessonGeneration), failed: make(map[int64]bool)}
}

func newLessonGeneration(id uint64) *lessonGeneration {
	return &lessonGeneration{id: id, cancels: map[*generationLease]context.CancelFunc{}, closers: map[uint64]func(){}, drained: make(chan struct{})}
}

func (g *lessonGeneration) enter() bool {
	for {
		state := g.state.Load()
		if state&(generationClosedBit|generationFailedBit) != 0 || state&generationCountMask == generationCountMask {
			return false
		}
		if g.state.CompareAndSwap(state, state+1) {
			return true
		}
	}
}

func (g *lessonGeneration) leave() {
	state := g.state.Add(^uint64(0)) // subtract one without losing the closed bit
	if state&generationClosedBit != 0 && state&generationCountMask == 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
}

func (c *generationCoordinator) acquire(ctx context.Context, lessonID int64) (*generationLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if lessonID <= 0 {
		return nil, errLessonUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed[lessonID] {
		return nil, errGenerationFailed
	}
	g := c.lessons[lessonID]
	if g == nil {
		c.seq++
		g = newLessonGeneration(c.seq)
		c.lessons[lessonID] = g
	}
	return c.newLeaseLocked(ctx, lessonID, g)
}

func (c *generationCoordinator) acquireFor(ctx context.Context, token generationToken) (*generationLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if token.owner != c || token.lesson <= 0 || token.id == 0 || c.failed[token.lesson] {
		return nil, errGenerationFailed
	}
	g := c.lessons[token.lesson]
	if g == nil || g != token.state || g.id != token.id {
		return nil, errGenerationRetired
	}
	return c.newLeaseLocked(ctx, token.lesson, g)
}

func (c *generationCoordinator) newLeaseLocked(ctx context.Context, lessonID int64, g *lessonGeneration) (*generationLease, error) {
	if !g.enter() {
		return nil, errGenerationRetired
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	lease := &generationLease{token: generationToken{owner: c, lesson: lessonID, id: g.id, state: g}, ctx: leaseCtx, cancel: cancel}
	g.mu.Lock()
	if g.state.Load()&generationClosedBit != 0 {
		g.mu.Unlock()
		cancel()
		g.leave()
		return nil, errGenerationRetired
	}
	g.cancels[lease] = cancel
	g.mu.Unlock()
	return lease, nil
}

func (l *generationLease) Context() context.Context { return l.ctx }
func (l *generationLease) Token() generationToken   { return l.token }

func (l *generationLease) Release() {
	if l == nil || l.token.state == nil {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	g := l.token.state
	g.mu.Lock()
	_, held := g.cancels[l]
	delete(g.cancels, l)
	g.mu.Unlock()
	if held {
		g.leave()
	}
}

func (t generationToken) Permit() (*generationEffect, error) {
	if t.owner == nil || t.state == nil {
		return nil, errGenerationRetired
	}
	if t.state.state.Load()&generationFailedBit != 0 {
		return nil, errGenerationFailed
	}
	if !t.state.enter() {
		return nil, errGenerationRetired
	}
	return &generationEffect{token: t}, nil
}

func (e *generationEffect) Release() {
	if e == nil || e.token.state == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.mu.Unlock()
	e.token.state.leave()
}

func (l *generationLease) AddCloser(closeFn func()) (remove func(), err error) {
	if l == nil || closeFn == nil {
		return nil, errGenerationRetired
	}
	g := l.token.state
	g.mu.Lock()
	if g.state.Load()&generationClosedBit != 0 {
		g.mu.Unlock()
		return nil, errGenerationRetired
	}
	g.nextCloser++
	id := g.nextCloser
	g.closers[id] = closeFn
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.closers, id)
			g.mu.Unlock()
		})
	}, nil
}

// retire closes admission atomically while holding only the coordinator map
// lock. Cancellation and connection callbacks run after every lock is released.
func (c *generationCoordinator) retire(lessonID int64) (*generationRetirement, error) {
	c.mu.Lock()
	g := c.lessons[lessonID]
	if g == nil {
		c.mu.Unlock()
		return nil, errGenerationRetired
	}
	state := g.state.Load()
	if state&generationClosedBit != 0 {
		retirement := &generationRetirement{owner: c, token: generationToken{owner: c, lesson: lessonID, id: g.id, state: g}, done: g.drained}
		c.mu.Unlock()
		return retirement, nil
	}
	for !g.state.CompareAndSwap(state, state|generationClosedBit) {
		state = g.state.Load()
		if state&generationClosedBit != 0 {
			break
		}
	}
	token := generationToken{owner: c, lesson: lessonID, id: g.id, state: g}
	if g.state.Load()&generationCountMask == 0 {
		g.drainOnce.Do(func() { close(g.drained) })
	}
	g.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(g.cancels))
	for _, cancel := range g.cancels {
		cancels = append(cancels, cancel)
	}
	closers := make([]func(), 0, len(g.closers))
	for _, closeFn := range g.closers {
		closers = append(closers, closeFn)
	}
	g.mu.Unlock()
	retirement := &generationRetirement{owner: c, token: token, done: g.drained}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, closeFn := range closers {
		closeFn()
	}
	return retirement, nil
}

func (r *generationRetirement) Wait(ctx context.Context) error {
	if r == nil || r.done == nil {
		return errGenerationRetired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Advance is called only after reset cleanup and the final stale-handle sweep.
func (r *generationRetirement) Advance() error {
	if r == nil || r.owner == nil {
		return errGenerationRetired
	}
	select {
	case <-r.done:
	default:
		return errGenerationRetired
	}
	c := r.owner
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.lessons[r.token.lesson]
	if c.failed[r.token.lesson] || g != r.token.state || g.id != r.token.id || g.state.Load()&generationCountMask != 0 || g.state.Load()&generationClosedBit == 0 {
		return errGenerationFailed
	}
	delete(c.lessons, r.token.lesson)
	return nil
}

func (r *generationRetirement) Fail() {
	if r == nil || r.owner == nil {
		return
	}
	c := r.owner
	c.mu.Lock()
	c.failed[r.token.lesson] = true
	c.mu.Unlock()
	if r.token.state != nil {
		r.token.state.state.Or(generationClosedBit | generationFailedBit)
		if r.token.state.state.Load()&generationCountMask == 0 {
			r.token.state.drainOnce.Do(func() { close(r.token.state.drained) })
		}
	}
}

type generationLeaseContextKey struct{}

func withGenerationLease(ctx context.Context, lease *generationLease) context.Context {
	return context.WithValue(ctx, generationLeaseContextKey{}, lease)
}

func generationLeaseFromContext(ctx context.Context, lessonID int64) *generationLease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(generationLeaseContextKey{}).(*generationLease)
	if lease == nil || (lessonID > 0 && lease.token.lesson != lessonID) {
		return nil
	}
	return lease
}

func (r *Room) operationLease(ctx context.Context) (*generationLease, bool, error) {
	if r == nil || r.app == nil || r.generation.owner == nil {
		return nil, false, errGenerationRetired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if lease := generationLeaseFromContext(ctx, r.lessonID); lease != nil {
		if lease.token.id != r.generation.id || lease.token.state != r.generation.state {
			return nil, false, errGenerationRetired
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return lease, false, nil
	}
	lease, err := r.app.generations.acquireFor(ctx, r.generation)
	if err != nil {
		return nil, false, err
	}
	return lease, true, nil
}
